---
name: git-commit
description: Prepare Git commits that follow the repository's own message convention (Conventional Commits by default), split work into small atomic commits, stage only what belongs, and warn about committing to a shared main branch without overriding what the user explicitly wants. Use when asked to create, prepare, split, review or word a commit or commit message.
metadata:
  domain: "git"
  workflow: "commits"
---

# Git commits

Use this skill when the user asks you to make, prepare, review or phrase a Git
commit.

## Ground rules

1. Commit only when the user has explicitly asked for it.
2. Before drafting a message, read the recent history (`git log --oneline -n 30`, plus `git log -n 5` to see whether bodies are used). The repository's established style takes precedence over everything below.
3. Without a clear house style, use Conventional Commits. Types: `feat`, `fix`, `chore`, `docs`, `refactor`, `test`, `build`, `ci`, `perf`, `style`. Add a scope where one fits, such as `feat(parser)`, `fix(cli)` or `chore(deps)`.
4. The title is one short line that says what changed.
5. Follow the title with a brief body, unless the repository's convention clearly avoids bodies.
6. Keep commits as small as is sensible, and never bundle unrelated changes together.

## Branches

1. Do not create a branch unless the user tells you to.
2. If you are about to commit straight to `main` or `master` and the branch looks shared, point that out to the user.
3. If the user still wants the commit there, make it. Do not refuse.

## Splitting work into commits

- Group changes by behaviour, by module and by what could be reverted on its own. A good commit has exactly one reason someone might revert it.
- Rule of thumb: if three or more files change for different reasons, that is at least two commits. One behaviour spread across many files is still one commit.
- Let the last 30 or so commits show you the expected types, scopes and body habits. What the history does beats these defaults.
- These grouping rules sit on top of the branch rules and never override them: no unrequested branches, a warning before committing to a shared main, and the user's explicit choice always wins.

## Staging

- Before committing, look at `git status`, `git diff`, `git diff --cached` and the recent log.
- Stage only the files that belong to the commit you are making (`git add <path>`, or `git add -p` for part of a file). Never stage anything that looks like a secret, credentials, or a local-only artifact.
- Leave unrelated changes in the working tree alone, and tell the user they are there.

## Message shape

```
<type>(<scope>): <short description>

<One short sentence explaining why this commit exists.>
```

Examples:

```
feat(export): add CSV download to the invoice list

Finance asked to reconcile invoices in a spreadsheet without copying rows by hand.
```

```
fix(scheduler): skip jobs whose start time is already in the past

Restarting the service replayed every missed job at once and flooded the queue.
```
