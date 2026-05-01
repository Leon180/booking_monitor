#!/bin/bash
# Bilingual docs sync reminder.
#
# Fires after Edit/Write on one of the paired English/Chinese docs and injects
# a system reminder so the agent updates the other side before ending the turn.
#
# See AGENTS.md (or .claude/CLAUDE.md) > Bilingual Documentation Contract.

set -euo pipefail

# Read PostToolUse JSON payload from stdin.
input="$(cat)"

# Extract file_path from tool_input (works for Edit, Write, MultiEdit).
file_path="$(printf '%s' "$input" | jq -r '.tool_input.file_path // empty')"
[ -z "$file_path" ] && exit 0

# Resolve repo-relative path. CODEX_PROJECT_DIR is set by Codex; fall back to
# CLAUDE_PROJECT_DIR or PWD if unset.
repo_root="${CODEX_PROJECT_DIR:-${CLAUDE_PROJECT_DIR:-$(pwd)}}"
rel_path="${file_path#"$repo_root"/}"

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
