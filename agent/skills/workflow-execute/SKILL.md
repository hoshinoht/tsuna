---
name: workflow-execute
description: Execute an existing durable development workplan through scoped implementation and evidence-backed validation. Routine inline plans do not use this skill.
compatibility: Requires OMP (hoshi-omp) with this repository's agents and the mcp__workplan_* tools served by Shiori (vendor/shiori).
metadata:
  domain: software-engineering
  workflow: workplan-execution
---

# Executing a durable workplan

The orchestrator (the parent) runs this loop. It alone writes workplan state; workers return receipts.

## Before you start

- Read the plan from the actual project root: JSON state, linked Markdown and `specFiles`. Use `mcp__workplan_resume` or a scoped `mcp__workplan_inspect`; read the full plan only for history those leave out.
- Read [the artifact and evidence contract](../workflow-plan/references/workplan-contract.md). If it cannot be read, report that blocker; never guess field names or accept an unknown schema.
- Confirm readiness and the user's implementation authorization. An implementation request or `/dev` authorizes ordinary in-scope work; a plan-only request does not. Do not ask for approval again between ready steps.
- If a session starts with an `in_progress` workplan and no new instruction, report its resume point (next step, unmet dependencies, unresolved findings) and ask whether to continue. Never auto-execute a plan-only workplan.
- If there is no durable workplan, stop using this skill and work directly or from a short inline plan.

## The loop

1. Check structural validity (`mcp__workplan_validate`; see "Validation without the tool" below) and that the code is ready. Ask `plan-checker` only when the plan carries migration, security or data, public-contract, rollback or multi-owner risk and has not had proportionate review.
2. Pick the next executable step: its prerequisites, owned files, blocked or shared files, integration order and validation. Re-read the exact phase and step ids just before each update. Recheck references if the code changed.
3. Load `agent-use` and dispatch. Use `code-writer` for settled behaviour and ownership, `code-engineer` for slices needing bounded judgment, `frontend-engineer` for UI. Work directly on small steps or when one reasoning context is needed. Delegate in parallel only independent slices with disjoint ownership; serialize shared-file work. Claim steps as the `agent-use` reference describes.
4. Require the worker's self-checks and receipt. Inspect the diff, reconcile outputs, then update shared state once.
5. Use `tester` for extra specified checks, bug reproduction or high-volume evidence when that saves work. Reuse checks that are valid for the same code state. Testers report; implementers fix.
6. For medium, large or consequential changes, get a fresh `code-checker` review with the exact scope, acceptance criteria, diff and evidence. Track findings in the plan and send concrete corrections to the right worker.
7. Record code state, worker and returned `agent://` handle, attempts, changed files, acceptance evidence, validation results, findings and next step. Update the artifacts at phase changes, blockers, review handoffs and completion, not after every command.
8. Continue to the next ready step without asking. Stop when the completion gate passes or a concrete blocker needs a decision.
9. When the completion gate passes, mark all completed steps, phases and the workplan in one final update, record current evidence, and set the resume point to `none — complete`. Dispatch nothing further.
10. Anything found after completion is new scope. Ask the user before changing files, adding a phase, reopening the plan or running more loops.
11. Before the final update, review any `Lesson candidates`. Persist at most three durable, project-specific lessons, and only if that is part of the authorized workflow.

For parallel slices that would collide in one checkout, see the worktree lanes section of the `agent-use` reference. Lanes need explicit authorization for commits and local merges.

## State updates

- Keep one active workplan per task.
- `mcp__workplan_update` changes JSON state; `mcp__workplan_patch` changes localized Markdown prose. Omit unchanged optional fields. Never send empty placeholder strings or the full `planMarkdown` for routine updates.
- Without the tools, edit the same version-2 JSON and Markdown with OMP's live `edit` or `apply_patch` schema. Keep ids, markers and unrelated state. Follow the fresh-read rule in `AGENTS.md` with no other write between the read and the edit.
- Use the live session's canonical project root, not an alias that resolves outside it.
- Severities are `blocker`, `critical`, `major`, `minor`, `note`, `ask`. Map older labels: Critical to critical, High to major, Medium to minor, Low to note. Mark resolved findings explicitly.
- Structural `valid` does not mean the work is complete. Keep failed-attempt history and the current resume point across compaction.

## Tool rules

Each rule stands alone; apply the ones that match what you are doing.

- `mcp__workplan_doctor` is a read-only preflight for tool, artifact, host and permission problems. Permission facts it cannot see are unknown, not approved.
- Every native write to an existing plan needs the current `stateHash` passed as `expectedHash`. After a stale-state error, re-read and recompute the change.
- `mcp__workplan_resume` output is bounded. Follow its cursor or its read/inspect pointers for anything omitted.
- Reconfirm stale checkpoint guardrails and blockers. A fresh plan checkpoint does not make test evidence fresh.
- Compaction, in order: update, checkpoint, read-only preview, then apply with that exact `previewToken`, the current `expectedHash` and `confirmation: "ARCHIVE_SELECTED_HISTORY"`. Any change in between needs a new preview.
- Only the orchestrator may checkpoint, apply compaction or recover a transaction. Recovery uses `mcp__workplan_update` with `recovery: "resume" | "rollback"` and no normal update fields.
- A create interrupted before the plan file existed shows in `mcp__workplan_doctor` as a plan entry with `recoveryRequired: true` and a `stateHash`; recover it with `mcp__workplan_update` recovery, passing that `stateHash` as `expectedHash`. `mcp__workplan_read` still reports such a plan as not found.
- On an external-edit conflict, stop. Never delete locks or journals, and never bypass a pending transaction with fallback edits.
- Do not retry nonexistent or stale tools in a loop.

## Validation without the tool

If `mcp__workplan_validate` is absent, run the repository-local `scripts/check-workplan.ts <absolute-workspace-root> <workplan-id>` with Bun through OMP's `bash` tool after the planner returns. It is read-only and checks the schema and linked files. A nonzero exit blocks structural validity. If Bun or the helper is unavailable, say so and validate with native reads against the contract; never claim the command ran. Planner and reviewer agents have no `bash` tool and return their artifacts to the orchestrator for this check.

## Convergence and completion

- A cycle is implementation or fix, validation, and independent review. A failed fix leaves its finding unresolved or introduces a material defect.
- After two failed fixes, reassess the hypothesis yourself; consult `oracle` for contradictory evidence or exceptional uncertainty. Resume the same worker for a specific correction.
- Stop after three non-converging cycles and report the evidence and the decision needed. Never repeat an unchanged failing approach or widen scope to satisfy speculative review suggestions.
- Before marking the plan completed, check the original user outcome, current acceptance evidence, required validation, integration across slices, and that no blocker, critical or major finding is open. A reviewed significant change may pass with non-blocking notes. If required verification is unavailable, report it; never invent a passing check.
- `completed` is terminal for the current authorization. Do not reopen or extend the plan without an explicit user request.

Report the behaviour delivered, relevant files, checks and evidence, and remaining limitations. After changes to the global harness, say whether a new session or service restart is needed to load them; do not interrupt unrelated running work.
