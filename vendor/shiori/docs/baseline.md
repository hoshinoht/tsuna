# TypeScript reference baseline (stage A)

Status: measured on 2026-09-30. This is reference data for [03](specs/03-performance.md).
It is not a Go result and not a performance target. Targets are set only after
the stage B measurement on the same machine and fixtures.

## Environment

| Item | Value |
| --- | --- |
| Machine | Apple M4 Pro, 12 cores (8 performance + 4 efficiency), 24 GiB RAM, on AC power |
| OS | macOS 27.0.1 (build 26A434), Darwin arm64, local APFS |
| Runtime | bun 1.4.0 |
| Reference code | Fingerprinted file by file in `testdata/MANIFEST.json` → `oracle.files`. The checkout at `fa060fc` has uncommitted changes, so the file hashes, not the commit, identify it |
| Library | zod 4.1.8 (the schema layer used by the reference) |
| Raw results | [`testdata/perf/baseline-results.json`](../testdata/perf/baseline-results.json): two complete runs |

## Fixtures

The fixtures are deterministic generated plans. `id` is `perf-plan`. Phases hold
50 steps each. Every step has a title, target, action and validation. The plan
also has one finding per 10 steps (severities rotate; every third finding is
resolved) and one note per 10 steps. The Markdown is the reference's generated
rendering, so every file counts as "generated". There are no spec files or
sidecars.

| Fixture | Steps / phases / findings / notes | JSON bytes | MD bytes | Stored as |
| --- | --- | --- | --- | --- |
| perf-100k | 258 / 6 / 25 / 25 | 102,415 | 83,621 | `testdata/perf/perf-100k/` |
| perf-1m | 2,631 / 53 / 263 / 263 | 1,048,711 | 860,239 | `testdata/perf/perf-1m.tar.gz` |
| perf-10m | 26,038 / 521 / 2,603 / 2,603 | 10,486,061 | 8,648,334 | `testdata/perf/perf-10m.tar.gz` |

Archive sha256: `perf-1m.tar.gz`
`f18891949bb1f2ddc37f97b4af6864cc3342a859c70cb284578d9e174a0ffbbc` and
`perf-10m.tar.gz` `701f2261d6cc4aee3e573886b78f4b9bdf8d802e77522f4cae9c2907f5fb4e4c`.
The archive bytes are not reproducible (tar metadata); the extracted file
hashes are the fixture identity:

```text
cd06bc5924c87ee1c20c95c7537c9851e1218f6d1c27608e4fe9b60c7a27a565  perf-100k/.opencode/workplan/perf-plan.json
f11fd877ebd38fb939619b7f663fcebb5cafd12ac508bb5072f421479ed49a35  perf-100k/.opencode/workplan/perf-plan.md
2561e5ef5d51549b500e135cf80a4135164b6ffcd3d0d64bdf27ff919ffff25a  perf-1m/.opencode/workplan/perf-plan.json
df0e796e1a42fa01f3e3cd42dbcf03f41000a33452cdd79ea28ddd30d1784b0a  perf-1m/.opencode/workplan/perf-plan.md
75b17b3339d7be5c6a671369eebc8e200d777fcdc6947fb09275cbf310354e36  perf-10m/.opencode/workplan/perf-plan.json
0d09581aade5ea64c4fba6d6c46e5c256cdfc874639e0496403bf842f6bcff22  perf-10m/.opencode/workplan/perf-plan.md
```

## Method

- **Operations.** Each operation calls the reference tool's `execute` with core
  input, default options and the fixture root as the workspace.
  - `read`: `{id}`, which includes the Markdown.
  - `inspect`: `{id}`, limit 100.
  - `resume`: `{id}`, maxChars 12000, limit 20.
  - `validate`: `{id}`.
  - `update`: `{id, appendNotes: ["bench note i"]}`, with no `expectedHash`.
- **Permission prompt.** The context's `ask` resolves at once. So `update`
  includes locking, staging, journaling, publication and fsync, but no
  host-permission or IPC time.
- **Cold.** Each sample is a new `bun` process that imports the reference and
  runs one call. The table reports:
  - the in-process time of that first call ("cold first call"), and
  - the wall time of the whole process ("cold process"). This includes bun start
    and module import. A no-op process of the same kind measured
    28 ms median / 44 MiB RSS for the 100k and 1m fixtures, and 29 ms / 61 MiB
    for 10m.
- **Samples.** 15 cold processes per operation (5 for perf-10m).
- **Warm.** One process runs 2 unrecorded warm-up calls, then 100 recorded calls
  (30 for perf-1m, 10 for perf-10m).
- **Update isolation.** For `update`, the original JSON and Markdown bytes are
  restored before each iteration. The restore is outside the timed region.
- **Peak RSS.** The maximum resident set size reported by `/usr/bin/time -l` for
  the process, which includes the bun runtime.
- **Percentiles.** p95 is the nearest-rank value. With only 5 cold samples, the
  p95 for perf-10m is the maximum.
- **Repeat.** The whole matrix ran twice. Run 2 is shown; the last column gives
  the run 1 warm median.

## Results (run 2; ms)

| Size | Op | Warm median | Warm p95 | Cold first call median / p95 | Cold process wall median / p95 | Peak RSS cold median (max) | Output chars | Run 1 warm median |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| 100 KiB | read | 2.12 | 2.89 | 5.5 / 6.0 | 34 / 35 | 49 MiB (50) | 293,459 | 1.89 |
| 100 KiB | inspect | 1.64 | 2.40 | 5.5 / 6.6 | 34 / 36 | 47 MiB (48) | 34,604 | 1.70 |
| 100 KiB | resume | 1.93 | 2.79 | 6.4 / 7.4 | 35 / 36 | 47 MiB (48) | 11,440 | 1.91 |
| 100 KiB | validate | 2.02 | 3.23 | 5.9 / 6.5 | 35 / 38 | 48 MiB (48) | 815 | 1.66 |
| 100 KiB | update | 11.09 | 13.27 | 21.6 / 38.6 | 50 / 70 | 58 MiB (60) | 760 | 10.30 |
| 1 MiB | read | 7.10 | 9.28 | 14.7 / 15.5 | 42 / 44 | 84 MiB (89) | 3,004,147 | 7.01 |
| 1 MiB | inspect | 4.30 | 5.76 | 11.5 / 11.8 | 39 / 40 | 62 MiB (63) | 34,605 | 4.72 |
| 1 MiB | resume | 4.60 | 5.72 | 13.1 / 13.7 | 41 / 42 | 62 MiB (63) | 11,524 | 5.02 |
| 1 MiB | validate | 4.99 | 6.04 | 12.8 / 14.2 | 40 / 43 | 64 MiB (65) | 814 | 5.12 |
| 1 MiB | update | 24.87 | 27.62 | 41.5 / 45.5 | 70 / 76 | 126 MiB (128) | 759 | 25.31 |
| 10 MiB | read | 58.43 | 61.58 | 70.5 / 71.0 | 101 / 103 | 319 MiB (335) | 30,066,121 | 60.57 |
| 10 MiB | inspect | 24.80 | 26.92 | 38.2 / 38.7 | 68 / 73 | 178 MiB (179) | 34,612 | 26.82 |
| 10 MiB | resume | 26.17 | 28.13 | 40.7 / 42.6 | 71 / 72 | 180 MiB (180) | 11,536 | 26.44 |
| 10 MiB | validate | 29.07 | 35.27 | 47.0 / 48.1 | 77 / 79 | 226 MiB (227) | 819 | 28.37 |
| 10 MiB | update | 152.45 | 160.58 | 202.6 / 243.4 | 246 / 286 | 711 MiB (732) | 764 | 161.34 |

"Output chars" is the length of the returned text in UTF-16 code units.

## Observations (reference behaviour, not targets)

- Warm latency grows roughly linearly with plan size. From 1 MiB to 10 MiB it
  grows about 5.5–8×, and `update` is the most expensive operation: it parses,
  serializes and hashes the JSON, renders and compares the Markdown, journals
  and fsyncs. At 10 MiB, `update` peaks near 0.7 GiB RSS.
- `resume` stays inside its 12000-char budget at every size (11.4–11.5 K), but
  still costs O(plan) time and memory. `inspect` output is bounded by `limit`.
- `read` output has no bound: 30.1 M chars for the 10 MiB plan. This exceeds the
  16 MiB frame proposed in spec 02; see contracts D9.
- At 100 KiB, process start and module import (~28 ms) dominate every one-shot
  call. For the native adapter that is the case for a long-lived child
  (contracts 5.4).

## Not yet measured (spec 03 matrix gaps)

- Checkpoint, compaction preview/apply, doctor, and interrupted-state recovery
  latency.
- Plans with many spec files, deep dependency chains, heavy escaped Unicode,
  large unknown metadata, or 10,000+ findings.
- Separate counts of bytes read/written, parse/hash/serialization passes and
  allocations.
- IPC and permission-broker overhead (no adapter exists yet).
- Concurrent-process and cancellation stress.
- Linux/amd64.

Stage B measured the Go read path on the same fixtures and machine; see below.
No performance target is set yet (spec 03 §1: stage F sets targets from these
two tables).

## Go stage B results (same machine, 2026-09-30)

| Item | Value |
| --- | --- |
| Machine / OS | As above (Apple M4 Pro, macOS 27.0.1, darwin/arm64, local APFS) |
| Toolchain | Go 1.27.1, `CGO_ENABLED=0 go build -trimpath` |
| Code | Working tree of stage B (uncommitted; the engine at the time of this run) |
| Fixtures | The same three fixtures, verified against the sha256 list above before measuring |
| Raw results | [`testdata/perf/go-baseline-results.json`](../testdata/perf/go-baseline-results.json) |
| Harness | `SHIORI_BASELINE=<out.json> go test ./internal/engine -run '^TestBaselineMatrix$' -v` |

Method, matched to the reference:

- **Inputs.** The same operation inputs, with default options: `read {id}`
  including the Markdown; `inspect {id}` at limit 100; `resume {id}` at 12000
  and limit 20; `validate {id}`. The timed region includes serializing the
  result text, which the reference also returns.
- **Warm.** In one process, 2 unrecorded calls, then 100 recorded (30 for
  perf-1m, 10 for perf-10m).
- **Cold first call.** The in-process time of the first call in a fresh
  process: 15 processes (5 for perf-10m).
- **Cold process wall.** The wall time of the `shiori <op> perf-plan --json`
  binary as a fresh process, including start-up and writing the output.
- **Peak RSS.** The maximum resident set size of that CLI process, from
  `/usr/bin/time -l`.
- **Percentiles.** p95 is the nearest-rank value.

"Output chars" is in UTF-16 code units. It includes the absolute root path,
which differs in length from the reference's measurement root, so it differs
from the reference by a few characters. `update` is not measured: writes are
stage C.

| Size | Op | Warm median | Warm p95 | Cold first call median / p95 | Cold process wall median / p95 | Peak RSS median (max) | Output chars |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 100 KiB | read | 0.79 | 0.89 | 0.90 / 3.45 | 6.6 / 432.7¹ | 9 MiB (9) | 293,557 |
| 100 KiB | inspect | 0.43 | 0.63 | 0.61 / 0.73 | 5.6 / 5.8 | 7 MiB (7) | 34,702 |
| 100 KiB | resume | 0.46 | 0.66 | 0.60 / 0.73 | 5.6 / 5.9 | 7 MiB (7) | 11,489 |
| 100 KiB | validate | 0.38 | 0.59 | 0.53 / 0.59 | 5.7 / 6.6 | 6 MiB (6) | 913 |
| 1 MiB | read | 7.26 | 7.83 | 8.09 / 8.65 | 14.3 / 15.1 | 25 MiB (28) | 3,004,245 |
| 1 MiB | inspect | 3.91 | 4.13 | 4.17 / 4.47 | 9.5 / 9.7 | 12 MiB (12) | 34,703 |
| 1 MiB | resume | 4.51 | 4.82 | 4.85 / 4.97 | 10.3 / 10.9 | 14 MiB (14) | 11,573 |
| 1 MiB | validate | 3.93 | 4.25 | 4.13 / 4.27 | 9.5 / 9.8 | 12 MiB (12) | 912 |
| 10 MiB | read | 81.82 | 95.74 | 86.90 / 94.95 | 101.3 / 105.1 | 176 MiB (178) | 30,066,219 |
| 10 MiB | inspect | 36.15 | 36.74 | 37.20 / 37.42 | 42.8 / 43.0 | 52 MiB (52) | 34,710 |
| 10 MiB | resume | 40.58 | 42.72 | 41.29 / 42.01 | 47.7 / 48.3 | 71 MiB (72) | 11,585 |
| 10 MiB | validate | 35.51 | 36.54 | 36.47 / 38.13 | 42.9 / 43.3 | 52 MiB (60) | 917 |

¹ This is the first execution of a freshly built binary. macOS checks a new
executable on its first launch, so this one sample includes that check. Every
other sample is 5–7 ms.

### Comparison with the reference (median; ratio = TS / Go)

| Size | Op | Warm TS → Go (ms) | Cold process TS → Go (ms) | Peak RSS TS → Go (MiB) |
| --- | --- | --- | --- | --- |
| 100 KiB | read | 2.12 → 0.79 (2.7×) | 34 → 6.6 (5.2×) | 49 → 9 |
| 100 KiB | inspect | 1.64 → 0.43 (3.8×) | 34 → 5.6 (6.1×) | 47 → 7 |
| 100 KiB | resume | 1.93 → 0.46 (4.2×) | 35 → 5.6 (6.3×) | 47 → 7 |
| 100 KiB | validate | 2.02 → 0.38 (5.3×) | 35 → 5.7 (6.1×) | 48 → 6 |
| 1 MiB | read | 7.10 → 7.26 (1.0×) | 42 → 14.3 (2.9×) | 84 → 25 |
| 1 MiB | inspect | 4.30 → 3.91 (1.1×) | 39 → 9.5 (4.1×) | 62 → 12 |
| 1 MiB | resume | 4.60 → 4.51 (1.0×) | 41 → 10.3 (4.0×) | 62 → 14 |
| 1 MiB | validate | 4.99 → 3.93 (1.3×) | 40 → 9.5 (4.2×) | 64 → 12 |
| 10 MiB | read | 58.43 → 81.82 (0.71×) | 101 → 101.3 (1.0×) | 319 → 176 |
| 10 MiB | inspect | 24.80 → 36.15 (0.69×) | 68 → 42.8 (1.6×) | 178 → 52 |
| 10 MiB | resume | 26.17 → 40.58 (0.64×) | 71 → 47.7 (1.5×) | 180 → 71 |
| 10 MiB | validate | 29.07 → 35.51 (0.82×) | 77 → 42.9 (1.8×) | 226 → 52 |

Observations (not targets):

- Go wins every cold measurement, because there is no runtime or module start
  (~5 ms against bun's ~28 ms), and it uses roughly 2–7× less memory at every size.
- Warm, Go is faster at 100 KiB. It is about even at 1 MiB, and 1.2–1.6×
  slower at 10 MiB. At 10 MiB the dominant costs are the kernel page faults
  for the 10–30 MB buffers (`runtime.madvise`) and the single parse of the
  10 MiB JSON. JavaScriptCore's JSON parser and warmed heap are strong on this
  workload.
- `resume`, `inspect` and `validate` still cost O(plan) in both engines:
  every call reads, hashes and decodes the whole artifact set (spec 03 §3,
  reuse and caching are stage F).
- **First-implementation numbers.** The 10 MiB warm medians before the stage B
  allocation fixes were: read 122.9, inspect 68.7, resume 72.3,
  validate 71.2 ms. The fixes were a pre-sized file read, no per-object maps,
  lazy field paths, zero-copy strings over the immutable artifact buffer and
  slot-backed optional fields. They cut allocations about 7×. The before/after
  profiles were taken on the same fixtures.

The reference-table gaps listed above remain open for Go as well. Go also has
no `update`, checkpoint or compaction numbers yet.

## 2026-10-01 writes and end-to-end

Measurement only. No product code changed. Raw data (statistics only, no
fixture content):
[`testdata/perf/writes-e2e-results.json`](../testdata/perf/writes-e2e-results.json).

| Item | Value |
| --- | --- |
| Machine / OS | Apple M4 Pro (8P+4E), 24 GiB, macOS 27.0.1 (26A434), local APFS, AC power. The owner's own OpenCode was running at the same time, so expect some noise |
| Shiori | `f1f0ad7` (clean tree), `CGO_ENABLED=0 go build -trimpath` |
| Toolchains | Go 1.27.1, bun 1.4.0, OpenCode 2.0.20 |
| TS engine | The stage A black-box oracle (`<reference-checkout>/src/custom-tools/workplan.ts`), run in-process by a bun harness |
| TS plugin | Read-only scratch copy of `packages/workplan-tools` with absolute dependency links (as in the stage D parity run) |

### Fixtures

| Label | Plan JSON | Plan MD | Checkpoint | Source |
| --- | --- | --- | --- | --- |
| 66 KB | 65,939 | 115,721 | 3,621 | `cp -Rp` of the `real-plan-a` scratch copy |
| 257 KB | 257,151 | 203,367 | 2,718 | `cp -Rp` of the `real-plan-b` scratch copy |
| 1 MiB | 1,048,711 | 860,239 | none | `perf-1m` (sha256 as above) |
| 10 MiB | 10,486,061 | 8,648,334 | none | `perf-10m` (sha256 as above) |

### Method

- **Operations.** All inputs are constant per fixture and are derived from the
  pristine plan.
  - `update status`: the first `draft` step is set to `in_progress`.
  - `update note`: `appendNotes: ["bench note"]`.
  - `patch`: one unique Markdown line gets a suffix (a 1-line patch).
  - `checkpoint`: summary, next action, phase and step, sent as orchestrator.
  - `compact preview`: `archiveReason` only. TS uses `workplan_compact` in
    preview mode; Shiori uses `workplan_compact_preview` (serve) and
    `compact --reason` (CLI).
- **Expected hash and restore.** Every write passes `expectedHash` (the
  pristine `stateHash`) to both engines. The workplan directory is restored to
  the pristine bytes before each iteration, outside the timed region.
- **TS engine.** Runs in-process, and `ask` resolves at once.
  - Warm: 2 unrecorded warm-up calls, then 30 recorded (20 for 10 MiB).
  - Cold first call: 10 fresh processes (5 for 10 MiB).
- **Shiori CLI.** Each call is a new process:
  `--input … --expected-hash H --yes --json`. There are 2 warm-up calls, then
  30 recorded (20 for 10 MiB).
- **Shiori serve.** One `serve --stdio` child is driven through the adapter's
  own `CoreClient` (tool request → `prepared` → `shiori.commit`) with no host.
  - Warm: 2 + 30 (20 for 10 MiB).
  - Cold: 10 new children (5 for 10 MiB). Spawn plus handshake is reported
    separately from the first request.
- **Bytes and syncs.** A scratch DYLD interposer counted `write`/`pwrite`/`writev`
  (including the `$NOCANCEL` variants), `fsync`, `fcntl(F_FULLFSYNC)`,
  renames, links and unlinks under the workplan directory. It ran in one
  untimed call per engine × fixture × operation. TS was measured this way
  through an ad-hoc-signed scratch copy of bun, so the TS source was never read.
- **End to end.** One private `opencode serve --service` per engine × fixture:
  - isolated HOME, XDG_* and TMPDIR under scratch, its own loopback port;
  - a config that registers only the plugin under test and the runtime-smoke
    relay harness;
  - no model calls.

  The client opens one persistent event subscription, which replies `once` to
  each `permission.asked` as soon as it arrives. Each tool gets 2 warm-up calls,
  then 20 recorded, timed from the RPC `run` call to its result. "First" is the
  first tool call on a fresh server, a single sample. For Shiori it includes the
  child spawn and handshake.
  - "Ask" is the time from the call to receipt of `permission.asked`.
  - "RTT" is the time for the reply POST.
  - "After" is the time from the reply to the result (the commit).

  Only the processes this run started were stopped. The owner's server on port
  4096 was not touched.

### 1. Core writes, engine vs engine (ms, median / p95)

| Size | Op | TS warm | TS cold first call | Shiori serve warm (prepare + commit) | Shiori serve cold first request (+ spawn/handshake) | Shiori CLI process |
| --- | --- | --- | --- | --- | --- | --- |
| 66 KB | update status | 11.8 / 15.0 | 22.7 / 24.6 | 29.0 / 32.0 (1.4 + 27.6) | 30.8 (+11.3) | 32.4 / 35.6 |
| 66 KB | update note | 12.9 / 15.0 | 22.8 / 24.1 | 29.8 / 32.3 (1.4 + 28.6) | 30.0 (+11.3) | 33.7 / 36.4 |
| 66 KB | patch | 13.1 / 15.4 | 22.3 / 23.1 | 31.4 / 34.8 (1.2 + 30.2) | 30.8 (+11.5) | 32.8 / 35.7 |
| 66 KB | checkpoint | 11.2 / 12.7 | 21.2 / 21.9 | 30.8 / 34.4 (1.1 + 29.7) | 28.6 (+11.5) | 32.7 / 34.6 |
| 66 KB | compact preview | 3.2 / 3.9 | 9.2 / 9.6 | 1.5 / 1.8 | 2.1 (+11.0) | 4.9 / 5.1 |
| 257 KB | update status | 11.5 / 12.7 | 23.4 / 24.3 | 32.9 / 36.0 (2.8 + 30.2) | 33.9 (+11.5) | 36.4 / 38.6 |
| 257 KB | update note | 11.5 / 12.9 | 23.3 / 23.9 | 32.6 / 39.3 (2.7 + 29.9) | 34.6 (+11.3) | 36.2 / 38.1 |
| 257 KB | patch | 11.2 / 12.5 | 20.7 / 21.4 | 32.6 / 35.0 (1.8 + 30.8) | 31.6 (+11.6) | 36.4 / 38.5 |
| 257 KB | checkpoint | 9.4 / 10.8 | 19.7 / 20.4 | 30.8 / 33.4 (1.3 + 29.2) | 30.0 (+11.6) | 33.5 / 35.6 |
| 257 KB | compact preview | 2.7 / 3.6 | 8.9 / 9.1 | 3.3 / 3.6 | 3.8 (+10.9) | 7.0 / 7.3 |
| 1 MiB | update status | 25.9 / 29.3 | 44.1 / 45.0 | 59.5 / 62.6 (13.7 + 45.6) | 59.6 (+10.8) | 63.0 / 65.8 |
| 1 MiB | update note | 26.1 / 30.8 | 44.1 / 45.0 | 60.6 / 65.6 (13.3 + 47.2) | 59.5 (+11.9) | 61.7 / 66.9 |
| 1 MiB | patch | 19.1 / 20.9 | 33.7 / 35.1 | 43.4 / 45.8 (6.5 + 37.2) | 45.1 (+11.6) | 45.8 / 48.9 |
| 1 MiB | checkpoint | 15.7 / 16.7 | 28.4 / 29.3 | 36.3 / 40.8 (4.3 + 31.9) | 37.8 (+11.2) | 40.2 / 43.6 |
| 1 MiB | compact preview | 6.0 / 7.3 | 14.6 / 15.1 | 15.0 / 15.7 | 16.2 (+11.5) | 19.2 / 19.8 |
| 10 MiB | update status | 159.4 / 169.4 | 198.5 / 207.5 | 297.6 / 338.7 (118.8 + 179.0) | 298.1 (+12.2) | 296.7 / 305.2 |
| 10 MiB | update note | 172.4 / 225.1 | 185.7 / 197.9 | 288.8 / 299.9 (117.2 + 171.1) | 292.5 (+11.6) | 286.6 / 308.1 |
| 10 MiB | patch | 100.3 / 106.1 | 128.3 / 129.6 | 168.8 / 178.6 (54.5 + 114.6) | 175.4 (+13.0) | 174.2 / 203.3 |
| 10 MiB | checkpoint | 75.5 / 80.6 | 99.0 / 99.3 | 110.7 / 132.5 (35.2 + 75.7) | 108.9 (+12.3) | 112.2 / 117.1 |
| 10 MiB | compact preview | 41.3 / 47.9 | 56.7 / 57.5 | 136.2 / 138.3 | 137.2 (+12.5) | 141.8 / 144.7 |

The TS cold process wall time, including bun start and import, was 47–50 ms at
66–257 KB, 57–73 ms at 1 MiB and 137–241 ms at 10 MiB.

**Bytes written per operation** (logical `write` bytes; journal / staged / lock):

| Size | Op | TS | Shiori | TS syncs | Shiori syncs |
| --- | --- | --- | --- | --- | --- |
| 66 KB | update | 243,050 (176,634 / 66,141 / 275) | 243,049 (176,626 / 66,137 / 286) | 7 `fsync` | 7 `F_FULLFSYNC` |
| 66 KB | patch | 425,140 (309,131 / 115,734 / 275) | 425,147 (309,127 / 115,734 / 286) | 7 `fsync` | 7 `F_FULLFSYNC` |
| 66 KB | checkpoint | 9,158 | 9,157 | 7 `fsync` | 7 `F_FULLFSYNC` |
| 257 KB | update | 943,756 (686,308 / 257,173 / 275) | 943,751 (686,296 / 257,169 / 286) | 7 `fsync` | 7 `F_FULLFSYNC` |
| 257 KB | patch | 746,524 | 746,531 | 7 `fsync` | 7 `F_FULLFSYNC` |
| 1 MiB | update | 7,000,658 (5,091,402 / 1,908,981 / 275) | 7,001,268 (5,091,742 / 1,909,240 / 286) | 9 `fsync` | 8 `F_FULLFSYNC` |
| 1 MiB | patch | 3,155,026 | 3,155,033 | 7 `fsync` | 7 `F_FULLFSYNC` |
| 10 MiB | update | 70,160,619 (51,025,918 / 19,134,426 / 275) | 70,166,689 (51,029,378 / 19,137,025 / 286) | 9 `fsync` | 8 `F_FULLFSYNC` |
| 10 MiB | patch | 31,711,373 | 31,711,380 | 7 `fsync` | 7 `F_FULLFSYNC` |

The two engines follow the same sequence: two lock files, same-directory
staging, a v1 journal holding before and after bytes as base64, rename, and
journal removal. They write the same bytes to within 0.01%. The journal is
about 73% of the bytes written, and total bytes are about 3.7× the changed
artifacts. The difference is the sync primitive:

- Go's `File.Sync` on darwin is `F_FULLFSYNC`. It flushes the drive cache.
- The TS engine's `fsync` returns before the data leaves the drive cache.

Microbenchmark on this disk (scratch directory):

| Primitive | Time |
| --- | --- |
| `fsync`, 66 KB file | 0.04 ms |
| `F_FULLFSYNC`, 66 KB file | 2.7 ms |
| `F_BARRIERFSYNC` | 0.41 ms |
| `F_FULLFSYNC`, directory | 2.8 ms |
| `F_FULLFSYNC`, 19 MB file | 29.6 ms |
| `F_FULLFSYNC`, 51 MB file | 60.9 ms |

### 2. End to end through OpenCode (ms; private servers)

The host RPC floor, a relay-harness call with no tool, was 0.4–0.5 ms median on
every server.

| Size | Tool | Shiori first | Shiori warm median / p95 | TS first | TS warm median / p95 | Shiori ask / RTT / after reply | TS ask / RTT / after reply |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 66 KB | read | 31.2 | 5.4 / 13.6 | 11.6 | 7.6 / 9.2 | – | – |
| 66 KB | resume | 7.4 | 5.5 / 5.9 | 8.5 | 5.5 / 10.5 | – | – |
| 66 KB | inspect | 2.2 | 1.7 / 2.0 | 5.8 | 4.5 / 5.1 | – | – |
| 66 KB | validate | 1.8 | 1.6 / 1.9 | 8.0 | 4.3 / 4.7 | – | – |
| 66 KB | update status | 44.6 | 39.0 / 41.2 | 34.7 | 21.4 / 27.2 | 7.5 / 0.7 / 30.3 | 8.8 / 0.9 / 11.8 |
| 66 KB | update note | 39.0 | 37.5 / 39.6 | 27.0 | 20.3 / 24.3 | 6.4 / 0.6 / 30.1 | 8.2 / 0.7 / 11.5 |
| 66 KB | patch | 38.7 | 38.4 / 41.4 | 20.9 | 20.3 / 27.5 | 6.1 / 0.6 / 31.5 | 7.8 / 0.7 / 11.5 |
| 66 KB | checkpoint | 41.4 | 36.8 / 38.7 | 19.8 | 20.0 / 32.9 | 5.7 / 0.5 / 30.4 | 7.7 / 0.6 / 11.8 |
| 257 KB | read | 28.6 | 11.0 / 13.1 | 14.5 | 10.4 / 15.6 | – | – |
| 257 KB | resume | 7.5 | 6.9 / 8.3 | 8.1 | 4.5 / 5.0 | – | – |
| 257 KB | inspect | 2.9 | 2.6 / 2.8 | 4.6 | 3.6 / 4.1 | – | – |
| 257 KB | validate | 2.7 | 2.8 / 3.0 | 4.3 | 3.6 / 4.3 | – | – |
| 257 KB | update status | 54.3 | 42.4 / 45.8 | 41.5 | 21.8 / 25.5 | 8.4 / 0.8 / 33.5 | 8.9 / 0.8 / 11.8 |
| 257 KB | update note | 43.3 | 41.6 / 47.7 | 31.7 | 20.4 / 34.5 | 8.2 / 0.7 / 31.8 | 8.2 / 0.7 / 11.2 |
| 257 KB | patch | 39.8 | 43.2 / 51.6 | 30.5 | 18.3 / 22.9 | 7.2 / 0.7 / 35.7 | 7.2 / 0.6 / 10.6 |
| 257 KB | checkpoint | 37.8 | 41.9 / 67.6 | 18.0 | 18.7 / 26.6 | 6.6 / 0.6 / 34.0 | 7.0 / 0.6 / 10.5 |
| 1 MiB | read | 177.4 | 29.3 / 32.4 | 30.5 | 25.6 / 30.9 | – | – |
| 1 MiB | resume | 8.0 | 6.2 / 6.6 | 9.3 | 6.2 / 10.1 | – | – |
| 1 MiB | inspect | 5.4 | 5.4 / 5.7 | 8.3 | 5.7 / 9.1 | – | – |
| 1 MiB | validate | 7.4 | 7.5 / 8.1 | 7.8 | 6.0 / 10.0 | – | – |
| 1 MiB | update status | 75.3 | 72.5 / 79.4 | 67.7 | 44.4 / 106.2 | 19.4 / 0.7 / 52.2 | 16.1 / 0.8 / 27.2 |
| 1 MiB | update note | 71.0 | 69.5 / 75.1 | 44.4 | 39.0 / 52.7 | 18.6 / 0.6 / 50.5 | 14.7 / 0.6 / 24.1 |
| 1 MiB | patch | 55.5 | 61.7 / 81.2 | 26.8 | 28.1 / 30.3 | 11.2 / 0.5 / 49.9 | 9.1 / 0.6 / 16.1 |
| 1 MiB | checkpoint | 54.7 | 49.7 / 62.7 | 33.5 | 23.6 / 31.8 | 9.9 / 0.7 / 39.2 | 9.3 / 0.5 / 13.8 |
| 10 MiB | read | 677.5 | 491.6 / 515.5 | 267.0 | 214.5 / 348.0 | – | – |
| 10 MiB | resume | 46.7 | 46.1 / 47.5 | 38.0 | 30.1 / 39.9 | – | – |
| 10 MiB | inspect | 39.7 | 38.4 / 39.7 | 31.8 | 28.6 / 39.5 | – | – |
| 10 MiB | validate | 58.1 | 59.5 / 61.3 | 35.7 | 33.1 / 40.5 | – | – |
| 10 MiB | update status | 341.6 | 308.8 / 325.4 | 270.8 | 226.4 / 247.3 | 128.8 / 0.7 / 178.6 | 88.1 / 0.8 / 138.9 |
| 10 MiB | update note | 307.1 | 314.7 / 362.6 | 240.4 | 233.6 / 272.9 | 127.0 / 0.7 / 183.3 | 96.3 / 0.7 / 138.9 |
| 10 MiB | patch | 171.5 | 175.8 / 192.4 | 129.4 | 138.1 / 151.5 | 61.7 / 0.7 / 113.6 | 39.8 / 0.8 / 95.0 |
| 10 MiB | checkpoint | 114.0 | 115.1 / 119.5 | 94.0 | 101.8 / 128.7 | 41.6 / 0.7 / 72.0 | 36.6 / 0.7 / 60.8 |

Every write raised exactly one `permission.asked`, for both plugins.

Plugin readiness is the time until the relay harness saw the native tools,
counted from 300 ms after the server first answered. It was 275–340 ms for
Shiori and 590–620 ms for the TS plugin.

**Shiori 10 MiB read split.** On its own, `serve --stdio` `read` through
`CoreClient` took 330 ms warm, against 98 ms for the whole CLI process. At
1 MiB the figures are 23.8 ms and 16.6 ms. So about 230 ms of the 10 MiB
figure is spent moving and decoding the 30 M-char response frame on the
adapter side. The likely cause is the adapter's per-chunk
`Buffer.concat` / `indexOf` accumulation plus the `JSON.parse` of the frame.
This was not profiled.

### 3. Summary

**Writes are about 2× slower in Shiori, almost entirely because of
durability.** At the real plan sizes (66–257 KB):

- A Shiori commit is 28–31 ms. It performs 7 `F_FULLFSYNC` calls at
  about 3–4 ms each.
- The whole TS write is 9–13 ms, with 7 plain `fsync` calls totalling under
  1 ms.

Prepare work is 1–3 ms in Shiori, the same scale as the TS CPU work. The bytes
written are identical, so the byte count is not the cause. The TS engine's
guarantee is weaker on macOS: its `fsync` does not flush the drive cache.
Shiori's extra ~20 ms buys durability against power loss.

The remaining costs:

- **Process hop.** A new CLI process adds about 3–4 ms. A new serve child adds
  about 11 ms for spawn and handshake, once per plugin lifetime. End to end,
  Shiori's first `read` is 29–31 ms at the real-plan sizes (66 KB, 257 KB), against 5–11 ms warm.
  One 1 MiB first read took 177 ms (a single sample).
- **Permission bridge.** Its cost matches the TS plugin's. The reply RTT is
  0.5–0.9 ms. Tool call to `permission.asked` takes 6–8 ms at the real-plan sizes
  for both plugins (TS 7–9 ms), which includes Shiori's prepare frame. Host
  plus adapter overhead over the engine is about 8–10 ms per write for both
  plugins.

Where Shiori is faster:

- Reads at the real sizes, end to end: inspect 1.7 against 4.5 ms and
  validate 1.6 against 4.3 ms at 66 KB. Resume and read are about even.
- Compact preview up to 257 KB.
- Plugin readiness (about 0.3 s faster).
- Every cold path (Go start against bun import).

Where Shiori is slower:

- All writes, by durability.
- At 1–10 MiB, prepare CPU work. The 10 MiB update prepare is 118 ms, against
  159 ms for the whole TS update.
- Compact preview at 1 MiB (15 against 6 ms) and 10 MiB (136 against 41 ms).
- The 10 MiB end-to-end read and validate, mostly transport.

**Relative to model latency, none of this matters at real plan sizes.** A write
costs about 40 ms end to end, against about 20 ms for TS. A model turn is
seconds, and a human answering a permission prompt is seconds more. The worst
case is the 10 MiB update: about 310 ms, 80 ms more than TS. That is still
below 5% of a typical turn. Plans of that size also produce 30 M-char reads that
could never enter a model context.

**Extensions.**

- **X8 (warm cache): not justified yet.** Warm reads at the real sizes are
  1.6–11 ms end to end, and the host RPC plus relay dominate them. X8 would help
  prepare and validate from 1 MiB up, but writes must still reread and rehash
  under the lock. Revisit only if real plans reach about 1 MiB.
- **X9 (derived index): not justified.** Nothing at the real sizes costs more
  than about 3 ms in the engine.
- **X10 (slice parsing): not justified.** The reason is the same as for X9.
  Filtered reads are already bounded by the host floor.
- **P4 (journal v2): not justified for latency at the real sizes.** At
  66–257 KB the commit cost is the number of syncs, not bytes: a full flush of
  1 MB costs about 3.7 ms, against 2.7 ms for 66 KB. At 10 MiB P4 would remove
  about 51 MB of journal and about 60 ms of the 179 ms commit, which is real but
  only for synthetic sizes. P4 also adds a new artifact version and a fault
  matrix.

**What the numbers do point at** (outside X8–X10/P4, owner decisions):

1. **The sync policy.** Examples: `F_FULLFSYNC` only at the durability
   points (journal, then publication). Lock files and some directory syncs
   could use plain `fsync` or `F_BARRIERFSYNC` (0.4 ms). This could remove
   10–15 ms per write without giving up power-loss safety of the journal
   protocol. It needs a crash-matrix review before any change.
2. **The adapter's large-frame decode path.** About 230 ms at 30 M chars, and
   a cheap fix. It matters only for oversized `read` outputs.
