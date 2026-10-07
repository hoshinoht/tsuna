# 06 — Extensions (to-review)

Status: **to-review, optional.** Proposed 2026-09-30. None of these is required
for stages A–G or for any acceptance gate in [05](05-migration-and-acceptance.md).
Each needs owner review before it is scheduled, and each ships only after
stage D.

## 1. Ground rules

- Keep the original workplan design: no V2 plan field changes, no new or renamed
  `workplan_*` tools, same hashes and generated Markdown.
- Every extension is a separately versioned, parent-owned sidecar classified per
  [01 §4](01-core.md) before it is exposed, and written only through the
  transaction engine ([01 §6](01-core.md)).
- A missing or corrupt extension sidecar degrades that feature only; it never
  hides plan state or blocks core operations.
- Performance items follow [03](03-performance.md): measure, record a target,
  then change.

## 2. Candidates

Ordered by expected value for effort.

| ID | Extension | Value | Depends on |
| --- | --- | --- | --- |
| X1 | Per-file hash cache + Merkle-style manifest | Skip rehashing unchanged artifacts; state-hash recompute proportional to what changed. Targets the measured 10 MiB warm regression | 03 baseline |
| X2 | Evidence ledger | Makes completion checkable instead of asserted | stage C |
| X3 | Worktree lanes: path-claim trie, lane state machine, baseline tree fingerprint, merge train | Safe parallel execution | [04](04-worktrees.md), X2 |
| X4 | Hash-chained event log | Audit trail, cheap "changed since checkpoint", basis for undo | stage C |
| X5 | Cross-plan workspace graph | Portfolio view of related plans in one repo | X4 optional |
| X6 | Critical path and slack | Surface the steps that block the most work | stage F ready queue (readiness delivered early in D.2) |
| X7 | Session ↔ step/lane links | Precise handoffs across sessions | X3 optional |
| X8 | Warm snapshot cache (buffer pool) | Repeat reads skip parsing; near-zero cost when nothing changed | stage D serve process |
| X9 | Derived plan index (materialized view) | Reads touch only the slices they need; ready queue and hashes precomputed | X8, X1 |
| X10 | Structural index + slice parsing | Filtered read/inspect/resume parse only the selected byte ranges | X9 optional |
| P2 | Compaction advisor | Keeps long-running plans small without manual bookkeeping | stage C compaction |
| P3 | Note rollover | Keeps the plan roughly constant in size over hours of iteration | P2, stage C compaction |
| P4 | Journal v2 by reference | About 3x fewer bytes written per mutation on large plans | stage C journal |
| P5 | Markdown section index for resume | Smaller resume packets on Markdown-heavy plans | stage B resume |
| P6 | Stray-file classification in doctor | Tidy workplan roots; no unknown files silently ignored | stage B doctor |

## 3. Sketches

### X1 — Hash cache and Merkle manifest

Cache `sha256` per artifact keyed by `(device, inode, size, mtime_ns)`; any key
mismatch rehashes. The cache is a hint, never mutation authority: mutations
still reread and rehash under locks. The `*-v1` hash output must stay
byte-identical; the Merkle structure is internal only.

### X2 — Evidence ledger

Candidate `<id>.evidence.json` v1: per `(phaseId, stepId)` records of
`{command, exitCode, treeOid, outputDigest, recordedAt, source}`. `treeOid` is
the git tree actually tested, including uncommitted changes via a temporary
index. Evidence is stale when files owned by the step changed after `treeOid`.
Plan hashes are not code fingerprints (01 §7); this adds the missing code-side
binding. `metric-loop` results can be imported as evidence.

**Status: implemented in stage E1** (2026-10-07,
[contracts §20](../contracts.md#20-approved-design-changes-e1-approved-2026-10-07)).
Evidence enters through `workplan_update.recordEvidence` (no new tool) and
`shiori evidence`; scope paths stand in for step ownership until X3.

### X3 — Worktree lanes, concrete structures

- **Path-claim trie:** owned paths and globs per lane in a prefix tree with
  wildcard nodes; overlapping claims are rejected before a lane is created.
- **Lane state machine:** `claimed → prepared → running → review → integrating
  → merged | abandoned`. Doctor reports orphans (worktree without owner,
  unmerged branch for a completed step) and requires explicit disposition (W03).
- **Baseline fingerprint:** git tree OID of the starting state, dirty changes
  included, recorded in the lane manifest (W01).
- **Merge train:** topological order of lane steps from the dependency DAG,
  ties broken by predicted file overlap; combined state is retested after each
  integration (W02). Merges stay user- or orchestrator-authorized, never
  automatic.

**Status: implemented in stage E2** (2026-10-07,
[contracts §22](../contracts.md#22-approved-design-changes-x3-approved-2026-10-07))
through `workplan_update.lanes`. Claims are path prefixes (no globs yet);
the merge order uses the dependency DAG only (claims never overlap, so
there is no predicted overlap to break ties); lane evidence covers W02.

### X4 — Hash-chained event log

Append-only `<id>.history.jsonl`; each entry carries operation, actor source,
before/after state hashes and the previous entry's hash. Candidate scope
extension (database LSM/WAL lesson): new notes may be appended here and
rolled into the plan or archive by compaction (P3), so a note write is O(note)
instead of rewriting the whole plan. This changes where notes live and needs
its own compatibility decision before adoption. Compaction archives
closed log segments instead of rewriting them. Undo is a separately reviewed
follow-up, not part of X4.

**Measured 2026-10-07 (owner roadmap, 286 KB):** notes are 68% of the
plan JSON (361 notes, median 485 bytes) and are rendered into the
generated Markdown. Moving them out is a format change for every notes
consumer (generated Markdown, rollover, advisor, decision register,
archives, the reference plugin on rollback). Since P4 a note append
writes ~1× the plan, resume already omits notes, and rollover archives
old ones, so the remaining gain is plan size for full reads; deferred
until the reference plugin is retired or notes need concurrent writers.

**Status: implemented** (2026-10-07,
[contracts §24](../contracts.md#24-approved-design-changes-x4-approved-2026-10-07)):
the log is appended under the commit's locks after the transaction
completes (advisory, outside the hashes; gaps are detected, never
trusted), and its per-entry element changes let a stale
`workplan_update` with `rebase` apply over newer writes to other
elements. Resume shows the writes since the last checkpoint, doctor the
log and stalled steps. Notes stay in the plan; undo is not done.

### X5 — Cross-plan workspace graph

Optional links between plans in the same coordination root (for example a
roadmap blocking a migration plan). Read-only portfolio projection first;
cross-plan writes need their own locking design.

**Status: implemented** (2026-10-07,
[contracts §26](../contracts.md#26-plan-links-templates-and-quality-checks-approved-2026-10-07)):
per-plan `<id>.links.json` written by `workplan_update.planLinks`; the
portfolio (doctor, resume `waitingOnPlans`, `shiori portfolio`) is
read-only. No cross-plan writes.

### X6 — Critical path and slack

**Status: accepted and implemented in stage D.2** (2026-09-30;
[contracts §12](../contracts.md#12-approved-design-changes-d2-approved-2026-09-30)
G6).

Longest-path and slack over the step DAG, weighted by optional estimates.
Resume and inspect may show "blocks N downstream steps". Recommendations only;
never automatic execution.

As implemented: the critical path is the heaviest chain of open steps
(weight = a step's optional numeric `estimate` member, else 1). Inspect
(top level) and doctor (per plan) show it when it chains at least two open
steps. Inspect shows `unblocks` and `slack` per open step. Resume ranks
ready work by `unblocks` and shows it per item, but leaves the path out to
keep its budget. It is computed on every read from the validated sidecar;
there is no new sidecar or cache.

### X7 — Session links

Record OpenCode session IDs from trusted host context (never model input) that
touched each step or lane, for `/handoff` and resume.

### Long-plan measurements behind P2–P6

Measured 2026-09-30 on a copy of a real plan after many hours of iteration
(257 KB JSON, 203 KB Markdown, 12 phases, 62 steps, 214 notes). Go stage B
timings were ~22–24 ms for every read-only operation including process start,
so parse/hash cost is not the bottleneck at this size; X1 matters only at
~10 MB. The costs are elsewhere:

- `notes` is 48% of the JSON; terminal (completed/cancelled) steps are 48% of
  step bytes; handwritten Markdown sections (work packages, execution
  receipts, decision register) are append-only.
- A full `read` returned 576 KB (~145k tokens) versus ~12 KB for `resume` at
  the 12000 budget. (The owner's OpenCode configuration now steers agents to bounded reads.)
- The v1 journal stores base64 before/after content, so one status change on
  this plan writes on the order of 1 MB plus fsyncs, growing with every note.
- Loose `.patch` files (~200 KB) sit in the workplan root unclassified.

### Database-inspired read path (X8–X10)

Databases avoid re-reading whole datasets: binary pages, persistent indexes,
a buffer pool, and lazy reads of only what a query needs. Shiori cannot make
its canonical files internal the way a database does, because plan JSON and
Markdown must stay the human-, git- and agent-readable source of truth. The
lessons that fit are the read-side ones; writes keep the stage C engine.

- **X8 — Warm snapshot cache.** The `serve --stdio` process keeps parsed
  plans and indexes in memory, keyed by `(device, inode, size, mtime_ns)` of
  every artifact in the manifest, under a memory budget with LRU eviction.
  A key mismatch reparses. The cache is never mutation authority: writes
  reread and rehash under locks as today.
- **X9 — Derived plan index.** A disposable, rebuildable index beside the
  plan (candidate `<id>.index` sidecar, classified per 01 §4, or an internal
  cache directory) holding byte offsets of phases/steps/notes/findings,
  cached artifact hashes (X1), the dependency adjacency and ready queue
  (D.2), and note summaries. It is bound to the plan's `stateHash`: any
  mismatch discards and rebuilds it; correctness never depends on it; deleting
  it is always safe. **Open choice:** SQLite (queryable, mature, adds a
  dependency and cgo/pure-Go driver decision) versus a small custom binary
  format (no dependency, narrower). Decide with measurements.
- **X10 — Structural index + slice parsing.** A single structural pass finds
  the byte ranges of top-level fields and array elements without building
  objects; filtered read, inspect and resume then parse only the slices they
  need with the order- and spelling-preserving parser. Byte fidelity is
  unchanged because the canonical parse still runs for writes and full reads.
  Minor add-ons: hash from a memory-mapped file for large artifacts; hash the
  plan, Markdown and spec files concurrently.

The write-side database technique (a write-ahead log with periodic
checkpoints) is not adopted for the plan itself, because the JSON on disk
would be stale between checkpoints. It fits only the notes history: see X4.

### P2 — Compaction advisor

**Status: accepted and implemented in stage D.4** (2026-09-30;
[contracts §15](../contracts.md#15-approved-design-changes-d4-approved-2026-09-30)
item 1).

`resume` and `doctor` report `compactionRecommended` with the estimated
savings when terminal-step bytes, note count or total size cross configurable
thresholds. Advice only: compaction still requires preview → exact token →
authorized apply ([01 §7](01-core.md)).

As implemented: the estimate is the exact plan JSON saving of archiving
every archivable completed phase, the rollover notes and every resolved
finding (doctor adds the generated-Markdown saving). It is recommended when
that saving reaches 32 KiB and one threshold is crossed: 50 eligible notes,
archivable phases ≥25% of the JSON, or a JSON of 192 KiB (configurable with
`--compaction-advice`). Resume carries a compact form that is dropped before
text would go below the D.1 minimums; doctor carries the detail and a
ready-to-preview selection. On the measured plan it recommends saving
80 969 of 257 151 JSON bytes (31%): 105 rollover notes, the one archivable
phase (2 steps; the other terminal steps sit in unfinished phases, which
compaction never archives) and 32 resolved findings.

### P3 — Note rollover

**Status: accepted and implemented in stage D.4** (2026-09-30;
[contracts §15](../contracts.md#15-approved-design-changes-d4-approved-2026-09-30)
item 2).

A compaction selection mode that archives notes older than the latest N (and
older than the newest checkpoint), keeping notes pinned by the decision
register or referenced by open steps/findings. Archives keep complete
originals. Uses the existing "selected history" mechanism; no V2 field change.

As implemented: `noteRollover {keepLatest (default 20), pinNoteIndexes}`
on `workplan_compact`/`workplan_compact_preview`. Kept: the latest N,
notes marked `[pinned]` or listed, decision records (`decision`/`decided`
or the uppercase `USER` marker, the JSON twin of a Decision register
line), notes naming an open step (`<phase>/<step>`) or quoting an open
finding title, and earlier archive pointers. "Older than the newest
checkpoint" is enforced by apply's fresh-checkpoint rule (notes carry no
timestamps). The token binds the parameters; archives are unchanged and
hold the complete originals. On the measured plan it archives 105 of 214
notes (59 616 note bytes): the plan JSON goes from 257 151 to 196 808
bytes and a full `read` from 576 412 to 515 861 bytes.

### P4 — Journal v2 by reference

A separately versioned `<id>.transaction.json` v2 that records digests and
the paths of same-directory staged files instead of inline base64 content.
v1 journals stay readable and recoverable. Needs its own crash/fault matrix
(S04) and must not weaken third-state detection (S05). This is the only
item here that adds a new artifact version.

**Status: implemented** (2026-10-07,
[contracts §21](../contracts.md#21-approved-design-changes-p4-approved-2026-10-07)):
before images are hard links next to the targets; 3.66× fewer bytes
written per mutation on the measured plan.
The link is checked by file identity against the locked recheck rather
than re-hashed (2026-10-07, write hashing pass).

### P5 — Markdown section index for resume

Hash-bound heading/marker ranges over the plan Markdown so `resume` can point
to "execution receipts §N" by offset instead of inlining text. Reads remain
byte-exact; offsets are invalidated by any Markdown hash change.

### P6 — Stray-file classification

Doctor lists files in the workplan root that are neither known artifacts,
sidecars, locks, staging files nor archives (for example `*.patch`), and
suggests moving them under `archive/`. Never moves or deletes them itself.

## 4. Not proposed

Ropes/piece tables, SQLite storage and Bloom filters remain deferred per
[03](03-performance.md) until measurements justify them.

## 5. Review checklist

For each candidate the owner decides: accept / defer / reject, scope, and the
stage it attaches to. Record decisions here with a date.

| ID | Decision | Date | Notes |
| --- | --- | --- | --- |
| X1 | to-review | | |
| X2 | accepted, implemented | 2026-10-07 | stage E1 (contracts §20) |
| X3 | accepted, implemented | 2026-10-07 | stage E2 (contracts §22); merge train as an ordering, never automatic |
| X4 | accepted, implemented | 2026-10-07 | change log and rebased updates (contracts §24); notes stay in the plan, undo not done |
| X5 | accepted, implemented | 2026-10-07 | plan links sidecar and a read-only portfolio (contracts §26) |
| X6 | accepted, implemented | 2026-09-30 | implemented in stage D.2 (contracts §12 G6) |
| X7 | to-review | | |
| X8 | accepted, implemented | 2026-10-07 | serve snapshot cache; see STATUS "Performance pass" |
| X9 | to-review | | |
| X10 | deferred | 2026-10-07 | measured: serialization ~11% of a warm 10 MB write once parsing is cached; not worth splicing yet |
| P2 | accepted, implemented | 2026-09-30 | implemented in stage D.4 (contracts §15 item 1) |
| P3 | accepted, implemented | 2026-09-30 | implemented in stage D.4 (contracts §15 item 2) |
| P4 | accepted, implemented | 2026-10-07 | contracts §21; v2 is the default writer |
| P5 | to-review | | |
| P6 | accepted | 2026-09-30 | covered by D.1 (F) and D.3 |
