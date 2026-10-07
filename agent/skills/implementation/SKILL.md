---
name: implementation
description: Working procedure for a delegated code change. Covers reading the code and brief first, when to check current documentation, making the smallest correct edit, verifying it, handling failed attempts, and the STATUS receipt. Load at the start of every implementation slice.
metadata:
  domain: software-engineering
  workflow: implementation
---

# Implementing a slice

Your agent prompt sets how much you may decide on your own and when you must hand a decision back. This skill covers everything else.

## 1. Read before writing

- Read the brief: goal, scope, acceptance and validation. Work from its workspace root or cwd.
- If the brief names a workplan, read only your phase or step with a scoped `mcp__workplan_inspect`. Read any linked `specFiles` before editing; they are constraints.
- Stay inside the files the brief grants. If ownership is missing or unclear in a way that changes what you edit, stop and report the gap. Otherwise state which files you assumed you own.
- If the brief contradicts the spec files, stop and report the conflict.
- Find the files, tests and config involved. Read the surrounding code and one nearby precedent. Survey more widely only if the target is genuinely unclear.
- Read the installed or declared versions from manifests and lockfiles.

## 2. Check current documentation when the code depends on something external

- Check when you add or change a call, import, hook, component, config key, CLI flag, environment variable, request or response shape, or test/build helper that belongs to a library, framework, SDK, service or tool, or when the local examples are missing, stale or too thin to rely on.
- Skip it when the repository fully determines the work: a local refactor, a rename, or a logic fix in internal code.
- Sources, in order: Context7 (resolve the library, then query the section you need); official docs, changelogs and migration guides through the web tools; the repository's own docs and DeepWiki; issues and discussions when the docs are silent.
- Match the docs to the installed version. Look for deprecations, migration notes and changed defaults.
- Never guess an API signature, a default or configuration syntax. If you cannot find current docs, say what you searched, fall back to repository evidence and flag the uncertainty.
- If you notice you wrote code that needed research you skipped, stop, do the research, then fix or re-validate.

## 3. Make the change

- Make the smallest correct change, in the style already there: naming, structure, typing, error handling, test layout.
- Add no speculative fallbacks, compatibility shims, one-off abstractions or configuration nobody asked for.
- Before writing new code, use the first of these that covers the need: a helper or pattern already in the repository, the standard library, a native platform feature, an already-installed dependency. Write custom code only when none fits; a new dependency is a parent decision.
- For a bug fix, find every caller of the function you change. Fix the shared function once rather than guarding only the path the report names. If that changes behaviour for other callers, it is a scope decision for the parent.
- A bug fix is not a cleanup. Leave unrelated code as it is, and list pre-existing problems you notice in the receipt instead of fixing them.
- Other agents may be editing neighbouring files at the same time, so an edit outside your scope can collide with theirs. Report needed out-of-scope changes to the parent.
- Run the repository's formatter or autofix command instead of formatting by hand.
- Comments in code you write explain why: intent, a constraint that is not obvious, a failure mode. Do not add comments that restate the code, and leave no commented-out code behind.

## 4. Verify

- Pick the smallest check that would catch a wrong implementation of this change. A non-behavioural edit needs diagnostics or a type check. A behaviour change needs a targeted test plus one real run of the entry point.
- For a reproduced bug, add a focused regression test when practical. Test observable behaviour, not a mirror of the implementation. Use property-based tests only when real invariants call for them, and never add a framework just for that.
- Never make a check pass by weakening or deleting a test, hard-coding the expected value, or suppressing a type or lint error. If a test looks wrong, say so in the receipt.
- If a check cannot run, say why and do the best static cross-check you can.
- Do not re-run a check that already passed on the same code without a new reason.

## 5. When an attempt fails

- Fix within your scope and re-run.
- Before the next attempt, change something material: the hypothesis, the approach or the API used. Never resend the same fix.
- When you reach the failure limit in your agent prompt, stop. Restore the files you changed to how you found them (never touch changes you did not make), unless the brief asks you to leave them for inspection. Return FAIL or BLOCKED listing each attempt: hypothesis, change, result, and the next check that would tell the explanations apart.

## 6. Report

End with the receipt from `agent-use`, outcome first:

```text
STATUS: PASS | FAIL | BLOCKED
Changed: files touched and the behaviour delivered
Acceptance: criterion -> result or unmet
Verified: cwd, command, exit status, test counts, evidence paths
Not verified: what could not be checked, and why
Attempt: hypotheses and results, if anything failed
Pre-existing issues: problems noticed and left alone, or none
Decision required: exact question or conflict, or none
```

Mention the docs you consulted only where they bear on the result. PASS covers this slice only; the parent owns final acceptance.

Stop when the slice is implemented and checked, or when a decision outside your latitude blocks it. Do not pick up adjacent work, spawn agents or edit shared workplan state; send state changes back to the parent.
