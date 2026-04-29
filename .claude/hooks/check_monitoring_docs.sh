#!/bin/bash
# Monitoring docs reminder.
#
# Fires after Edit/Write/MultiEdit on any file that defines or wires
# observability surfaces (Prometheus metrics, alerts, scrape config,
# Grafana dashboards, custom collectors). Injects a system reminder so
# Claude updates docs/monitoring.md + docs/monitoring.zh-TW.md before
# ending the turn.
#
# This is a peer to check_bilingual_docs.sh — same hook event
# (PostToolUse), same reminder injection mechanism, but watches the
# *source* of observability rather than a paired doc.
#
# Why a hook instead of trusting the human / agent to remember:
# monitoring docs are easy to forget. A new metric that's defined in
# code but never documented in monitoring.md is functionally invisible
# — operators cannot use a metric they don't know exists.

set -euo pipefail

input="$(cat)"
file_path="$(printf '%s' "$input" | jq -r '.tool_input.file_path // empty')"
[ -z "$file_path" ] && exit 0

repo_root="${CLAUDE_PROJECT_DIR:-$(pwd)}"
rel_path="${file_path#"$repo_root"/}"

# Match the canonical observability surfaces. Patterns are intentionally
# narrow — adding a new collector means adding a new line here, which
# is the right cost for the reminder being precise.
should_remind=0
case "$rel_path" in
  internal/infrastructure/observability/metrics.go)             should_remind=1 ;;
  internal/infrastructure/observability/streams_collector.go)   should_remind=1 ;;
  internal/infrastructure/observability/db_pool_collector.go)   should_remind=1 ;;
  internal/infrastructure/observability/*_collector.go)         should_remind=1 ;;
  deploy/prometheus/alerts.yml)                                 should_remind=1 ;;
  deploy/prometheus/prometheus.yml)                             should_remind=1 ;;
  deploy/grafana/provisioning/dashboards/*.json)                should_remind=1 ;;
esac

[ "$should_remind" -eq 0 ] && exit 0

reminder="📊 Monitoring contract: you just edited an observability surface ('${rel_path}'). Review whether docs/monitoring.md AND docs/monitoring.zh-TW.md need an update — new metric → §2 inventory; new alert → §5 catalog (+ §5 'forcing fire' recipe if it's a stream/DLQ-style alert that's reproducible); new dashboard panel → §4 list. Operators rely on this guide; an undocumented metric is functionally invisible. Both EN and zh-TW must be updated together (same paired-doc rule as PROJECT_SPEC). If no doc change is needed, briefly say why before ending the turn."

jq -n --arg ctx "$reminder" '{
  hookSpecificOutput: {
    hookEventName: "PostToolUse",
    additionalContext: $ctx
  }
}'
