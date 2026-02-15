---
description: Generate a conventional commit message for staged changes
---

# Create Commit Message Workflow

Generate a well-structured commit message following conventional commit format.

## Steps

// turbo
1. Get the list of staged files:
   ```bash
   git diff --cached --name-only
   ```

// turbo
2. Get a summary of staged changes:
   ```bash
   git diff --cached --stat
   ```

3. Review the actual staged changes:
   ```bash
   git diff --cached
   ```

4. Generate commit message following conventional commit format.

## Commit Message Format

```
<type>(<scope>): <subject>

<body>

<footer>
```

### Types
- `feat`: A new feature
- `fix`: A bug fix
- `refactor`: Code change that neither fixes a bug nor adds a feature
- `docs`: Documentation only changes
- `test`: Adding missing tests or correcting existing tests
- `chore`: Changes to build process or auxiliary tools
- `perf`: A code change that improves performance
- `style`: Changes that do not affect the meaning of the code

### Scope (optional)
The scope should be the name of the component/module affected:
- `service`, `controller`, `repository`, `model`, `config`, etc.

### Subject
- Use imperative mood: "add" not "added" or "adds"
- Don't capitalize first letter
- No period at the end
- Max 50 characters

### Body (optional)
- Explain the motivation for the change
- Use when the subject alone is not enough
- Wrap at 72 characters

### Footer (optional)
- Reference issues: `Closes #123`, `Fixes #456`
- Note breaking changes: `BREAKING CHANGE: description`

## Output Examples

### Simple commit
```
feat(service): add email notification for user agents
```

### With body
```
fix(repository): resolve duplicate key constraint in approval procedure

The previous implementation did not check for existing records before
insert, causing unique constraint violations when updating procedures.

Closes #234
```

### Breaking change
```
refactor(api): change approval procedure endpoint to use path params

BREAKING CHANGE: The PUT endpoint now uses /approval-procedures/:docId
instead of query parameters. Clients need to update their API calls.
```

## czg Format

When using czg, provide the body in single-line format with `|` as line break delimiter:

### czg Output Template
```
Type: [type]
Scope: [scope or empty]
Subject: [subject]
Body (czg format): [line1]|[line2]|[line3]
```

### czg Example
```
Type: fix
Scope: repository
Subject: resolve duplicate key constraint in approval procedure
Body (czg format): The previous implementation did not check for existing records before insert.|This caused unique constraint violations when updating procedures.|Closes #234
```
