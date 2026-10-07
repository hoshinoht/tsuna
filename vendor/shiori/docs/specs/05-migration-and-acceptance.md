# 05 — Migration and acceptance

Status: specification and proposed sequence, not execution authorization.

## 1. Verified starting point

The local TypeScript hardening task was completed and independently reviewed
before this specification was prepared. Its reference implementation is the
local preset at `~/.config/opencode`; source changes were not committed or
published during the task. Do not cite its old clean Git revision as containing
the new implementation.

Recorded evidence, not Go results:

- Full workplan suite: 56 passed, two opt-in tests skipped, 605 expectations.
- Both opt-in OpenCode 2.0.19/client 2.0.20 smokes separately passed; affected
  lifecycle smoke reran after the final normal-update/journal-link changes.
- Typecheck, structural helper and whitespace checks passed.
- Actual native minimum-budget resume returned 3892 characters within 4096,
  retaining machine IDs/hashes, danger counts and retrieval pointers.
- Actual model-origin checkpoint exercised trusted native identity/signal and
  path authorization, supplementing scratch RPC-relay fixtures.
- Final independent review passed all eight acceptance areas. Two incorrect
  static reports were withdrawn after fresh source/dataflow and regression proof.
- Broad comparison retained 24 failures and one error in unrelated LSP/Vitest
  and visualization suites; that is not a full-repository test pass.

Freeze a reviewed source fingerprint and sanitized fixture corpus before Go
implementation. Retain source licensing/attribution checks before copying code.
The repository's existing GPLv3 license is unchanged by these documents.

## 2. Proposed development stages

| Stage | Outcome | Exit gate |
| --- | --- | --- |
| A. Contract/fixture freeze | Machine schemas, hash vectors, error cases, measured TS baseline | Reviewed semantics and reproducible corpus |
| B. Read-only Go core | Decode/list/read/inspect/validate/resume/doctor and indexes | No artifact writes/mtime changes; parity and budgets |
| C. Transactional Go core | Prepared intents, locks, journal/recovery and compaction | Fault/concurrency/scope/cancel tests; no unsafe bypass |
| D. Native adapter | Existing thirteen tool identities using Go protocol | Actual-host authority, source/signal and denial tests |
| E. Opt-in migration | Explicit backend selection and rollback | No automatic migration or dual authority/writers |
| F. Measured optimization | Batches/caches/indexes/history improvements | Same correctness plus measured target improvements |
| G. Worktree lanes | Parent-coordinated isolated execution | Binding/baseline/integration/cleanup acceptance |

Each stage needs separately authorized implementation ownership, validation and
a finite completion boundary. Stage G must not delay first core compatibility.
SQLite/ropes remain conditional design work, not prerequisites for stage B.

## 3. Compatibility and rollback

Use copied, sanitized fixtures for development and optional read-only shadow
comparison on real projects. Shadow mode must not create locks, journals,
checkpoints or repair files. Output comparisons may normalize declared presentation
differences, never hashes, identities, safety omissions or authorization outcomes.

One selected writer backend per coordination root. During transition, TS and Go
must either share proven lock/journal semantics or reject mixed writers. Merely
giving them matching filename extensions does not establish interoperability.

Switch only after resolving active requests and pending transactions. Backend
selection is explicit and reversible; unsupported versions fail before mutation.
Do not silently upgrade V2 plans, rename tools/agents, rewrite custom Markdown,
or convert JSON to a database on read. A future storage change requires its own
backup/import/export/downgrade and interrupted-migration design.

Rollback first disables new admissions, inventories pending state and preserves
originals. It may use the old backend only when format/hash/journal compatibility
is proven. If not, return blocked with an offline reconciliation path; never
delete recovery evidence to make downgrade appear successful.

## 4. Acceptance matrix

| ID | Required observable behavior |
| --- | --- |
| C01 | V2 known/unknown metadata, optional legacy fields and array order survive unrelated writes |
| C02 | Drafts remain readable; invalid/executable structure has exact field errors |
| C03 | Scoped phase/step IDs and duplicate/cycle checks are correct |
| C04 | JSON/MD/spec/sidecar changes invalidate the correct plan/state hashes |
| C05 | JS/Go hash golden vectors match, including Unicode ordering/escaping/missing files |
| S01 | Every writer follows one authorization/lock/staging/journal engine |
| S02 | Cross-process same-plan and shared-destination races have no lost/duplicate ownership |
| S03 | Denied/cancelled authorization creates no filesystem side effects |
| S04 | Fault after every stage/publication has old/new or explicit recovery-required state |
| S05 | Recovery rejects external third-state edits and preserves evidence |
| S06 | Forged source/other-plan/sidecar/archive/duplicate targets reject before authorization |
| S07 | Invented after-links, wrong/absent/deleted/overwriting MD move targets reject |
| S08 | Valid create/move/reset/checkpoint/dependency/MD/compact recovery works in both modes |
| S09 | Pre-primary creation failure has a read-only state-hash/recovery route |
| S10 | Missing-source moves fail unless explicit nonblank content repairs them |
| S11 | Live/foreign/ambiguous/reused lock owners are never reclaimed merely by age |
| P01 | Native root/identity/signal cannot be supplied by model input |
| P02 | Host-instance and event-readiness proof precedes exact-resource requests |
| P03 | Relative/absolute policy behavior follows actual host semantics and is disclosed |
| P04 | Genuine allow/ask/deny/reject/session override/cancel-late-reply cases are tested |
| P05 | Unrelated proof/asked/replied events cannot authorize or consume the request incorrectly |
| P06 | No private API, self-approval, policy rewrite or replayed approval |
| P07 | Unsupported host/capability and transport loss fail closed with actionable diagnostics |
| R01 | Every accepted 4096–64000 budget fits serialized output, including escaping/nested metadata |
| R02 | Machine identities/hashes/pointers and dangerous omission counts remain truthful |
| R03 | Cursors progress deterministically; stale/options-mismatched/forged cursors reject |
| R04 | Legacy/stale/corrupt checkpoints cannot drive nextAction or authorize compaction |
| R05 | Preview/apply binds exact state/removals/reason/token and archives complete originals |
| R06 | Fresh plan data and code-test evidence remain separate verification gates |
| R07 | Doctor/pure preview leave bytes/mtimes unchanged and unknown facts unknown |
| W01 | Dirty baseline transfer, actual session/cwd, ownership and dependency binding verified |
| W02 | Lane receipts bind code state; combined integration is retested |
| W03 | Dirty/unmerged/orphaned lanes preserve work and require explicit disposition |
| M01 | Explicit backend switch/rollback has no dual writer or silent migration |
| M02 | Go performance results report comparable cold/warm fixtures and safety parity |

C/S/P/R/M gates apply to core rollout; W gates apply when lane orchestration is
implemented. A read-only pilot does not claim transactional/native write completion.

## 5. Validation methods

Use table-driven examples and generated/fuzz cases where real invariants justify
them: round-trip metadata, hash compatibility, cursor bounds, journal scope and
dependency cycles. Preserve minimized counterexamples. No framework is required
solely to advertise property testing.

Separate-process barriers must test create/create and move/move sharing one absent
destination, same/different plans, pending journal claims and lock replacement.
Fault injection must cover staging, durable journal, each artifact publication,
directory sync, cancellation and cleanup. Test readable mixed/invalid states
without automatic recovery side effects.

Native integration must run on actual supported hosts. Headless scratch RPC
relays are useful for policy/runtime behavior but fixture-supplied identities
alone do not prove model dispatch supplied trusted context. Include at least one
actual native invocation and cancellation/source binding verification. No new
provider spend, credential access or shared-service interruption is implied by
this spec; arrange safe verification explicitly.

Initial platform gate: Darwin/arm64 and Linux/amd64 local filesystems. Network
filesystems and Windows writes are unsupported until atomicity/durability/path/
locking behavior is independently validated. Builds alone do not prove those gates.

After correctness, measure [03](03-performance.md)'s matrix with code and fixture
fingerprints. Proposed future commands include Go tests, race detection, vet,
benchmarks and fuzz runs; none has been run against a Go implementation here.

## 6. Proposed delivery and unresolved selections

Implementation-ready contracts: V2 preservation, authority split, exact intent
binding, safe transactions/recovery, bounded projections, dependency identity,
performance measurement and worktree parent ownership.

Before implementation, choose/freeze: schema/binding generation tooling, artifact
limit defaults, packaging/distribution method, process lifecycle strategy, exact
standalone CLI mutation confirmation policy and minimum supported host/platform
matrix. These choices must not relax required behavior. SQLite, ropes, remote
lanes and provider orchestration are deliberately deferred, not hidden blockers.

Specification completion means these documents are coherent and reviewable;
it does not mean Go code exists, performance targets are achieved, binaries ship,
or repository publication was approved.

## 7. References

- [Core](01-core.md), [protocol](02-protocol-and-adapter.md),
  [performance](03-performance.md), [worktrees](04-worktrees.md).
- [OpenCode V2 API](https://opencode.ai/v2/docs/api)
- [Go diagnostics](https://go.dev/doc/diagnostics)
- [Go context](https://pkg.go.dev/context)
- [Go filesystem operations](https://pkg.go.dev/os)
