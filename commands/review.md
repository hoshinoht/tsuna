---
description: Adversarially review the repository's most recent changes
agent: orchestrator
---

Run an adversarial review of everything that has recently changed in this repository.

Focus: $ARGUMENTS (if empty, review all current changes).

# What to review

- The current git changes: staged, unstaged tracked, and untracked files that are relevant source, config or docs.
- Begin with `git status --short --branch`, `git diff --stat`, `git diff` and `git diff --cached`.
- Read an untracked file only if it looks like relevant source, config or documentation. List generated, binary or unrelated artifacts separately without reading them in full.
- If there is nothing to review, say so and stop.

# How to split the work

1. For a small diff, delegate the whole review to a single `code-checker`.
2. For a large diff, or one that touches unrelated components, cut it into slices that do not overlap, by package, module or group of files.
3. Give each slice its own `code-checker`, and run them in parallel whenever their scopes are disjoint.
4. Put the reviewer brief below into every `code-checker` prompt, filled in with that slice's exact files and scope.
5. Once every checker has reported, merge the findings, drop duplicates and order them by severity.

# Constraints

- Do not modify files, commit, or create branches.
- Leave unrelated older code alone unless the diff cannot be understood without it.
- Prefer findings tied to a specific file and line over general advice.

# Reviewer brief

Paste this into each `code-checker` prompt and fill in the placeholders:

```markdown
You are reviewing changes written by another AI agent. Treat them with suspicion:
assume there are mistakes until you have checked otherwise.

## Context
- Repository root: <absolute path>
- Slice: <package / module / file group>
- Files in scope: <exact paths>
- Diff context: <summary of the change, or pointers to the relevant hunks>

## Task
Review only this slice. Hunt for problems the recent changes introduced:
correctness bugs, mismatches with the spec or intent, risky behaviour, missed
edge cases, security issues, broken or weakened tests, configuration mistakes,
and maintainability problems.

## Rules
- Do not modify any file.
- Stay inside the slice, except to follow a caller or callee far enough to judge the change.
- Every finding must be actionable and cite a path and line.
- Report only issues that meet your usual finding bar. If the slice holds up, say so plainly and list what you checked.

## Report
Use your standard receipt: `STATUS: PASS | FAIL | BLOCKED`, then findings
tagged with your usual severity (`blocker`, `critical`, `major`, `minor`,
`note`, `ask`), coverage (what you inspected and how), and what you could
not verify, with the commands the parent should run to check it. You can only
read files and run read-only `git status`/`git diff`, so do not claim to have
run tests or other commands.
```

# Final response

1. Overall verdict.
2. Blocking findings: `blocker`, `critical` and `major`.
3. Non-blocking findings: `minor`, `note` and `ask`.
4. Coverage: the slices reviewed and the subagents used for each.
5. Suggested verification commands (not yet run, unless you ran them yourself and say so).
