---
name: staged-delivery
description: Deliver a multi-stage project safely with delegated implementers — brief, implement without committing, independent review, send back for changes, commit, bump the consumer's pin, test-gated commit, push — with owner decisions and intentional output changes recorded as dated, pinned design changes. Use for work split into reviewed stages (engines, plugins, migrations), especially when the result is already live somewhere.
license: GPL-3.0-or-later
metadata:
  domain: "software-engineering"
  workflow: "staged-delivery"
---

# Staged delivery

## Per stage

1. **Brief.** Write the stage scope with the owner's decisions already made:
   what changes, what must stay identical, the evidence required, and the
   validation commands. Save briefs for later stages somewhere durable
   (for example a gitignored `plans/` directory), not in temporary space.
2. **Implement without committing.** The implementer works in the working
   tree only; it never commits, pushes or restarts shared services, and it
   reports changes, test results and decisions for the owner.
3. **Review.** Check the report against the brief, spot-check the evidence
   yourself, and run the key checks fresh (tests with `-count=1`/no cache).
   Send it back with specific changes when something overcorrects or leaves a
   gap — continue the same implementer so its context is kept.
4. **Owner decisions.** Surface only real choices, each with options and a
   recommended default; apply the answers before committing.
5. **Commit and push** the stage with a message listing what changed.
   Record the commit id where the project tracks status.
6. **Update consumers.** Bump the pin (submodule, lockfile), rebuild
   artifacts, run the consumer's full test suite, and commit **only if it
   passed** — check the result, not just that the command ran.
7. **Tell the owner** what went live and whether a restart is needed.

## Design changes

- When a stage deliberately changes behaviour from a reference, record it as
  an approved design change with the date and the reason.
- Pin intentional output changes in a per-stage expectations file compared
  against the **unchanged** reference corpus, with a comparator proving that
  only the approved difference occurs. Everything else stays byte-identical.
- Keep a switch to turn a stage's changes off in tests, so earlier stages'
  checks keep passing.

## Commit hygiene

- Stage specific paths; never `git add -A` in a tree that may hold the
  owner's uncommitted edits.
- Gate commits on test results: parse the pass/fail counts and require zero
  failures before `git commit`.
- Don't chain dependent git steps with `&&` after commands that can warn and
  fail (for example `git add` with a pathspec that names an ignored path).
  Check each step's exit status, confirm `git show --stat HEAD` holds the
  intended files under the intended message, and only then record the commit
  id anywhere.
- Don't restart or replace things the owner is actively using without saying
  so; prefer changes that apply on their next restart, or verify live state
  immediately after a hot reload.

## Pausing

When the owner pauses, leave a resume point: stage status with commit ids,
what is live and how it is pinned, the remaining queue in order, open
decisions, and how to recreate any temporary test fixtures.
