---
name: simplify-review
description: Review a diff or named files for over-engineering only — reinvented standard library, dependencies doing what the platform already does, speculative abstractions, dead flexibility, longer-than-needed logic. One line per finding naming what to cut and what replaces it. Report only; never applies fixes. Use when asked to review for over-engineering, bloat or simplification, "what can we delete", or "is this over-engineered". Complements correctness review; does not replace it.
license: MIT
metadata:
  domain: software-engineering
  source: "Adapted from ponytail-review in DietrichGebert/ponytail (MIT, Copyright (c) 2026 DietrichGebert)"
---

# Simplification review

Review the assigned scope for unnecessary complexity. The best outcome for the diff is getting shorter. Report only; do not edit.

## Scope

- Default to the change under review, not older code. Flag older code only when the change copies or extends it.
- Over-engineering only. Correctness bugs, security holes and performance belong to a normal review; if you notice one, list it once under `Out of scope` and move on.
- Never flag as bloat: validation at trust boundaries, error handling that prevents data loss, security measures, accessibility, anything the brief or spec explicitly requires, or a test that guards real logic.
- Check before claiming a replacement exists: the helper is really in the repository, the stdlib function exists in the project's language version, the dependency is really installed (read the manifest).

## Finding format

`<file>:L<line>[-<line>]: <tag> <what>. <replacement>.`

| Tag | Meaning | Replacement |
| --- | --- | --- |
| `delete:` | dead code, unused flexibility, speculative feature | nothing |
| `reuse:` | re-implements a helper or pattern already in the repository | name the existing one and its path |
| `stdlib:` | hand-rolls what the standard library ships | name the function |
| `native:` | code or a dependency doing what the platform already does | name the feature |
| `yagni:` | abstraction with one implementation, config nobody sets, layer with one caller | inline it, until a second user exists |
| `shrink:` | same logic in fewer lines | show the shorter form |

Examples:

- `src/validate.py:L12-38: stdlib: 27-line email validator class. "@" in email; real validation is the confirmation mail.`
- `web/date.ts:L4: native: moment.js imported for one format call. Intl.DateTimeFormat, no dependency.`
- `repo.py:L88: yagni: AbstractRepository with one implementation. Inline it until a second one exists.`
- `lib/fetch.ts:L52-71: delete: retry wrapper around an idempotent local call. Nothing.`
- `util.py:L30-44: shrink: manual loop builds a dict. dict(zip(keys, values)).`

Not: "This class might be more complex than necessary; have you considered whether all these rules are needed?"

## Report

Findings ordered by lines saved, largest first, then `net: -<N> lines possible.` If nothing qualifies, say `Lean already.` and list what you inspected.

As a delegated agent, wrap this in your usual receipt. Simplification findings are non-blocking: use severity `minor` or `note`, and they alone never make the status FAIL.
