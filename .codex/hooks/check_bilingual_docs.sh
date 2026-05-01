#!/bin/bash
# Bilingual docs sync reminder.
#
# Fires after Edit/Write on one of the paired English/Chinese docs and injects
# a system reminder so the agent updates the other side before ending the turn.
#
# See AGENTS.md (or .claude/CLAUDE.md) > Bilingual Documentation Contract.

set -euo pipefail

# `jq` is required to parse the PostToolUse JSON payload + emit the
# response JSON. If it's missing the hook can't function — log loudly
# (visible in the agent's tool-result stream) and exit non-zero so the
# absence is observable rather than silent.
if ! command -v jq >/dev/null 2>&1; then
  echo "check_bilingual_docs: jq not found — bilingual sync reminder DISABLED" >&2
  exit 1
fi

# Read PostToolUse JSON payload from stdin.
input="$(cat)"

# Extract file_path from tool_input (works for Edit, Write, MultiEdit).
file_path="$(printf '%s' "$input" | jq -r '.tool_input.file_path // empty')"
[ -z "$file_path" ] && exit 0

# Resolve repo-relative path. CODEX_PROJECT_DIR is set by Codex; fall back to
# CLAUDE_PROJECT_DIR or PWD if unset.
repo_root="${CODEX_PROJECT_DIR:-${CLAUDE_PROJECT_DIR:-$(pwd)}}"
rel_path="${file_path#"$repo_root"/}"

# If the strip didn't take (e.g. neither env var set AND pwd wasn't
# the repo root), `rel_path` will still be the absolute path.
# Without this guard the case statement below silently falls through
# to the catch-all and no reminder fires — the exact silent-failure
# mode the bilingual contract is meant to prevent. Surface this
# loudly so the operator sees the misconfiguration instead of
# silently drifting docs.
if [[ "$rel_path" == /* ]]; then
  echo "check_bilingual_docs: could not resolve repo-relative path for '${file_path}' (CODEX_PROJECT_DIR='${CODEX_PROJECT_DIR:-unset}', CLAUDE_PROJECT_DIR='${CLAUDE_PROJECT_DIR:-unset}', pwd='$(pwd)') — bilingual sync reminder DISABLED for this edit" >&2
  exit 1
fi

# Paired files — must stay structurally identical.
paired=""
case "$rel_path" in
  "AGENTS.md")                 paired="AGENTS.zh-TW.md" ;;
  "AGENTS.zh-TW.md")           paired="AGENTS.md" ;;
  ".claude/CLAUDE.md")         paired=".claude/CLAUDE.zh-TW.md" ;;
  ".claude/CLAUDE.zh-TW.md")   paired=".claude/CLAUDE.md" ;;
  "README.md")                 paired="README.zh-TW.md" ;;
  "README.zh-TW.md")           paired="README.md" ;;
  "docs/PROJECT_SPEC.md")      paired="docs/PROJECT_SPEC.zh-TW.md" ;;
  "docs/PROJECT_SPEC.zh-TW.md") paired="docs/PROJECT_SPEC.md" ;;
  "docs/monitoring.md")        paired="docs/monitoring.zh-TW.md" ;;
  "docs/monitoring.zh-TW.md")  paired="docs/monitoring.md" ;;
  *) exit 0 ;;
esac

# Inject a system reminder back into the conversation via PostToolUse
# additionalContext. The agent will see this right after the tool result.
reminder="⚠️ Bilingual docs contract: you just edited '${rel_path}'. Its paired file '${paired}' MUST be updated in the same response to keep both versions structurally identical (same sections, same tables, same ordering). Code/commands/filenames stay in English in both versions — only prose is translated. Use Taiwan Traditional Chinese conventions (資料庫/介面/物件) in the zh-TW file. Do NOT end your turn until both files are updated. See AGENTS.md (or .claude/CLAUDE.md) > Bilingual Documentation Contract for the full rule."

jq -n --arg ctx "$reminder" '{
  hookSpecificOutput: {
    hookEventName: "PostToolUse",
    additionalContext: $ctx
  }
}'
