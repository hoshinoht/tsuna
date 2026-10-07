# 01 — Core architecture and storage

Status: proposed specification, 2026-09-30. Implementation is not started.

## 1. Objective and boundaries

Shiori owns durable coordination data, not the authority to perform arbitrary
development work. It must make plans readable, changes conflict-aware,
interrupted mutations recoverable, and continuation bounded and honest.

Initial scope:

- Go core and standalone CLI; no Bun requirement for the core.
- Existing V2 plan JSON and linked Markdown, without automatic migration.
- Lifecycle, checkpoint, dependency, compaction, resume and doctor behavior.
- A versioned local protocol for the OpenCode adapter.
- Derived indexes and performance measurements before alternative storage.

Non-goals for the first release: running a plan's prose as shell commands,
model/provider integration in Go, remote worker provisioning, a web UI,
automatic Git commits/pushes, or replacing OpenCode's permission engine.
Worktree orchestration is separately staged in [04](04-worktrees.md).

Normative terms: **must** is an acceptance requirement; **should** is a default
that an evidenced design decision may amend; **candidate** is not committed
architecture. Any future amendment must preserve the safety gates below.

## 2. Architecture

```text
Human CLI                         OpenCode JS/TS adapter
   |                               | registration, trusted identity
   | OS-authorized local mode      | exact-path permission broker
   +---------------+---------------+
                   | versioned local protocol
                Go engine
                   |
      schemas -> snapshots -> indexes -> operations
                   |
       authorization -> locks -> journal -> publication
                   |
          existing JSON / Markdown / sidecars
```

The Go engine should be transport-independent. Proposed modules, not files
created by this specification:

| Module | Responsibility |
| --- | --- |
| `model` / `schema` | Compatible decoding, input validation, structural gates |
| `snapshot` | Exact artifact bytes, manifests, stable hashes |
| `index` | Phase/step lookup, dependencies and section indexes |
| `storage` | Path policy, locks, staging, journals and recovery |
| `engine` | Lifecycle operations and bounded projections |
| `protocol` | Framing, capabilities, cancellation and prepared mutations |
| `cli` | Explicit local operations, machine-readable errors and diagnostics |
| `lanes` | Later worktree inventory and integration state |

CLI human formatting must not change machine protocol semantics. No module
may infer authorization from a checkpoint, status, hash, worker PASS or prose.

## 3. Compatibility contract

Preserve the current known document fields:

| Area | Fields |
| --- | --- |
| Identity | `schemaVersion: 2`, `id`, `kind`, nullable `title`, `goal` |
| Scope | `scope`, `nonGoals`, `constraints`, `relevantFiles` string arrays |
| Links | `planFile`, optional legacy `specFiles` |
| Work | ordered `phases`, each with ordered `steps` |
| Findings | `severity`, `title`, optional `detail`, `source`, `status` |
| State | `notes`, `status`, `createdAt`, `updatedAt` |

Statuses: `draft`, `in_progress`, `blocked`, `review`, `completed`, `cancelled`.
Finding severities: `blocker`, `critical`, `major`, `minor`, `note`, `question`.
Finding status may be absent in old plans; absent does not mean resolved.

Phase IDs are unique within a plan; step IDs are unique **within a phase**.
Use a composite `(phaseId, stepId)` key, not a globally unique step assumption.
Preserve array order, stable IDs, unknown document/phase/step/finding metadata
and omission of valid optional legacy fields on unrelated mutations.

Compatible decoding and executable validation are distinct:

- Incomplete drafts remain readable; wrong known types/versions are diagnosed.
- Executable structure requires nonempty goal, linked artifacts, phase/step
  identities, actions and validation descriptions, and unambiguous IDs.
- Readiness additionally requires clear ownership, dependencies and decisions.
- Completion requires current acceptance evidence, not structural validity.

Errors must include exact field paths. Missing or malformed sidecars must be
diagnosed individually; they must not hide current plan state or trigger repair.
Reads must not write defaults, upgrade formats or change file mtimes.

## 4. Files and artifact classification

Retain the coordination root:

```text
.opencode/workplan/
  <id>.json
  <id>.md                         # default; explicit linked paths supported
  <id>.checkpoint.json            # separately versioned, new baseline v2
  <id>.dependencies.json          # baseline v1
  <id>.transaction.json           # recovery journal, baseline v1
  .workspace-mutation.lock
  .<id>.lock
  archive/<id>/...
```

Known sidecars, locks, staging files and archives must not be enumerated as
primary plans. Invalid primary plans must be returned with per-item diagnostics.
Proposed lane/history/index sidecars must be classified before they are exposed.

Canonical project roots come from trusted host/CLI context, not model input.
Paths in persistent manifests use normalized project-relative separators.
Absolute legacy links may normalize in memory when valid; reads never persist
that normalization. Linked Markdown remains restricted to the workplan tree.
Specs are read inputs, not writable recovery targets.

Reject escapes and symlink substitutions; do not weaken the baseline's symlink
policy merely because a rooted filesystem API can follow an in-root symlink.
Case-insensitive filesystems, hard-link aliases and root replacement must have
explicit tested policies. Do not promise protection against an uncooperative
editor's final check/rename race that the implementation cannot provide.

## 5. Snapshot and hash interoperability

`planHash` binds exact JSON, linked Markdown and every linked spec byte sequence.
`stateHash` additionally binds checkpoint, dependencies and pending journal.
Lock/temp files are coordination machinery, not semantic plan state.

Retain the baseline hash algorithm unless a separately versioned migration is
approved:

```text
SHA256(version + "\n" + compact_manifest_JSON + "\n")
version = "workplan-plan-v1" or "workplan-state-v1"
present entry = {"path": relative_path, "sha256": SHA256(exact_bytes)}
missing entry = {"path": relative_path, "missing": true}
```

Entries are sorted by the reference implementation's JavaScript string order.
Go must not assume its map ordering, byte ordering or default JSON escaping
produces identical hashes. Golden vectors must cover UTF-16 path ordering,
non-BMP characters, escaping, object property order, newlines and missing files.
The initial Go manifest encoder must reproduce the reference representation.
Never rename an incompatible algorithm to the existing `*-v1` label.

Stable snapshots must detect ordinary changes while reading the complete
artifact set. Reuse bytes inside an operation without using stale caches as
mutation authority. Return current hashes from reads and successful mutations.
An absent/unsafe required artifact cannot produce a verified fresh checkpoint.

Native existing-state mutations require `expectedHash`; fresh create uses
must-be-absent preconditions. Legacy direct-call compatibility may permit an
omitted hash, but a concurrent change still must not silently overwrite data.

## 6. Transactions and recovery

Mutation sequence:

1. Decode and validate input, artifact scope, complete read/write intent and
   expected state. Allocate path names without filesystem side effects.
2. Obtain authorization for exact resources through the host boundary.
3. Acquire workspace linkage lock, then plan lock, in deterministic order.
4. Reread state and all preconditions under locks; reject stale/conflicting intent.
5. Exclusively stage same-directory content, preserve safe modes and sync bytes.
6. Publish a durable journal before the first artifact replacement.
7. Publish individual artifacts atomically where the supported filesystem allows.
8. Sync directories where supported, complete journal cleanup and return outcome.

All lifecycle writers, checkpoint writes, dependency changes, compaction and
recovery use this engine. No raw-write helper may bypass it. No writer lock is
held while a user approval prompt is pending. Denial/cancellation before
authorization produces no locks, directories, staging, journals or artifacts.

Workspace-wide linkage serialization must cover ownership checks **through
publication/recovery**. Distinct plans racing to one absent Markdown destination
must produce one winner, not duplicate ownership or overwritten bytes. Pending
journals also claim destinations; ambiguous claims fail closed.

Lock metadata includes host, PID, nonce and start time. Baseline wait is five
seconds; abandonment grace is five minutes. Age alone is never enough to reclaim
a live, reused, foreign or ambiguous owner. Cleanup must not unlink a replacement
owner's lock. Lower-level lock alternatives require equivalent crash/reclaim
tests and interoperability with any still-supported writer.

Recovery is explicit `resume` or `rollback`, separate from ordinary updates.
Validate the journal **before requesting authorization**:

- Identity, canonical paths, content/hash consistency and unique semantic targets.
- Own plan JSON and verified current/before/after linked Markdown relationships.
- Own recognized sidecars and valid, complete, exclusive own compaction archives.
- Operation consistency, not an operation string granting arbitrary authority.
- Any changed normalized `planFile` requires its exact new Markdown target with
  absent before-image and non-null after-image. Old-only/deleted/overwriting or
  invented links must reject. Equivalent absolute/relative links are not moves.

Then preflight **every** current target against its before/after hash. Any
external third state preserves the journal and aborts; no unconditional rollback.
Partial moves with already-published destination bytes remain resumable.
Failure after staging/publication reports recovery-required, never ordinary
success with a mixed generation. Read-only diagnostics must expose an interrupted
state hash even before a new primary JSON is published, so native recovery can
obtain its required `expectedHash`; this is an explicit Go acceptance case.

Initial supported write platforms should be Darwin and Linux local filesystems.
Go's `os.Rename` does not promise atomicity on non-Unix systems; Windows write
support requires a separate equivalent primitive/test gate. `File.Sync` and
directory-sync limitations must be reported, not described as universal durability.

## 7. Markdown, checkpoints and compaction

Preserve handwritten Markdown unless full replacement is explicit. Refresh
generated Markdown only after exact comparison with the prior generated bytes.
Localized patches must preserve markers and unrelated sections, and must not
rewrite JSON as a side effect.

Moving missing source Markdown fails unless the caller supplies nonblank
replacement content; no implicit generation. Blank placeholder arguments do not
clear data. Same-link metadata updates and explicit repair remain distinct.

Checkpoint v2 binds the complete plan manifest. Read checkpoint v1 but classify
its freshness as legacy/unverified; reads never upgrade it. Stale summary and
next action must not drive execution. Retain stale guardrails/blockers/references
as reconfirmation warnings. Evidence remains unverified unless separately checked
against the actual tested code state; plan hashes are not code fingerprints.

Compaction order: update → checkpoint → preview → authorized apply. Apply requires
fresh multiartifact checkpoint, current hash, the exact preview token and
`ARCHIVE_SELECTED_HISTORY`. The token binds state, selection, exact removed
contents, reason and Markdown treatment; it is not a permission credential.
Archive complete originals before pruning, preserve unfinished work/open findings/
scope/constraints and needed prerequisite summaries. Custom Markdown preservation
means compaction does not necessarily shrink a large handwritten history.

## 8. Bounded continuation and doctor

Retain `maxChars` default 12000, range 4096–64000; page `limit` default 20,
range 1–100. Every accepted budget must produce a valid bounded packet, even with
long/escaped metadata. Preserve machine IDs, hashes, freshness states, danger
counts and exact retrieval pointers. Truncate display details transparently;
compact JSON is allowed when pretty output does not fit.

Pin current work and essential safety summaries; page other records
deterministically. Snapshot/options-bound checksummed cursors must progress past
oversized items only with explicit omission/truncation markers. No silent
disappearance of critical findings or nested unknown metadata.

Doctor is read-only: canonical/requested root, schema/link/freshness problems,
artifact classification, pending lock/journal state and recovery pointers. Host
supplies effective registration and available permission facts. Unprovable facts
remain unknown; doctor never asks approval, repairs, changes policy or restarts.

## 9. References and baseline

- Completed local baseline: `~/.config/opencode/src/custom-tools/workplan/`,
  `packages/workplan-tools/`, `tests/workplan/`, and terminal
  `.opencode/workplan/workplan-tools-hardening.{json,md}` in that checkout.
- [Go context](https://pkg.go.dev/context), observed tagged Go 1.27.1 docs.
- [Go encoding/json](https://pkg.go.dev/encoding/json).
- [Go os and platform behavior](https://pkg.go.dev/os#Rename).
- Next: [protocol](02-protocol-and-adapter.md), [acceptance](05-migration-and-acceptance.md).
