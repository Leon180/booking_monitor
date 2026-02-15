---
description: Generate PR title and description based on current branch changes
---

# Create PR Title and Description Workflow

Generate a well-structured Pull Request title and description based on current branch changes.

## Steps

// turbo
1. Get the current branch name:
   ```bash
   git branch --show-current
   ```

// turbo
2. Get the list of commits on this branch (compared to main):
   ```bash
   git log main..HEAD --oneline
   ```

// turbo
3. Get all changed files compared to main:
   ```bash
   git diff main --name-only
   ```

// turbo
4. Get a summary of all changes:
   ```bash
   git diff main --stat
   ```

5. Review the actual changes to understand the purpose:
   ```bash
   git diff main
   ```

6. Generate PR title and description following this format:

## Output Format

```markdown
## PR Title
[type](<scope>): [concise description]

Types: feat, fix, refactor, docs, test, chore, perf
Scope (optional): service, controller, repository, model, config, etc.

Example: `feat(service): add user agent notification email functionality`

---

## PR Description

### Summary
[Brief description of what this PR does and why]

### Changes
- [Change 1]
- [Change 2]
- [Change 3]

### Related Issues
- Closes #[issue-number] (if applicable)

### Testing
- [ ] Unit tests added/updated
- [ ] Manual testing performed
- [ ] Integration tests pass

### Screenshots (if applicable)
[Add screenshots for UI changes]

### Checklist
- [ ] Code follows `.agent/skills/backend-guide-ddd` conventions
- [ ] Self-review completed
- [ ] Documentation updated (if needed)
```

## Notes

- Use conventional commit format for the title
- Keep the title under 72 characters
- Description should be comprehensive but concise
- Mention breaking changes if any
