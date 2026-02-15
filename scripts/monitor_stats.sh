#!/bin/bash
# scripts/monitor_stats.sh
# Monitors docker stats and logs to a file

LOG_FILE="benchmark_stats.log"
echo "Starting monitoring to $LOG_FILE..."
echo "Timestamp,Container,CPU,MemUsage" > $LOG_FILE

# Run in background
while true; do
  timestamp=$(date +"%Y-%m-%d %H:%M:%S")
  stats=$(docker stats --no-stream --format "{{.Name}},{{.CPUPerc}},{{.MemUsage}}")
  
  # Prepend timestamp to each line
  while IFS= read -r line; do
    echo "$timestamp,$line" >> $LOG_FILE
  done <<< "$stats"
  
  sleep 1
done &

MONITOR_PID=$!
echo "Monitor PID: $MONITOR_PID"

# Run the test
echo "Running usage: make stress-k6 VUS=1000 DURATION=60s"
make stress-k6 VUS=1000 DURATION=60s

# Stop monitoring
kill $MONITOR_PID
echo "Monitoring stopped."
