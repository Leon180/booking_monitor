#!/bin/bash
# scripts/benchmark_k6.sh
# Usage: ./scripts/benchmark_k6.sh [VUS] [DURATION]

VUS=${1:-1000}
DURATION=${2:-60s}
TIMESTAMP=$(date +"%Y%m%d_%H%M%S")
REPORT_DIR="docs/benchmarks/${TIMESTAMP}_c${VUS}"
mkdir -p "$REPORT_DIR"

REPORT_FILE="$REPORT_DIR/report.md"
STATS_FILE="$REPORT_DIR/stats.csv"

echo "Starting Benchmark C${VUS}..."
echo "Report Dir: $REPORT_DIR"

# 1. Record Environment
echo "# Benchmark Report: ${VUS} Concurrent Users" > "$REPORT_FILE"
echo "**Date**: $(date)" >> "$REPORT_FILE"
echo "" >> "$REPORT_FILE"
echo "## 1. Test Environment" >> "$REPORT_FILE"
echo "| Component | Specification |" >> "$REPORT_FILE"
echo "| :--- | :--- |" >> "$REPORT_FILE"
echo "| **Host OS** | $(uname -sr) |" >> "$REPORT_FILE"
echo "| **Go Version** | $(go version) |" >> "$REPORT_FILE"
echo "| **Commit** | $(git rev-parse --short HEAD) |" >> "$REPORT_FILE"
echo "| **Parameters** | VUS=${VUS}, DURATION=${DURATION} |" >> "$REPORT_FILE"

# Try to get DB version (if container running)
DB_VER=$(docker exec booking_db psql -U user -d booking -t -c "SELECT version();" 2>/dev/null | tr -d '\n' | xargs)
echo "| **Database** | ${DB_VER:-Unknown} |" >> "$REPORT_FILE"
echo "" >> "$REPORT_FILE"

# 2. Start Monitoring
echo "Starting resource monitor..."
echo "Timestamp,Container,CPU,MemUsage" > "$STATS_FILE"
(
  while true; do
    ts=$(date +"%H:%M:%S")
    docker stats --no-stream --format "{{.Name}},{{.CPUPerc}},{{.MemUsage}}" | while read line; do
      echo "$ts,$line" >> "$STATS_FILE"
    done
    sleep 1
  done
) &
MONITOR_PID=$!

# 3. Run Test
echo "## 2. Execution Log" >> "$REPORT_FILE"
echo "\`\`\`" >> "$REPORT_FILE"
echo "Running K6..."
make stress-k6 VUS=$VUS DURATION=$DURATION 2>&1 | tee -a "$REPORT_FILE"
EXIT_CODE=${PIPESTATUS[0]}
echo "\`\`\`" >> "$REPORT_FILE"

# 4. Stop Monitoring
kill $MONITOR_PID

# 5. Summarize Resources
echo "" >> "$REPORT_FILE"
echo "## 3. Resource Usage (Peak)" >> "$REPORT_FILE"
echo "| Container | Peak CPU | Peak Mem |" >> "$REPORT_FILE"
echo "| :--- | :--- | :--- |" >> "$REPORT_FILE"

# Simple awk to find max CPU/Mem (approximate parsing)
for container in booking_db booking_app; do
  peak_cpu=$(grep "$container" "$STATS_FILE" | awk -F',' '{print $3}' | sort -nr | head -1)
  peak_mem=$(grep "$container" "$STATS_FILE" | awk -F',' '{print $4}' | sort -hr | head -1)
  echo "| **$container** | $peak_cpu | $peak_mem |" >> "$REPORT_FILE"
done

echo "" >> "$REPORT_FILE"
if [ $EXIT_CODE -eq 0 ]; then
  echo "✅ **Result**: PASS" >> "$REPORT_FILE"
else
  echo "❌ **Result**: FAIL" >> "$REPORT_FILE"
fi

echo "Benchmark complete. Report saved to $REPORT_FILE"
exit $EXIT_CODE
