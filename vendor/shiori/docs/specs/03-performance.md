# 03 — Performance and data structures

Status: proposed, benchmark-gated. Language choice is not a performance result.

## 1. Measure before changing storage

Record reference TypeScript and Go results on the same machine, fixtures and
operation state. Cover approximately 100 KiB, 1 MiB and 10 MiB primary plans;
large handwritten Markdown histories; many specs; 10,000+ steps/findings; deep
dependency chains; escaped Unicode; and large unknown metadata.

Operations: cold/warm read, inspect/resume pagination, checkpoint, update,
preview/apply and interrupted-state doctor/recovery. Measure median/p95 latency,
peak memory, bytes read/written, parses/hash passes/serialization, allocations,
payload length and IPC/permission overhead separately. Optional 100 MiB stress
fixtures must not change default input limits silently.

Targets must be recorded **before** optimization implementation, after baseline
measurement. No arbitrary “Go is faster” claim and no speed target that waives
durability, permissions or correctness. Profiling must identify the dominant
component and retain before/after results tied to code/fixture fingerprints.

## 2. Retain a shallow tree; add purpose-specific indexes

The plan already forms a shallow tree: plan → ordered phases → ordered steps.
Do not replace it with one balanced tree simply for branding or asymptotics.

| Structure | Purpose | Expected complexity / caveat |
| --- | --- | --- |
| Ordered slices | Stable display, serialization and insertion order | Scans remain O(n) |
| `phaseById` map | Direct phase lookup | Expected O(1); rebuild/invalidation required |
| Composite step map | `(phaseId, stepId)` lookup | Expected O(1); no global-step-ID assumption |
| DAG adjacency maps | Prerequisites and dependents | Cycle detection/topological pass O(V+E) |
| Ready queue/counts | Incremental scheduling recommendations | Only after correct invalidation; no automatic execution |
| Ordered severity buckets | Important unresolved findings | O(n) build, O(k) projection; preserve stable tie order |
| Heading/marker range index | Targeted Markdown reads | Hash-bound offsets; preserve original prose |
| Bounded LRU snapshots | Repeated lookup/page reuse | Memory budget, explicit invalidation and ownership |

Go step keys should be a structured pair, not delimiter-concatenated strings
that collide when legacy IDs contain delimiters. Index construction still costs
O(n); savings require reuse or batches. Duplicate/ambiguous identities remain
errors, not “last map entry wins.”

Dependency indexes must retain archived prerequisite summaries. Missing dependency
data means unrecorded, not inferred independent work. A cancelled prerequisite
does not automatically satisfy a completed prerequisite; scheduler behavior needs
an explicit policy and observable blocked reasons.

## 3. Reduce repeated work, not safety

- Read each artifact into one reusable buffer per required validation pass;
  parse/hash the same bytes and avoid nested helpers reopening them needlessly.
- Cache validated parsed/indexed snapshots and rendered projections by artifact
  identity/hash. Use metadata/inode/size/time only as hints for invalidation.
- Final mutations still require authoritative fresh state and under-lock checks;
  cached indexes, timestamps and previous approvals cannot authorize a write.
- Bound concurrent spec reads/hashing, open descriptors, memory and queue depth.
  All workers must observe cancellation and clean up on failure.
- Select the requested page before building rich projections. Avoid O(n) rendering
  followed by repeated whole-packet serialization just to return twenty items.
- Reuse clients/event connections where proven safe, but preserve fresh instance/
  stream proof and per-invocation correlated approvals; no grant cache.

Hash-bound derived indexes may be discarded/rebuilt. They are not a second
authoritative store and cannot turn external edits into invisible state changes.
Cache validity optimizations must retain the integrity contract from [01](01-core.md).

## 4. History and Markdown

Growing receipts/notes should be evaluated for append-only or chunked history
sidecars with stable references. Keep scope, decisions, unresolved findings,
current work and safety summaries readily available. Old archives remain
discoverable; compaction never quietly deletes evidence or handwritten content.

Use a marker/heading-to-byte-range index for section retrieval first. Evaluate
ropes/piece tables only if localized editing is a measured copying/allocation
bottleneck. They reduce in-memory copying, not automatically full-file writing
or integrity hashing. Ranges must use documented UTF-8 byte offsets and be
invalidated by content changes; JS character offsets are not interchangeable.

Structural sharing is optional for cached snapshots if profiling shows allocation
pressure. Immutable versions still need correct unknown-metadata preservation.

## 5. Conditional disk structures

A B-tree-backed/SQLite store is a later candidate **only** when JSON loading or
whole-document persistence remains the measured bottleneck. Do not place it on
the required first-release path. It requires a separate durable design for:

- Authority/export/import rules and round-trip preservation of user content.
- Transactions, crash recovery, locks, downgrade and interrupted migration.
- Compatibility with existing JSON/Markdown and mixed-version writers.
- Indexed pagination, dependency updates, schema versions and portability.

The “database plus JSON both authoritative” model is forbidden. Keep exactly one
authority and explicit projections/exports. No automatic migration on reads.

## 6. Performance acceptance

Every optimization must pass the same safety/semantic fixtures before its speed
result is accepted. Record cold/warm behavior, cache invalidation after external
edits, large-output bounds, concurrent processes and cancellation stress.
Evidence must include the tested code state; faster partial output that hides
critical omissions is a regression.

Go candidates: standard streaming JSON, `json.RawMessage` for preserved unknown
values, bounded goroutines, reusable buffers and profiling. Verify rather than
assume serializer/hash parity or zero-copy behavior.

- [Go JSON APIs](https://pkg.go.dev/encoding/json)
- [Go profiling](https://go.dev/doc/diagnostics)
- Next: [worktrees](04-worktrees.md), [acceptance](05-migration-and-acceptance.md).
