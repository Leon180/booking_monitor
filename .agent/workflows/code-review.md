---
description: Perform a thorough code review on staged or recent changes
---

# Code Review Workflow

Perform a comprehensive code review on the current changes.

> **Prerequisites**: Before reviewing, read `.agent/skills/backend-guide-ddd/SKILL.md` to understand project conventions.

## Steps

// turbo
1. Get the list of changed files:
   ```bash
   git diff --name-only HEAD
   ```
   If no changes, check staged files:
   ```bash
   git diff --cached --name-only
   ```

2. For each changed file, review the diff:
   ```bash
   git diff HEAD -- <file>
   ```
   Or for staged changes:
   ```bash
   git diff --cached -- <file>
   ```

3. Analyze each change for:
   - **Correctness**: Does the code do what it's supposed to do? Are edge cases handled?
   - **Architecture**: Does it follow the 4-layer DDD style? (`.agent/skills/backend-guide-ddd` §2)
   - **Transactions & DB Access**: Is `transaction.WithTx`/`GetTx`/`GetDB` used correctly? (`.agent/skills/backend-guide-ddd` §3)
   - **Error Handling**: Are errors handled per the guide? (`.agent/skills/backend-guide-ddd` §1)
   - **Logging**: Is `o11y.GetLogger(ctx)` used for logging? (`.agent/skills/backend-guide-ddd` §1)
   - **Style, Naming & Types**: Does code follow conventions? (`.agent/skills/backend-guide-ddd` §1)
   - **Security**: Are there any security concerns (SQL injection, XSS, etc.)?
   - **Performance**: Are there any obvious performance issues?
   - **Tests**: Are there adequate tests for the changes?
   - **Documentation**: Are complex parts documented?

4. Provide a summary with:
   - ✅ **Approved items**: Things that look good
   - ⚠️ **Suggestions**: Non-blocking improvements
   - ❌ **Issues**: Problems that should be fixed before merging

## Output Format

```markdown
# Code Review Summary

## Files Reviewed
- `file1.go`
- `file2.go`

## ✅ Approved
- [List of things that look good]

## ⚠️ Suggestions
- [Non-blocking improvements]

## ❌ Issues to Fix
- [Problems that need to be addressed]
```
