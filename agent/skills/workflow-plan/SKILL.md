---
name: workflow-plan
description: Create or revise a durable development plan for cross-session, multi-owner, migration, staged-rollout, or consequential work. Ordinary planning stays inline; planning never authorizes implementation by itself.
compatibility: Requires OMP (hoshi-omp) with this repository's agents and the mcp__workplan_* tools served by Shiori (vendor/shiori).
metadata:
  domain: software-engineering
  workflow: workplan-planning
---

# Planning procedure

1. Confirm a durable plan is warranted: the task must survive across sessions, has several dependent write owners, is a migration or staged rollout, carries consequential architecture, security or data-loss risk, or the user asked for one. Otherwise return a concise inline plan and create no files.
2. Read the goal, project instructions, existing changes and the smallest relevant set of code and tests. Separate discoverable facts from decisions the user must make; follow repository precedent for reversible details.
3. Create one stable plan id. Define coherent work packages, each with ownership, dependencies, observable acceptance, a validation step (command or interaction plus expected result) chosen from how it could fail, and a finite end. Do not split work just to create phases or parallel lanes.
4. Ask `plan-checker` only when independent review is likely to reduce consequential risk: migrations, security or data changes, public-contract changes, hard rollback, several interacting owners. One pass is the default; re-review only after a material correction, and stop after two non-converging passes.
5. Return an execution-ready handoff or the exact blocker. Preserve existing implementation authorization and do not start implementation workers. Add `Lesson candidates` only when a durable project-specific lesson emerged.

# Storage

- `.opencode/workplan/<id>.json` holds machine state; `.opencode/workplan/<id>.md` holds reasoning, ownership, acceptance and evidence. Optional specs live under `.opencode/docs/specs/` and are linked through `specFiles`.
- Read [the artifact contract](references/workplan-contract.md) when creating, resuming or validating a durable plan.
- Discover plans with `mcp__workplan_list`, `mcp__workplan_read` and `mcp__workplan_inspect`.

# Writing with the tools

Use a tool only if it is actually registered in this session; this skill naming it is not proof.

- `mcp__workplan_create` makes a new plan. Creation requires that the plan does not already exist.
- `mcp__workplan_update` changes JSON state: goal, scope, constraints, files, phases and steps, findings, notes, status. Prefer the targeted `updatePhases`, `updateSteps`, `addPhases` and `addSteps`.
- `mcp__workplan_patch` changes localized Markdown prose. Never use Markdown to change machine state, and do not replace the whole plan on every update.
- Omit unchanged optional values. Never send blank strings or placeholder arrays to clear data. Do not retry a stale-schema error unchanged.
- `mcp__workplan_reset` is only for a requested restart. Never reset a valid plan on resume.
- `mcp__workplan_validate` checks structure and linked files. `valid` does not prove the plan is executable, authorized or complete.
- `mcp__workplan_doctor` (read-only) diagnoses missing tools, invalid artifacts, pending transactions and permission conflicts.

Rules for changing an existing plan:

- Pass the current `stateHash` as `expectedHash`, taken from read, inspect, resume or the last successful write. On a stale hash, re-read and recompute; never rebase an old patch automatically.
- Handwritten Markdown is preserved. Replacing it fully needs explicit `replaceMarkdown`. When a move finds the source Markdown missing, supply nonblank `planMarkdown` or stop; do not let it regenerate.
- As the planner you do not write checkpoints, apply compaction or recover transactions. `mcp__workplan_compact_preview` (read-only) is allowed.
- If a transaction is pending, return its diagnostic to the orchestrator. Do not work around it with fallback edits or delete lock or journal files.

# Writing without the tools

When the tools are absent, write the same files with OMP's native read and edit tools inside the planner's allowed paths, with no shell and no plugin installation. Follow the live `edit` or `apply_patch` schema; hashline anchors from `read` are required when that edit mode is active.

1. Read `references/workplan-contract.md` relative to this skill's directory first. If it cannot be read, return BLOCKED with the error; never infer a schema from logs, old conversations or another project.
2. Use numeric `schemaVersion: 2` and the documented field types and statuses. Keep existing ids and fields.
3. Follow the fresh-read rule in `AGENTS.md`, with no other write between the read and the edit. Keep JSON and Markdown edits separate, one small logical change at a time.
4. Use the live session's canonical project directory for paths (on macOS `/tmp` may resolve to `/private/tmp`).
5. Re-read both files and compare them with the contract before claiming structural validity.

If both the tools and the native fallback are unavailable or denied, return BLOCKED with the exact tool error. Never claim READY with pasted content for the parent to write. During execution the parent is the only writer.

# Handoff

Return `STATUS: READY | BLOCKED` with the workspace root, workplan id and paths, scope, decisions, work packages and ownership, acceptance and validation, remaining material findings, and the next executable step. READY requires both artifacts on disk. Include review coverage and `Lesson candidates` only when present. Preserve whether implementation was already requested; do not ask for another approval.
