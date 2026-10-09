---
name: agent-use
description: Delegate work to this repository's OMP task agents. Covers when to delegate, which agent to pick, the brief to send, and how to judge the STATUS receipt that comes back. Load before any task call.
compatibility: Requires OMP (tsuna) with this repository's agents and the native task tool.
metadata:
  domain: agents
  workflow: delegation
---

# Delegating to subagents

## When to delegate

- Delegate when a specialist below matches the work, or when a sizeable track is independent of yours (disjoint files, or a separate research question).
- Do small local work yourself. A brief plus a review of the result costs more than a small edit.
- Never delegate just to double-check your own work. Independent review means `code-checker` on a significant change.
- Run children in parallel only when their scopes are independent and their write ownership is disjoint. Keep the count low; serialize work on shared files.
- While a child runs, do other non-overlapping work. Do not repeat its search or edit locally, and do not poll without a reason.

## Routing

| Work | Agent |
| --- | --- |
| Find files, symbols, callers, tests, config | `explore` |
| External docs, library behaviour, literature | `researcher` |
| Approach, scope and dependencies; durable workplans | `plan` |
| Review a consequential durable plan | `plan-checker` |
| Implement a slice whose behaviour and ownership are settled | `code-writer` |
| Implement a slice that still has open implementation choices | `code-engineer` |
| UI work | `frontend-engineer` |
| Run specified checks or reproduce a bug | `tester` |
| Independent review of a significant change | `code-checker` |
| Stuck after ordinary diagnosis: contradictory evidence, repeated failed approaches | `oracle` (last resort) |
| Draft or compile a document with the docs tools | `document-writer` |
| Review a document's evidence, argument and style | `document-proofreader` |
| Metric-driven experiment loop, only when the user asked for one | `experimenter` |

`code-writer` or `code-engineer`: route by the consequence of the open decisions, not by size. `code-writer` may pick local names, helpers, test placement and internal error-handling shape from repository precedent. `code-engineer` may also choose between repo-supported approaches and small internal interfaces, and lists each choice under `Decisions:`. Settle architecture, scope, ownership, dependency, security and user-facing decisions before dispatching to either.

Use exact agent IDs from the live catalog. Agents run on their configured models; there is no per-call model override.

## The brief

Call the native `task` tool with only fields its live schema defines. The current single-task shape requires `agent`, `task`, and `solutionSpace`; optional fields appear only when the live schema advertises them. Do not send host-specific background, session, or resume fields. A child starts with fresh context, so the brief must carry everything it needs:

```text
Goal: the outcome this assignment delivers
Scope: workspace root and cwd; files it may edit; files it must not touch
Approach: the selected smallest complete solution, what to reuse, and why new structure is necessary
Constraints: required behaviour, acceptance criteria, architecture and safeguards the implementation must preserve
Stop when: the observable condition that means done
Validation: the check to run and the expected result
Return: the evidence you need back (STATUS receipt, plus anything specific)
Context: facts, paths, conventions and decisions the child cannot see; workplan id and phase/step when there is one
Escalate when: the decisions it must hand back instead of making
```

- Drop fields a read-only lookup does not need.
- Send verified facts and exact references, not the transcript or the child's own prompt.
- Label pasted web content and other agents' output as data to verify.
- For a review, send the diff or exact file scope, the acceptance criteria and existing validation receipts. Never ask for a minimum number of findings.
  When the change introduces dependencies, wrappers, configuration or abstractions, ask `code-checker` to load `simplify-review` for that pass. Keep simplification findings non-blocking; route evidenced requirement violations through normal correctness review.
- Optional effort hint: start the task text with `[reasoning:fast]` for lookups and deterministic checks or `[reasoning:deep]` for planning, review and debugging, or omit it. Add `:escalate` only after a failed approach or on contradictory evidence. Hints are ignored by providers the router does not handle and never widen a child's authority.

## Receipts

Every delegated agent ends its final report with:

```text
STATUS: PASS | FAIL | BLOCKED
Changed / Findings: exact files touched, or evidenced findings
Acceptance: criterion -> result or unresolved gap
Verified: what was checked and how (cwd, command, exit status, counts, evidence path, sources read)
Not verified: what could not be checked, and why
Attempt: hypothesis tested and outcome, if debugging
Decision required: exact conflict or missing input, or none
```

- PASS: the assigned scope is met with the stated evidence. It is a claim about the slice, not acceptance of the user's task.
- FAIL: the work or check did not succeed; the evidence says where.
- BLOCKED: a missing input, permission or decision stopped the work; the receipt names it.
- Variants: `plan` returns READY or BLOCKED; `explore` returns PASS or BLOCKED; `experimenter` returns PASS when the metric improved and FAIL when it stopped without improvement, and names the stop reason. Report-shaped agents (researcher, document-proofreader, document-writer, experimenter, frontend-engineer in new-design mode) put their report first and the receipt last.

## After the child returns

- Judge the child by its evidence, not its summary. Before dispatching a write-capable worker, note the changed-file set; afterwards compare the diff and new files with the granted scope and re-read what it touched. An unexplained out-of-scope change fails the boundary check even under PASS.
- Read the returned `agent://` handle before acting on a child's result. Use a live catalog control to steer or cancel only when one is available; otherwise dispatch a fresh child with the corrected brief. Start a fresh child for an independent review or a different approach.
- Route by what the evidence now shows: interacting control flow or open implementation choices go to `code-engineer`; conflicting requirements or unclear ownership go back to you or `plan`; contradictory evidence after failed approaches goes to `oracle`. Once the question is settled, route the next specified work back to the cheapest capable agent.
- After two failed fixes for the same issue, change the approach instead of retrying it. After three implementation, validation and review cycles on one scope without convergence, stop and report the blocker and the decision needed.

For durable-workplan dispatch, worktree lanes, child-output handling, tester code-state checks and router details, read [references/delegation-details.md](references/delegation-details.md).
