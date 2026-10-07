# Workplan artifact and evidence contract

Use one stable id and one pair of files per non-trivial task. Preserve the existing version-2 schema; store richer task contracts and receipts in Markdown, with concise pointers and decisions in JSON notes. Do not invent new tool arguments or silently upgrade old metadata.

A minimal version-2 JSON document has this shape (substitute actual values, paths and timestamps):

```json
{
  "schemaVersion": 2,
  "id": "feature-name",
  "kind": "software-engineering",
  "title": "Feature name",
  "goal": "Observable user outcome",
  "scope": ["Included behavior"],
  "nonGoals": ["Excluded behavior"],
  "constraints": [],
  "relevantFiles": ["src/example.ts"],
  "planFile": ".opencode/workplan/feature-name.md",
  "specFiles": [],
  "phases": [{
    "id": "implementation",
    "title": "Implementation",
    "status": "draft",
    "steps": [{
      "id": "feature",
      "title": "Implement the feature",
      "target": "src/example.ts",
      "action": "Concrete bounded change",
      "validation": "Exact check and expected result",
      "status": "draft"
    }]
  }],
  "reviewFindings": [],
  "notes": [],
  "status": "draft",
  "createdAt": "<current ISO timestamp>",
  "updatedAt": "<current ISO timestamp>"
}
```

Allowed statuses: `draft`, `in_progress`, `blocked`, `review`, `completed`, `cancelled`. Findings use `severity`, `title`, optional `detail`, `source`, and `status: open|resolved`; severities are `blocker`, `critical`, `major`, `minor`, `note`, `ask`.

Use this Markdown structure, scaled to the work:

```markdown
# Goal
## Scope and non-goals
## Authorization, decisions and assumptions
## Approach and verified references
## Work packages
### <phase>/<step>
- Objective:
- Owned files / blocked files:
- Dependencies and integration order:
- Acceptance criteria (observable behavior):
- Validation (command/interaction + expected result):
- Escalation trigger:
## Validation and review strategy
## Risks and open questions
## Execution receipts
### <phase>/<step> — attempt <n>
- Worker agent / `agent://` handle (if returned):
- Code state (commit plus working-diff description or artifact fingerprint):
- Changed files:
- Acceptance criterion -> evidence:
- Checks: cwd, command, exit status, relevant counts, evidence path:
- Hypothesis / result / next decision:
- Review findings and disposition:
## Resume point
- Next executable step and unmet dependencies:
- Unresolved findings / decision required:
```

Generated phase/step markers (`<!-- workplan-phase-id: ... -->`, `<!-- workplan-step-id: ... -->`) must be preserved when present. A new handwritten plan may include them beside the matching headings for tool interoperability.

The parent alone records execution receipts and JSON updates after integrating worker results. Retain failed attempts as well as the final result. Evidence paths should point to actual logs/artifacts, not promised future output. Avoid storing full transcripts. Note which code state was tested so changed code cannot reuse stale evidence.

## Three distinct gates

1. **Structural validity:** valid JSON/version/statuses, unique phase IDs and step IDs within each phase, nonempty goal/actions/validation, existing nonempty linked plan/spec files within the project. Validate with the tool when available, otherwise check with native reads. This does not run tests.
2. **Readiness:** requirements and ownership are clear enough to execute, dependencies are achievable, references are accurate, acceptance is observable, and material plan findings are resolved. Record the separate authorization decision; readiness alone is not permission.
3. **Completion:** every in-scope step/phase is completed, acceptance criteria have current supporting evidence, required checks actually ran, no unresolved blocker/critical/major findings remain, and significant changes have independent review (or an explicitly disclosed, accepted limitation). A zero exit code with no relevant tests, a worker's PASS, or a structurally valid plan alone is insufficient.

If checks cannot run, record the limitation and return BLOCKED/unverified as appropriate; do not silently treat missing evidence as passing. Update both artifacts using small edits (under the shared fresh-read rule in the global `AGENTS.md`) before handing off or after meaningful progress. When resuming, read actual files instead of reconstructing state from conversation memory.

When `mcp__workplan_validate` is unavailable, the parent orchestrator can run the repository-local `scripts/check-workplan.ts <absolute-workspace-root> <workplan-id>` with Bun. This helper is read-only and does not infer readiness or completion from a status field. Planning/review agents have `bash` disabled, so they return the workplan id/paths for the parent to run this check; file creation itself stays with the planner.

## Native mutation and continuation contract

- Core metadata stays at `schemaVersion: 2`. Checkpoints, dependency metadata and recovery journals are separately versioned sidecar files. Reads never migrate old artifacts.
- A native write to existing state needs the current `stateHash` as `expectedHash`, taken from read, inspect, resume or a successful write. A stale hash means re-read and recompute. Hashes and tokens bind state; they are not authorization.
- Tool writers serialize workspace linkage and per-plan changes. Each file is published atomically. A multi-file failure leaves explicit recovery state.
- Read-only tools never recover, create locks or repair files.
- Only the orchestrator may run recovery, through `mcp__workplan_update` recovery mode and separate from normal update fields. Recovery refuses targets edited outside the tools rather than overwriting them. For an interrupted create, take `expectedHash` from the `stateHash` that `mcp__workplan_doctor` reports for the `recoveryRequired` entry.
- The planner and orchestrator may author plans. Only the orchestrator may checkpoint or apply compaction.
- Native permissions must cover every exact path a mutation touches and the live invocation. An unknown host, denied approval or cancellation fails closed. Never bypass a pending transaction or a failed permission check with raw edits.
- Compaction order: update, checkpoint, read-only preview, apply. Apply needs the unchanged `previewToken`, the current `expectedHash`, a fresh checkpoint covering all artifacts, and `confirmation: "ARCHIVE_SELECTED_HISTORY"`. Changing the state or the selection needs a new preview. Complete originals are archived before anything is pruned.
- Checkpoint freshness covers the JSON, linked Markdown and specs, not the tested code.
- Legacy or stale guidance and guardrails count as unverified and need reconfirmation. Their absence or staleness never permits relaxing an existing restriction.
- Resume output is bounded and paginated. Respect its omission and danger counts, and fetch referenced work through the exact read or inspect pointers or the returned cursor before declaring readiness or completion.
- `mcp__workplan_doctor` only diagnoses. Permission facts it cannot see stay unknown.
