# Third-Party Attributions

This directory contains assets adapted from upstream projects. All are MIT-licensed and remain so.

## [affaan-m/everything-claude-code](https://github.com/affaan-m/everything-claude-code)

- **License**: MIT
- **Upstream commit pinned**: [`125d5e619905d97b519a887d5bc7332dcc448a52`](https://github.com/affaan-m/everything-claude-code/tree/125d5e619905d97b519a887d5bc7332dcc448a52)
- **Fetched**: 2026-04-11

### Files adopted

| Local path | Upstream path |
|------------|---------------|
| `.claude/agents/go-reviewer.md` | `agents/go-reviewer.md` |
| `.claude/agents/go-build-resolver.md` | `agents/go-build-resolver.md` |
| `.claude/agents/silent-failure-hunter.md` | `agents/silent-failure-hunter.md` |
| `.claude/skills/golang-patterns/SKILL.md` | `skills/golang-patterns/SKILL.md` |
| `.claude/skills/golang-testing/SKILL.md` | `skills/golang-testing/SKILL.md` |
| `.claude/skills/postgres-patterns/SKILL.md` | `skills/postgres-patterns/SKILL.md` |
| `.claude/skills/tdd-workflow/SKILL.md` | `skills/tdd-workflow/SKILL.md` |
| `.claude/rules/golang/coding-style.md` | `rules/golang/coding-style.md` |
| `.claude/rules/golang/hooks.md` | `rules/golang/hooks.md` |
| `.claude/rules/golang/patterns.md` | `rules/golang/patterns.md` |
| `.claude/rules/golang/security.md` | `rules/golang/security.md` |
| `.claude/rules/golang/testing.md` | `rules/golang/testing.md` |

Files are kept verbatim where possible. If any local modifications are made, they should be noted below with a short rationale so upstream-sync remains traceable.

### Local modifications

_(none yet)_

### Refresh procedure

To pull newer versions from upstream:

```bash
SHA=$(gh api repos/affaan-m/everything-claude-code/branches/main | jq -r .commit.sha)
for path in \
  agents/go-reviewer.md \
  agents/go-build-resolver.md \
  agents/silent-failure-hunter.md \
  skills/golang-patterns/SKILL.md \
  skills/golang-testing/SKILL.md \
  skills/postgres-patterns/SKILL.md \
  skills/tdd-workflow/SKILL.md \
  rules/golang/coding-style.md \
  rules/golang/hooks.md \
  rules/golang/patterns.md \
  rules/golang/security.md \
  rules/golang/testing.md; do
  gh api "repos/affaan-m/everything-claude-code/contents/${path}?ref=${SHA}" \
    | jq -r .content | base64 -d > ".claude/${path}"
done
```

After refreshing, review the diff, update the pinned SHA above, and commit.

## MIT License Notice

Everything Claude Code is released under the MIT License. The full license text
is available at <https://github.com/affaan-m/everything-claude-code/blob/main/LICENSE>.
Redistribution under MIT requires only that the original copyright notice and
this permission notice be preserved.
