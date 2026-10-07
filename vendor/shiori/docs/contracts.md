# Frozen contract v1 (stage A)

Status: **APPROVED 2026-09-30** by the owner (section 9 records the approval).
Everything in this document is a stage A output: the reviewed schemas, the
golden corpus, and a resolution for each open item in
[05 §6](specs/05-migration-and-acceptance.md). Items marked **APPROVED
2026-09-30** were proposals at stage A and are now binding. Items marked
**OBSERVED** describe what the reference implementation actually does, as
recorded in `testdata/`. Where OBSERVED and the specifications disagree,
section 7 lists the difference and the approved resolution. Section 10 records
stage B findings (reference behaviour the corpus pins down that stage A did not
spell out). Sections 11–18 record the approved D.1, D.2, D.3, D.3.1, D.4,
D.4.1, D.4.2 and D.4.3 design changes, which deliberately depart from the
reference; section 19 the host version policy, section 20 the E1
evidence ledger, section 21 journal v2, section 22 worktree lanes,
section 23 the MCP server, section 24 the change log, section 25
the verify and report commands and section 26 plan links, templates and
quality checks.

## 1. Sources of truth

| Artifact | Location | Role |
| --- | --- | --- |
| Machine schemas | [`schema/`](../schema/index.json) (JSON Schema 2020-12) | Shape contract for stored artifacts, tool inputs and protocol frames |
| Golden corpus | [`testdata/`](../testdata/MANIFEST.json) | Behavioural contract: the fixtures plus the outputs the reference produced for them |
| This document | `docs/contracts.md` | Policies the corpus cannot express, and the stage B–D decisions |
| Specifications | `docs/specs/01..05` | Requirements. Where they conflict with this document, section 7 applies until the owner decides |

The reference implementation (the "oracle") is the local TypeScript workplan
engine that `testdata/MANIFEST.json` fingerprints: the sha256 of each source
file, bun 1.4.0, zod 4.1.8, macOS 27.0.1 arm64. It was only **executed**. No
reference source was copied, ported or paraphrased into this repository. The
harness scripts that drove it are kept outside the repository, and only their
sha256 fingerprints are recorded.

## 2. Schema layout (APPROVED 2026-09-30)

- The dialect is 2020-12. `$id` values use `https://shiori.invalid/schema/v1/…`.
  They are identifiers only and are never fetched. Relative `$ref`s resolve
  within the directory.
- `schema/v1/common.schema.json` holds shared definitions: hash, status,
  severity, `nonblank` (trimmed with the ECMAScript whitespace set), `workplanId`,
  the UTC datetime, the manifest entry and the composite step reference.
- Stored artifacts:
  - `plan-v2`
  - `checkpoint-v1`, `checkpoint-v2` and the union `checkpoint`
  - `dependencies-v1`
  - `transaction-journal-v1`
  - `lock-owner-v1`
  - `cursors-v1`
- Protocol: `protocol-envelope-v1` (section 5.4).
- Tools: `schema/v1/tools/<tool>.input.schema.json` for all 13 native identities,
  including `workplan_compact_preview`. These are the **native** surface: they
  have no `workspaceRoot` and are strict at the top level. The standalone core/CLI
  surface also accepts `workspaceRoot`, and keeps `expectedHash` optional for
  legacy direct calls.
- Rules that JSON Schema cannot express are listed under `x-shiori-*` keywords,
  documented in `schema/index.json`. Each one carries the exact reference message
  and field path. Where a native cross-field rule *can* be expressed (required
  `expectedHash`, recovery exclusivity, apply confirmation), it is also encoded as
  `allOf`/`if-then`. With those encodings, the schemas give the same accept/reject
  result as the oracle's native parser on all 55 input-validation vectors.
- The storage schemas accept every valid fixture and reject every invalid one
  (checked with a 2020-12 validator; see MANIFEST harness fingerprints).

**Semantic validation remains mandatory.** Schema validity does not cover id
normalization, dependency existence or cycles, artifact scope, hash/token
binding, or structure rules (`x-shiori-structure-rules`).

## 3. Hash contract (OBSERVED, frozen)

Covered by the vectors in `testdata/vectors/hash/`: 11 synthetic manifests, a
snapshot of every fixture plan, and an invalidation table.

```text
planHash  = SHA256("workplan-plan-v1\n"  + M(planEntries)  + "\n")
stateHash = SHA256("workplan-state-v1\n" + M(stateEntries) + "\n")
M(entries) = compact JSON array of entries sorted by path
entry = {"path":P,"sha256":H} | {"path":P,"missing":true}   (key order exactly so)
```

- **Membership.** The plan manifest contains the primary JSON, the linked
  Markdown and every linked spec. The state manifest contains the plan entries
  plus `<id>.checkpoint.json`, `<id>.dependencies.json` and
  `<id>.transaction.json`, whether each is present or missing. Lock, stage, temp,
  unrelated and archive files never contribute. A change to mtime only does not
  change either hash (`hash/invalidation`).
- **Ordering.** The sort key is the JavaScript comparison of UTF-16 code units.
  It is neither byte order nor code-point order. For example, `docs/𝒜.md`
  (surrogate pair D835) sorts **before** `docs/Ａ.md` (FF21) and `docs/.md`
  (E000). Go must compare `utf16.Encode([]rune(p))` or use an equivalent
  comparator. It must not use `sort.Strings`.
- **Escaping.** `M` uses ECMAScript `JSON.stringify` string escaping:
  - `"` and `\` are escaped.
  - U+0008, U+0009, U+000A, U+000C and U+000D are escaped as `\b \t \n \f \r`.
  - Every other code point below U+0020 is escaped as `\u00xx`, lowercase hex.
  - Everything else is emitted raw as UTF-8, including `< > &`, U+007F,
    U+2028/U+2029 and non-BMP characters.

  Go's `encoding/json` default escaping (`<>&`, U+2028/9) would break the hash.
  The proposed encoder is Go 1.27's `encoding/json/jsontext` string quoting,
  which was checked locally to leave `<>&`, U+2028 and U+007F raw. The vectors
  (`canonicalManifestJsonUtf8Hex`) remain the arbiter.
- **Paths.** Paths are project-relative and use `/` separators.
  **OBSERVED:** a backslash inside a POSIX filename is also rewritten to `/`
  (section 7, D5).
- The hash helpers hash whatever key order they are given. Only the canonical
  entry order above is contractual.
- A pending journal's bytes are part of `stateHash`, and a missing primary JSON
  is recorded as a missing entry.

Cursor checksums use the same style of preimage:
`SHA256("<domain>\n" + compact JSON)`, with the key order given in
`cursors-v1.schema.json`. The token is unpadded base64url.

## 4. Other frozen formats (OBSERVED)

- **Generated Markdown** must be byte-exact.
  - `testdata/vectors/markdown/` holds 11 renderings and 2 error cases.
  - `markdown/generated-classification` records which fixture Markdown counts as
    "generated": stored Markdown that byte-equals the rendering of the stored
    JSON.
  - The renderer treats absent `specFiles` as `[]`. The reference renderer
    throws on a raw legacy document; the tools only avoid this because the
    snapshot normalizes the document first.
- **Stored JSON** is `JSON.stringify(doc, null, 2) + "\n"`: pretty-printed with
  2 spaces, the same escaping as in section 3, and a single trailing newline.
- **Resume budget unit.** `maxChars` counts **UTF-16 code units** of the complete
  output text (JavaScript `string.length`). Go must count
  `len(utf16.Encode([]rune(s)))`, not bytes and not runes.
  - Every resume vector records `rawOutputLength` in this unit.
  - All 111 successful resume outputs (single-call and paged) fit their budget.
    The other 34 resume vectors are expected errors (invalid fixtures, cursor
    misuse).
  - The minimum-budget stress case produced 4025 of 4096 code units.
- **Datetimes.** The UTC form `YYYY-MM-DDTHH:MM[:SS[.frac]]Z` is accepted.
  Offsets are rejected and seconds are optional
  (`validation/structure/datetime-*`).
- **Journal content** fields hold exact bytes as padded standard base64. `mode`
  is the POSIX permission bits.

## 5. Resolutions for 05 §6 ("choose/freeze before implementation")

### 5.1 Schema and binding tooling: APPROVED 2026-09-30

1. The JSON Schema files in `schema/` are the single source of truth. There is
   no code generator in stages B–D. Go types are hand-written against the
   schemas, and a Go test loads every schema and every fixture/vector to prove
   agreement. Candidate validator for tests only:
   `github.com/santhosh-tekuri/jsonschema` (version pinned at stage B). It is a
   test dependency and is never linked into the shipped binary.
2. Decoding uses `encoding/json/jsontext` (standard library, Go 1.27, no
   experiment flag).
   - It rejects duplicate member names by default, which is required for
     protocol frames and native input.
   - It gives token-level access for preserving unknown members and number
     spellings.
   - `encoding/json` v1 struct decoding is not used for artifacts, because it is
     case-insensitive and silently accepts duplicates.
3. The adapter reads the tool schema files at build time. The adapter build
   copies them verbatim into its single file. It registers them unchanged with
   OpenCode, which is how it avoids hand-duplicated property maps. Input
   validation stays in Go, and its error text is returned unchanged.
4. Schema changes bump the directory version (`schema/v2/…`). Version v1 is
   frozen once the owner approves it.

### 5.2 Limits: APPROVED 2026-09-30 (configurable, benchmark-revisited in stage F)

| Limit | Default | Notes |
| --- | --- | --- |
| Request frame | 16 MiB | Rejected before decoding into values |
| Response frame | 64 MiB | See D9. `workplan_read` output for a 10 MiB plan measured 30.1 M chars |
| Single artifact read | 64 MiB | Opt-in raise via CLI flag or config only, never via model input |
| Whole snapshot (JSON + MD + specs + sidecars) | 256 MiB | Fails closed as `unsupported_capability` |
| Linked spec files per plan | 1024 | |
| JSON nesting | 128 | Applies to frames and artifacts |
| Cursor token | 4096 bytes | Larger tokens fail with the reference "Invalid … cursor" text |
| Resume `maxChars` | 4096–64000, default 12000 | Unchanged |
| Resume / doctor / inspect `limit` | 1–100 default 20 / 1–100 default 50 / 1–500 default 100 | Unchanged |
| Lock wait / abandonment grace | 5 s / 5 min | Unchanged; age alone never reclaims |

### 5.3 Packaging and distribution: APPROVED 2026-09-30

- **Core.** A single static Go binary named `shiori`, built with `CGO_ENABLED=0`,
  `-trimpath`, reproducible flags, and the Go module
  `github.com/hoshinoht/shiori`. It has no runtime dependencies: no Bun, Node or
  libc requirement on Linux.
- **Adapter.** One dependency-free ES module (`adapter/shiori-opencode.js`, built
  from TS). It is npm-free: no `package.json` dependencies and no
  `node_modules`. It imports only OpenCode's plugin API and `node:` built-ins.
  - It locates the binary from an explicit plugin option or `SHIORI_BIN`. It
    never searches `PATH` implicitly, and never downloads or installs anything.
  - It refuses to start if the handshake's `contractVersion` or
    `protocolVersion` is unsupported.
- **Distribution.** Stages B–D use local build only (`go build`). Release
  artifacts (GitHub release, per-platform binaries plus `SHA256SUMS`) belong to
  stage E, and are neither published nor automated before that.

### 5.4 Process lifecycle: APPROVED 2026-09-30

- The adapter lazily spawns **one** `shiori serve --stdio` child per plugin
  instance on the first tool call. There is no daemon shared across projects or
  hosts, and no network listener.
- The first frame is `shiori.handshake`. Requests are multiplexed by
  `requestId`.
- A tool request that would mutate returns `prepared` and touches nothing. After
  host approval, `shiori.commit` commits exactly that intent (spec 02 §3).
- An `AbortSignal` sends a `cancel` frame. A late approval after cancellation is
  refused.
- **Child crash or transport loss.** All in-flight requests fail with
  `cancelled` or `outcome_uncertain`, plus the journal pointer when relevant.
  Nothing is replayed. The next call respawns the child and repeats the
  handshake, and prior approvals do not carry over.
- **Idle exit.** After 10 minutes with no request, the child exits.
- **Host unload.**
  1. Cancel in-flight requests.
  2. Close stdin.
  3. Wait 2 s, then send SIGTERM to the owned PID only.
  4. Wait 2 s more, then send SIGKILL.
  5. Keep any journal.
- **One-shot CLI.** `shiori <operation> …` runs the same engine in-process, with
  no child and no protocol.

### 5.5 Standalone CLI mutation confirmation: APPROVED 2026-09-30

The CLI acts under OS/local operator authority, not OpenCode policy (spec 02 §7).

- Read operations never prompt.
- Every mutation first prints the prepared intent: operation, exact resources,
  and before/after hashes.
  - On a TTY it requires typing `yes`.
  - Off a TTY it requires `--yes`.
- `--yes` cannot come from an environment variable or config file, and never
  bypasses stale-hash, lock, journal or scope checks.
- Existing-state writes require `--expected-hash` (native parity) unless
  `--legacy-unhashed` is given. That flag still re-checks state under the lock.
- `compact --apply` additionally requires `--preview-token` and
  `--confirm ARCHIVE_SELECTED_HISTORY`.
- Recovery takes `--recovery resume|rollback` and `--expected-hash`, and
  excludes every ordinary update flag.
- `--json` emits the protocol result object on stdout, and nothing else.

### 5.6 Platform and host matrix: APPROVED 2026-09-30

| Target | Read ops | Writes | Evidence required before claiming |
| --- | --- | --- | --- |
| darwin/arm64 (APFS, local) | yes | yes | Stage C fault, race and recovery suites on macOS |
| linux/amd64 (ext4/xfs, local) | yes | yes | Same suites on Linux; directory fsync verified |
| darwin/amd64, linux/arm64 | build-only | no | Unsupported until run through the gates |
| Windows, network/FUSE filesystems | no | no | Separate atomicity/locking design (spec 01 §6) |

- The minimum host is OpenCode runtime 2.0.19 with plugin/client 2.0.20. This is
  the verified baseline. Other versions failed closed at registration until
  D.3; since D.3 (§13 item 7) they keep the read-only tools and refuse every
  mutating tool. Since §19, `hostPolicy` decides which unverified versions
  may still write, and `bun run verify-host` extends the verified list.
- Handshake `durability` reports observed capabilities, never intent.
- On a system that is not a supported write target, the handshake reports
  `writeSupported: false`, and mutations fail with `unsupported_capability`.

## 6. Unknown-field preservation and error text

### 6.1 Unknown fields: APPROVED 2026-09-30 policy, with OBSERVED reference behaviour

**Stored plan JSON (document, phase, step, finding).** Unknown members must
survive unrelated writes with their values and nesting intact. The reference
does this, but lossily (`mutations/update-legacy-preserve`):

- Known keys are re-emitted in schema order, with unknown keys after them, at
  every level.
- Duplicate keys collapse to the last value.
- Integers beyond 2^53 lose precision (`12345678901234567890` becomes
  `12345678901234567000`).
- `1.0` becomes `1`.

Proposal (see D4): Go preserves each unknown member's raw JSON bytes and its
number spelling, and keeps the reference's key order so ordinary documents stay
byte-identical. Duplicate keys in a stored plan are **diagnosed** instead of
collapsed: the plan stays readable and mutations fail closed with a field path.

**Sidecars** (checkpoint, dependencies, journal). Unknown members are accepted
and ignored on read, and are not carried forward, because sidecars are replaced
wholesale. This matches the reference.

**Native tool input.** Unknown keys are rejected at **every** level (spec 02
§7). The reference rejects them at top level only, and silently strips them in
nested phase/step/finding/patch objects (D2). The core/CLI surface follows the
same rule.

### 6.2 Error text policy: APPROVED 2026-09-30

1. The reference's own messages must match byte-for-byte after `$ROOT`
   substitution. These are the messages listed in the behavioural contract and in
   the corpus: patch grammar, plan-file policy, handwritten refusal, cursor,
   recovery, stale-hash and structure-rule texts.
2. Schema-library messages must also match byte-for-byte. The corpus contains
   this finite template set, and Go reproduces exactly this set with the same
   dot-joined paths:
   - `Invalid input: expected <T>, received <T>`
   - `Invalid input: expected <literal>`
   - `Invalid option: expected one of "a"|"b"`
   - `Invalid string: must match pattern /[a-z0-9]/i`
   - `Invalid string: must match pattern /^[a-f0-9]{64}$/`
   - `Too small: expected number to be >=N`
   - `Too small: expected string to have >=1 characters`
   - `Too big: expected number to be <=N`
   - `Unrecognized key: "k"`

   Multiple issues are joined with `"; "`, in schema property order, and wrapped
   as `Invalid <tool> input: …`.
3. JavaScript-engine text is **not** reproduced. Examples:
   `JSON Parse error: Expected '}'` and
   `Property name must be a string literal`. Go keeps the stable prefix
   (`Invalid workplan JSON at <path>: `,
   `Invalid workplan checkpoint JSON at <path>: `) and appends its own detail.
   Comparators match on the prefix only. This is a declared presentation
   difference.
4. Protocol errors add `class`, `issues[]`, `currentStateHash` and `retrieval`
   (see `protocol-envelope-v1`). The `message` field carries the text defined by
   rules 1–3.

## 7. Oracle behaviours that differ from the specifications

Each item cites the vectors that show it. "Proposal" is what stage B/C
would implement if approved.

| # | Observed reference behaviour | Spec / requirement | Proposal |
| --- | --- | --- | --- |
| D1 | **A journal left before the primary JSON exists cannot be recovered through any tool.** `read`, `validate`, `resume`, `checkpoint` and `update {recovery}` all fail with `Workplan file not found: $ROOT/.opencode/workplan/tx-new.json`. `create` prompts for permission and then fails with `Refusing to overwrite existing workplan artifact: …tx-new.transaction.json`. `doctor` lists the journal (`valid: true`, `targetCount: 2`), but gives **no stateHash**, only the issue `Primary workplan not found: tx-new`. (`mutations/update-recovery-precreate-resume`, `mutations/create-over-precreate-journal`, `tools/pending-journal-precreate/*`) | 01 §6 and S09 require a read-only interrupted-state hash and a recovery route | Go `doctor` (and `read` with `recoveryRequired`) returns `stateHash` computed with the primary recorded as missing. `update {recovery}` accepts a journal-only plan and validates the journal before authorization. New Go vectors are needed, because the oracle cannot produce them |
| D2 | Unknown keys in nested input objects are silently stripped (`validation/input/create--unknown-nested-phase-key`). Top-level unknown keys are rejected | 02 §7: unknown native input fields reject | Reject at every level with `Unrecognized key` at the nested path. Declared divergence |
| D3 | The registered tool JSON Schema is weaker than the validator. The id pattern loses the case-insensitive flag (`[a-z0-9]`, so `ABC` fails the registered schema but passes the validator: `create--id-uppercase-only`). Trimmed-nonempty becomes `minLength: 1`. Required `expectedHash`, recovery exclusivity and the apply rules are absent | 02 §7: schemas are the common contract | `schema/v1/tools` fixes all of these (`[A-Za-z0-9]`, `nonblank`, `allOf` conditions) |
| D4 | Unknown-metadata preservation is lossy: key reordering, duplicate collapse, loss of large-integer precision, and `1.0` rewritten as `1` (`mutations/update-legacy-preserve`) | 01 §3, C01: unknown metadata survives unrelated writes | Section 6.1. Divergence is limited to documents that the reference would already damage |
| D5 | A manifest path turns a literal `\` in a POSIX filename into `/`. `docs/quote"back\slash.md` is recorded as `docs/quote"back/slash.md` (`hash/fixtures/unicode--unicode-plan`), which could collide with a real `docs/quote"back/slash.md` | 01 §4/§5: manifest identity is the relative path | Keep the conversion for hash parity. Reject spec/plan paths containing `\` on POSIX with a field-path diagnostic, so no ambiguous manifest can be created. Existing plans with such paths stay readable, and Go reports an issue |
| D6 | `workplan_list` sorts with `localeCompare` (ICU, locale `en-US` here; the result depends on the locale). Sidecars come back in raw `readdir` order, which depends on the filesystem (`tools/list-mixed/list`) | 01 §5: no dependence on locale or map order | For canonical ids (`[a-z0-9-]`) UTF-16 order equals the observed order, checked on this corpus. Go sorts plans by UTF-16 code units and sidecars by name. The only visible difference is the position of non-canonical invalid entries (`Bad Name`, `UPPER`), which becomes a declared presentation difference; vectors compare invalid entries as a set |
| D7 | Any `update` of a plan whose phase/step id normalizes to empty fails with `Workplan id must contain at least one letter or number`, with no field path, even for unrelated fields. The generated-Markdown check renders markers before any other check (`mutations/update-duplicate-step-ids` on `invalid-structure`) | 01 §3: exact field paths, and drafts stay diagnosable | Go reports `phases.<i>.id: Must not be empty` (or the step path) and refuses the mutation. The message changes; the refusal is the same |
| D8 | Markdown markers are lossy for non-ASCII ids: `phase-𝒜-x` becomes `phase-x` and `step-été` becomes `step-t`, so distinct ids can share a marker (`tools/unicode/*--inspect`) | 03 §4: marker/heading index used for section retrieval | Keep the marker format (byte parity). The Go section index treats a marker collision as ambiguous and falls back to the heading plus ordinal, never "first match wins" |
| D9 | `workplan_read` output has no bound. For the 1 MiB plan it is 3.0 M chars; for 10 MiB it is 30.1 M chars (baseline) | 02 §2: bounded frames | Response frame 64 MiB (section 5.2). A larger read fails with `unsupported_capability` and a pointer to `includeMarkdown=false`, `inspect` or `resume`. Declared divergence only above the limit |
| D10 | The compact `previewToken` and `archivePath` depend on the absolute canonical root. The same preview at a different root path gives a different token (`mutations/compact-apply-valid`) | 01 §7 lists what the token binds; the root is not named | **Superseded at stage C (owner):** the token and archive name must not depend on the absolute root. Shiori's token binds workplan id, stateHash, reason, exact selection, removals digest and Markdown treatment (§10 stage C) |
| D11 | Engine- and library-specific message text leaks into diagnostics (the JSC JSON parser, zod) | 01 §3 exact paths; 02 §7 error classes | Section 6.2 |
| D12 | `reset {mode: "markdown-only"}` on already-generated Markdown asks for permission and "commits" identical bytes, so no file changes (`mutations/reset-markdown-only-generated`) | S03-adjacent: avoid needless authorization | Go returns an unchanged result without preparing an intent. Declared difference: zero prompts |

The remaining checks found no differences: hash algorithm, checkpoint
freshness classes (`missing`, `fresh`, `stale`, `legacy-unverified`, `invalid`),
cursor stale/tamper rejection, pre-authorization rejections with zero prompts,
handwritten-Markdown protection, and read-only operations leaving bytes and
mtimes unchanged (checked for every read vector).

## 8. Corpus layout and vector format

```text
testdata/
  MANIFEST.json            oracle fingerprints, runtime, normalization rules, per-file sha256
  fixtures/<name>/         a complete workspace root (.opencode/workplan/..., docs/...)
  vectors/hash/            manifest-hash cases, per-fixture snapshots, invalidation table
  vectors/markdown/        render inputs + <case>.expected.md bytes; generated classification
  vectors/validation/      structure/ (document -> issues) and input/ (tool input -> core/native parse)
  vectors/tools/<fixture>/ read, inspect, validate, doctor, list outputs (+ filtered/error cases)
  vectors/resume/<fixture>/ resume at maxChars 4096 / 12000 / 64000 (exact outputText)
  vectors/paging/          inspect and resume cursor chains, cursor misuse errors
  vectors/mutations/       pre-authorization rejections and frozen-clock successes (+ <case>.after/ bytes)
  expected/                documented differences: expectations.json (not part of the oracle corpus)
  perf/                    baseline fixtures (100 KiB raw; 1 MiB and 10 MiB as .tar.gz)
```

Every vector JSON has `id` and `fixture` (or an inline `input`), `call`,
`expect`, and `deterministic`. Read vectors also record
`readOnly.bytesAndMtimesUnchanged`. Mutation vectors record `changedFiles` and
`permissionPrompts`.

To run a vector, a Go test copies the fixture to a temporary root, invokes the
operation, and replaces the root with `$ROOT`. Budget-sensitive vectors (resume
and paging) must use a root of the same length as `generationRoot`, or check
invariants instead of bytes: the budget is met, IDs and hashes are present, and
the omission counts are truthful. Generated IDs, transaction IDs and stage-file
nonces are not contractual beyond their format.

Counts:

- 21 fixtures, 79 fixture files.
- 567 vector JSON files, 624 vector files including expected Markdown and
  after-bytes:
  - hash 31
  - markdown 14
  - validation 92 (structure 37, input 55)
  - tools 199
  - resume 87
  - paging 74
  - mutations 70
- 1 vector (`mutations/compact-apply-valid`) is deterministic only at a fixed
  root (D10).

To regenerate, rerun the out-of-repo harness against an oracle whose file
fingerprints equal `MANIFEST.oracle.files`. A fingerprint mismatch invalidates
the corpus, and requires review before replacement.

**Documented differences.** Every vector whose Go output intentionally
differs from the oracle is listed once in
[`testdata/expected/expectations.json`](../testdata/expected/expectations.json):
a one-line reason, the section of this contract that approves it, and the
comparators that prove only that difference occurs. Approved design
changes also pin the SHA-256 and UTF-16 length of the Go output
(root-normalized; for a refusal, of the error text); oracle divergences
(section 7) are proved by their comparators alone. The black-box suite
`internal/conformance` runs every vector against the final engine: an
unlisted vector must equal the oracle byte-for-byte, and a listed one must
equal it after its comparators peel off each documented difference (each
checked against an independent restatement from the fixture's raw bytes).
The stage sections below record how each change was first proved, with
test-only switches that turned it off; the switches were removed in the
cleanup, and the same comparators now run against the oracle directly.
`SHIORI_EXPECTED_UPDATE=1 go test ./internal/conformance ./internal/model`
re-pins after review.

## 9. Owner decisions (APPROVED 2026-09-30)

The owner approved, on 2026-09-30:

1. Every item previously marked PROPOSED in sections 2, 5 and 6, and the
   proposed resolutions D1–D12 in section 7. They are bug fixes to the
   original workplan design, applied in the narrowest way: the V2
   plan/checkpoint/dependencies/journal formats, the `.opencode/workplan/`
   layout, the thirteen `workplan_*` identities, their argument shapes, the
   hash algorithm and the generated Markdown stay unchanged. A fix that would
   need a design change is not implemented; it is listed in
   [STATUS.md](STATUS.md) instead.
2. Keeping the compressed performance fixtures (`testdata/perf/*.tar.gz`,
   ~1 MB) in git.
3. Adding `go.mod` (module `github.com/hoshinoht/shiori`) and the test-only
   JSON Schema validator `github.com/santhosh-tekuri/jsonschema/v6` (pinned
   v6.0.3; its `golang.org/x/text` requirement comes with it). Only
   `_test.go` files import it, so it is never linked into the `shiori`
   binary. Its regexp engine is Go RE2 with `\uXXXX` escapes translated;
   no further dependency.

### Stage C owner decisions (2026-09-30)

4. D1's doctor addition (`{id, valid:false, issues, stateHash,
   recoveryRequired:true}` for a journal-only plan) is accepted, and
   `update {recovery}` accepts that `stateHash` as its `expectedHash`.
5. D10 is changed from "accept and document" to a fix: the compact preview
   token and the archive path must not depend on the absolute project root.
6. The native OpenCode host authorizer is stage D; stage C provides the
   `Authorizer` interface and the standalone CLI implementation (§5.5).

### Stage D owner decision (2026-09-30)

7. **Accepted divergence — resume display-cap residual.** For plans with
   very many truncated strings (the `resume-stress` fixture), the Go resume
   packet chooses a different display-string cap than the reference on some
   budgets (88 of 154 sampled; §10a item 1). Every packet still fits its
   budget and keeps all machine ids, hashes, counts and retrieval pointers
   (R01/R02). This is accepted as-is; resume behaviour is not changed.
   *Superseded by D.1 (§11 item A), which replaces the display-cap ladder.*

## 10. Stage B findings (OBSERVED, pinned by the corpus)

These reference behaviours were not written down at stage A. The Go core
reproduces them byte-for-byte; the vectors named are the evidence.

- **Tool output text** is `JSON.stringify(result, null, 2)` for every tool
  except `workplan_resume`, whose packet chooses its own format (below). Every
  `outputSha256` in `tools/`, `resume/` and `paging/` is the SHA-256 of that
  text after `$ROOT` substitution.
- **Pending journal short-circuit.** `read`, `inspect` and `resume` on a plan
  with `<id>.transaction.json` return only
  `{recoveryRequired, journalPath, planHash, stateHash}`; `read` adds
  `"workplan": null` and `resume` adds `"planFresh": false`
  (`tools/pending-journal/*`, `resume/pending-journal/*`).
- **Resume cursor filters.** The resume cursor stores `phaseId`/`stepId` as
  `"sha256:" + hex(SHA-256(id))`, not the raw id; the inspect cursor stores the
  raw `phaseId` (`paging/resume-big-phase2-step20`, `paging/inspect-big-*`).
- **Resume budget policy.** A packet is built with a pinned-list cap *L*, a
  display-string cap *C* (UTF-16 code units, the last one being `…`, never
  splitting a surrogate pair) and a page size, and the first candidate whose
  complete text fits `maxChars` is returned:
  1. *L* = 4 when `maxChars` is 4096 and 8 at 12000 and 64000. The cut-over
     between those budgets is not covered by the corpus; Go uses 8192.
  2. For *C* in 512, 256, 128, 64, 32, 21, 10, 5, 2, 1: pretty
     (`JSON.stringify(v, null, 2)`), then compact, with the full page.
  3. Then, at *C* = 1, the page shrinks one item at a time (pretty, then
     compact).

  The corpus forbids a cap in 11–20, 22–31 or 33–58 (a packet at such a cap
  would have fit and been chosen), and requires 32, 21, 10, 2 and 1; 256, 128,
  64 and 5 are unconstrained and follow the halving pattern. `truncatedFields`
  lists danger fields first, then the rest, in packet order, capped at 4·*L*.
  All 87 `resume/` and 74 `paging/` vectors reproduce exactly.
  *D.1 (§11 item A) replaces this policy; 35 of those vectors now differ on
  purpose and are pinned in `testdata/expected/`.*
- **Checkpoint diagnostics.** A checkpoint that is JSON but matches neither
  version is `checkpoint: : Invalid input` in doctor and
  `Invalid workplan checkpoint document at <path>: : Invalid input` in resume
  (`tools/list-mixed/a-plan--doctor`, `resume/list-mixed/a-plan--*`).
- **Doctor filtering.** `doctor {id}` keeps `planCount` for the whole
  directory, filters plans by exact file name against the normalized id, and
  lists every sidecar, lock and journal (`tools/list-mixed/UPPER--doctor`).
- **Case-insensitive lookup is inherited from the filesystem.** On APFS,
  `read UPPER` normalizes to `upper` and opens `UPPER.json`; the vectors
  record that. The same call on a case-sensitive Linux filesystem reports
  `Workplan file not found`.

## 10a. Stage B open-issue decisions and stage C findings

Recorded at stage C. Everything here keeps the original workplan design
(formats, layout, tool identities, argument shapes, hash algorithm and
generated Markdown). "Measured" means the reference was executed as a black
box on copies of the corpus fixtures; its source was not read.

**Stage B open issues.**

1. *Resume caps between the corpus budgets* (measured). The pinned-list cap
   is `clamp(floor(maxChars/900), 4, 8)`; the listed `truncatedFields` cap
   is `min(floor(maxChars/256), 32)`; the display-string cap ladder is
   512, 256, 128, 64, 32, 21, 10, **4**, 2, 1 (the reference uses 4, not 5).
   Also measured and fixed: `safety.overflow` is true when any display
   field was truncated (not only danger fields); `checkpoint.recentValidation`
   truncations count as danger fields; `checkpoint.summary` is truncated
   before the current position. With these, every sampled budget
   (4096–12000, step 37/53, plus 15000–64000) of the large-paging and
   full-valid fixtures and the owner's real plan is byte-identical, and the
   corpus still passes. **Residual:** for a plan with very many truncated
   strings (resume-stress) the reference chooses display caps that are not
   a fixed ladder (3, 7, 8, 16 observed); 88 of 154 sampled budgets pick a
   different display cap. Every packet still fits its budget with all
   machine ids, hashes, counts and pointers intact (R01/R02 hold).
   **Accepted as a divergence by the owner on 2026-09-30 (§9 item 7).**
2. *Resume cannot fit (very long machine ids).* Kept as a fail-closed error:
   machine ids are never truncated. Writers cannot create such ids (every
   create/update id is normalized to at most 80 code units), so the case is
   limited to hand-edited plans. No format change.
3. *D1 recovery.* Done (owner decision 4). The expectedHash of a journal-only
   plan is the doctor hash: `workplan-state-v1` over the missing primary,
   the journal's in-root targets and the id's sidecars. Recovery reports
   `planHash`/`stateHash` of the recovered state (for a rolled-back create:
   the same interrupted-plan definition, primary missing).
4. *D4/D5 in mutation preparation.* Done. A stored plan with repeated
   member names is readable, but every writer refuses it before
   authorization: `Workplan <id> has duplicate JSON member names: <paths>.
   Remove the duplicates before mutating the plan.` New `planFile`,
   `specFiles` or `addSpecFiles` links containing `\` are refused with
   `<field>.<i>: Linked path contains a backslash and has an ambiguous
   manifest identity: <raw>`.
5. *Mutating vectors and compact token parity.* Done: the 39 mutating input
   vectors, 70 mutation vectors and 2 compact-preview vectors run in CI
   (STATUS has the table). The token is compared as an opaque value bound to
   its inputs (D10 fix). The reference's `removals.digest` preimage could
   not be recovered by black-box probing (it is root-independent and
   depends only on the removed content); Shiori defines
   `SHA256("workplan-compact-removals-v1\n" + compact JSON of the removed
   object + "\n")`, and the token is `"v1-" + SHA256("workplan-compact-token-v1\n" +
   compact JSON {workplanId, stateHash, archiveReason, canonicalSelection,
   removalsDigest, linkedMarkdownTreatment} + "\n")`. The `v1-` prefix and
   64-hex shape, the archive name `state-<stateHash:12>-<token:12>.json` and
   the transaction id `<token:16>` are unchanged.
6. *Case-insensitive filesystems.* Ids are normalized to lowercase, so plan
   and sidecar names never differ by case. Linked Markdown ownership and
   pending-journal claims compare case-folded paths, and journal validation
   rejects case-folded duplicate targets, so aliasing links fail closed on
   every platform (stricter than necessary on case-sensitive Linux, never
   weaker on APFS). Reads still inherit the filesystem's lookup (§10).

**Stage C findings.**

- *Pre-authorization rejection.* The reference detects several refusals
  only under the lock, after prompting: an existing sidecar or pending
  pre-create journal on create, a move onto an existing file, destinations
  owned by another plan or claimed by a pending journal, and recovery
  third-state edits. Shiori checks them during preparation (same message,
  no authorization request, S06/S07) and again under the lock.
- *Unchanged results.* When a mutation would not change any byte (D12, and
  the same principle for any writer), no intent is prepared and no
  authorization is requested; the result is identical.
- *Plan-file extension.* The linked Markdown must end in lowercase `.md`
  on read and write (the reference rejects `.MD`; stage B accepted it).
- *Dependency validation text* (measured): `dependencies.<i>: Source step
  <p>/<s> does not exist`, `dependencies.<i>: Duplicate dependency source
  <p>/<s>`, `dependencies.<i>.dependsOn.<j>: Duplicate dependency`; a
  replacement drops existing `terminalSummaries`.
- *Compaction* (measured): only generated Markdown is refreshed and listed
  in the intent; preserved Markdown is not a target. Apply prunes dependency
  entries whose source was archived and records terminal summaries for
  archived prerequisites still referenced; the refreshed checkpoint keeps
  its fields, appends the archive path to `references` (last 10 kept) and
  writes `planHash`/`manifest`/`evidenceStatus` last.
- *Lock protocol.* Lock files keep the reference names and lock-owner-v1
  content. Shiori publishes owner metadata atomically (stage file + link),
  reclaims only proven-dead same-host owners past the 5-minute grace under a
  reclaim mutex, and releases by rename + nonce check (a replacement owner's
  lock is restored, never unlinked). Its auxiliary paths differ from the
  reference's (`<lock>.<tx>.stage`, `<lock>.reclaim.*`), so compact-preview
  `writeIntent.resources` lists different lock-protocol paths; all other
  resources match. Mixed TS/Go writers on one root are not proven
  interoperable and must not be run together (stage E, spec 05 §3).

## 11. Approved design changes D.1 (APPROVED 2026-09-30)

A real-use round on the owner's 13-phase/38-step roadmap found five
problems that every earlier stage had reproduced faithfully from the
reference design. The owner approved changing that design on 2026-09-30.
Everything not listed here stays as in sections 1–10: the V2
plan/checkpoint/dependencies/journal formats, the `.opencode/workplan/`
layout, the thirteen `workplan_*` identities, the hash algorithm, the
generated Markdown bytes and every other argument shape. The only schema
change is the new optional `includeNotes` boolean on `workplan_read`.

The oracle corpus is not edited. Vectors whose output changes on purpose
are listed in the D.1 set (now in [`testdata/expected/expectations.json`](../testdata/expected/expectations.json))
with the SHA-256 and UTF-16 length of the pinned Go output, and each one
passes a comparator that proves only the approved change differs
(`internal/conformance/resume_budget_test.go`). There are 56 such tool vectors
(A: 35 resume/paging, B: 3 filtered reads, C/F: 18 validate/doctor) and
one mutation vector (`mutations/patch-validate`, item D).

**A. Resume budgeting prefers fewer items to shorter text.** The reference
shortened every display string (down to one code unit) before it
returned fewer page items. On the roadmap, the default 12000 budget gave
20 items whose every string was cut to 32 code units, and 6000 gave one
code unit. A first D.1 version returned fewer items before any shortening,
which gave 2 of 44 items at 12000 and was too few for surveying work; the
coordinator's review on 2026-09-30 rebalanced it to a target page. The
order (see `Packet.Render` in `internal/resume/budget.go`):

1. **Target page** of min(limit, *T*) items: *T* = 8 at `maxChars` ≥ 12000,
   4 at ≥ 6000, 2 below. While the target still fits, text shrinks first
   through the tiers `{prose cap, title cap}`: uncapped, {2048, 512},
   {1024, 256}, {512, 200}, {240, 80}, then the floor {120, 80}. Each tier
   returns the largest page (at least the target) that fits. Current work
   (checkpoint summary, next action, current step
   target/action/validation) keeps a cap of at least 512 at these levels.
   Pretty output is preferred unless compact output carries more items.
2. **Below the target**, at the floor the page shrinks to one item (the
   rest stays reachable through `nextCursor`). Then current work drops to
   the floor. Then the pinned lists (scope, non-goals, constraints,
   blockers, guardrails, references, recent validation, relevant files,
   warnings, high findings, dependencies) show fewer entries, down to one
   each. Every total and `omittedDangerCounts` entry that the packet has
   stays exact, and `safety.overflow` is set. Recent validation and
   relevant files have no total in the packet (shape unchanged); the
   relevant files stay reachable as page references.
3. Only when one page item (or the pinned packet alone, if nothing is left
   to page) still does not fit, text goes below the minimums (caps 100,
   80, 64, 48, 32, 24, 16, 10, 4, 2, 1). Then file paths and references
   are shortened too, and last of all the page is dropped. Every shortened
   string ends in `…` and is listed in `truncatedFields`.

The minimums are 120 code units for prose (summary, next action, goal,
target/action/validation, list entries, finding detail) and 80 for titles.
At 120, a summary or next action still holds a full clause (about 20
words). At 80, a step title keeps its distinguishing words: the roadmap's
longest phase or step title is 69 code units. The 240 prose floor while
the target page fits keeps a typical action or validation sentence whole
(two to three clauses). A surrogate pair is never split, so a cut string
can be one unit shorter.

Some values are never shortened while one item fits: the path, planFile,
relevant files, checkpoint and page references, and the instruction.
Ids, hashes, enums (kind, severity, status), counts and retrieval
pointers are never shortened at all.

The output shape, field names, list caps
(`clamp(floor(maxChars/900), 4, 8)`), the `truncatedFields` cap and the
cursor format are unchanged. Every accepted budget still yields a packet
within `maxChars`, and every page makes progress. A page smaller than the
target only carries page-item prose at the 120 floor (text shrank first).
`TestResumeBudgetSweep` checks this over 33 budgets × 4 page limits ×
5 plans (including a synthetic 13-phase/38-step roadmap), paged to the
end. It also checks that no packet with more than one item goes below the
minimums or shortens a protected path, and that no page below the target
carries item prose above the floor. The only failure left is still the
§10a item 2 case, machine ids longer than the budget.

This supersedes the display-cap ladder in §10 ("Resume budget policy")
and the §9 item 7 residual. Resume output is no longer
reference-identical whenever the reference would have truncated. On
large plans the default budget pages with fewer, readable items per call.

**B. A filtered `workplan_read` returns a slice.** With `phaseId` and/or
`stepId`, the result is:

- `path`;
- `workplan`: the document without `phases`, and without `reviewFindings`
  and `notes` unless `includeNotes: true`. Unknown top-level metadata is
  kept;
- `selection` (unchanged);
- `plan` with `path`/`exists`. Its `content` is included only on an
  explicit `includeMarkdown: true`; with a filter the default becomes
  false;
- `dependencies`: entries whose source is selected or that depend on a
  selected step, the terminal summaries they reference, and all issues;
- `planHash`, `stateHash`;
- a new `slice` descriptor:
  `{filtered, phaseCount, stepCount, findingCount, noteCount,
  notesIncluded, markdownIncluded, dependenciesFiltered, full}`.

An unfiltered read, with or without `includeNotes`, is byte-identical to
the reference.

`includeNotes` is added to `schema/v1/tools/workplan_read.input.schema.json`
and to the adapter's registration snapshot
(`adapter/opencode/src/registration.json`, key `d1`). Removing the
addition and the `d1` key reproduces the reference snapshot byte-for-byte
(`referenceSha256`, checked by `plugin.test.ts`). This is the only
difference between the model-facing tool surface and the reference.

**C. Markdown drift warning.** `workplan_validate`, each `workplan_doctor`
plan entry and `workplan_patch {validate:true}` report a non-failing
warning when the linked Markdown exists, is nonblank and is not
byte-identical to the generated rendering of the stored JSON. In that
case later JSON changes (status, notes, findings, compaction) do not reach
the Markdown. The warning names the file and the explicit regeneration
path (`workplan_reset` `markdown-only` with `replaceMarkdown=true`).
The new `warnings` member appears only when nonempty, so outputs without
warnings are unchanged. `valid` and `issues` are unchanged.

**D. Status gate and patch issue list.** `workplan_create` and
`workplan_update` refuse to set the plan status to `in_progress`,
`review` or `completed` while the executable-structure rules (spec 01
§3, `x-shiori-structure-rules`, applied to the resulting plan) fail. The
refusal comes before authorization, so nothing is prepared, prompted or
written. The error class is `invalid_structure`. The message lists every
`path: message`, and the CLI `--json` error and the protocol error carry
`issues[{path, message}]`. `draft`, `blocked` and `cancelled` stay
allowed with incomplete structure. An update that does not set a gated
status is not gated (the `"draft"` placeholder is still a no-op). An
update that sets a gated status and completes the structure in the same
call is accepted.

`workplan_patch {validate:true}` (CLI and native) now returns
`metadata.validation.issues`, the full ordered issue list of
`workplan_validate`, next to `valid`/`issueCount`, plus `warnings`
when present.

**F. Stray artifacts in doctor.** `workplan_doctor` reports root-level
files of `.opencode/workplan/` that belong to no plan:

- `orphaned-sidecar`: `<id>.checkpoint.json` or `<id>.dependencies.json`
  with no `<id>.json`;
- `unclassified`: any other file that is not a plan, sidecar, lock,
  temporary or stage file and not a plan's linked Markdown, for example
  `*.patch`.

Journals without a primary JSON remain D1 recovery state, not strays.
Each entry suggests moving the file under `.opencode/workplan/archive/`.
Doctor never moves or deletes anything. The new members
`strayArtifacts[{name, kind, suggestion}]` (bounded by `limit`),
`strayArtifactCount`, `omittedStrayArtifacts` and `warnings` appear only
when there is at least one stray. `workplan_list` is unchanged. *D.3
(§13 items 3 and 5) adds the `stale-markdown` kind and gives every list
entry an `issues` array.*

## 12. Approved design changes D.2 (APPROVED 2026-09-30)

The dependency graph now drives work. The owner approved the brief on
2026-09-30, with two decisions: order checks **warn only** and never
refuse a write, and the critical path (extension X6,
[06](specs/06-extensions.md)) is included. Everything not listed here stays
as in sections 1–11: the V2 plan/checkpoint/dependencies/journal formats,
the `.opencode/workplan/` layout, the thirteen `workplan_*` identities
and argument shapes, the hash algorithm and the generated Markdown bytes.
The graph is never written into generated Markdown. There is no schema
change.

**Scope rule.** Every graph addition appears only when the plan has a
dependency sidecar that decodes and passes `ValidateDependencies` (no
issues). Without a sidecar, or with an invalid one (whose issues are
already reported), outputs are byte-identical to D.1. A prerequisite is
met only when it is `completed`, either as a plan step or as an archived
`terminalSummaries` entry. A step is open when it is neither `completed`
nor `cancelled`.

The oracle corpus is not edited. Vectors whose output changes on purpose
are listed in the D.2 set (now in [`testdata/expected/expectations.json`](../testdata/expected/expectations.json))
(SHA-256 and UTF-16 length of the pinned Go output). Each one is judged
twice (`internal/conformance/graph_test.go`). The same engine with the
graph additions turned off must still pass the oracle, D.1 pin or
divergence check it passed before, and the D.2 output must pass a
comparator against that output which restates readiness, downstream counts
and the critical path from the fixture's raw JSON. There are 11 such
vectors: 6 resume (`full-valid`, `list-mixed/b-plan`), 3 doctor and 2
inspect. No mutation vector changes.

**G1. Resume readiness.** The current step (`checkpoint.current`) and
every `active-work` page item carry `readiness: "ready" | "blocked"`,
right after their status. A ready step also carries `unblocks: N`. A
blocked step carries `blockedBy: [{phaseId, stepId, status}]`, its
prerequisites that are not completed, in stored order. The list shows at
most the pinned-list cap (`clamp(floor(maxChars/900), 4, 8)`, shrinking
with the D.1 list levels), and `blockedByOmitted` gives the rest when
there is any. Ids and statuses are never truncated, so the members cost
a fixed, small budget. The D.1 budgeting (target page 8/4/2, readable
floors, current work ≥512, pinned safety lists kept whole before the page
shrinks) is unchanged, and `TestResumeBudgetSweep` covers a 12-entry
cross-phase graph in addition to the D.1 plans. On the synthetic roadmap
with that graph, the 12000 budget gives 5 items where D.2-off gives 6, and
every other sampled budget gives the same count.

**G2. Order warnings (warn only).** `workplan_update` that sets a step to
`in_progress`, `review` or `completed` while a prerequisite is not completed succeeds
and returns `warnings` (only when nonempty), listing the unmet
prerequisites with their statuses. `workplan_validate`, each
`workplan_doctor` plan entry and `workplan_patch {validate:true}` report
every such step as a non-failing `warnings` entry
(`dependencies: Order warning: step <p>/<s> is <status> but its
prerequisites are not completed: ...`). `valid`, `issues` and every
refusal are unchanged. `review` counts as started (owner decision,
2026-09-30).

**G3. Cancelled prerequisites.** An open step whose unmet prerequisites
include a cancelled one, in the plan or as an archived terminal summary,
is reported as `dependencies: Step <p>/<s> is blocked by cancelled
prerequisite <p>/<s>. Replace or remove the dependency, or cancel the
step.` in validate, doctor and patch validation. Resume adds `Dependency
order warning: Step ...` to `safety.unverifiedWarnings`, after the
checkpoint freshness issue (when there is one) and before the unverified
checkpoint lines, and the item's `blockedBy` shows `status:
"cancelled"`. An update that cancels a step with open dependents returns a
warning naming them.

**G4. Phase replacement against the graph.** An update with `phases` (a
full replacement) and without `dependencies` re-validates the stored
sidecar against the resulting plan in the same prepared write. If the
replacement adds dependency issues, for example a removed prerequisite or
source step, it is refused before authorization:
`Invalid dependency metadata: <issues>; the phase replacement would leave
these dependency links dangling. Replace the dependencies in the same
update.` Issues the sidecar already had do not block the update. An update
that also supplies `dependencies` is validated against the result as
before. `workplan_reset` (draft) is not covered; it is revisited in D.3
(owner decision). *Resolved by D.3 (§13 item 1): the draft reset keeps the
structure, and the wipe removes the sidecar in the same transaction.*

**G5. Dependency writes and views.**

- An entry with an empty `dependsOn` is refused:
  `Invalid dependency metadata: dependencies.<i>.dependsOn: Dependency
  entry must list at least one prerequisite`. A stored sidecar with such an
  entry is still read. Validate reports it as a warning
  (`...: Dependency entry lists no prerequisites.`), not an issue.
- A backward link (a step that depends on a step later in plan order) is
  accepted with a warning: `dependencies.<i>.dependsOn.<j>: Backward link:
  <p>/<s> depends on <p>/<s>, which comes later in plan order.` A
  dependency write returns all graph warnings of the result. Validate and
  doctor report them too.
- The compaction preview adds `archivedPrerequisites[{phaseId, stepId,
  status, dependents[{phaseId, stepId}]}]` (only when nonempty) for
  selected steps that remaining steps depend on. Apply keeps them as
  terminal summaries (unchanged).
- `workplan_inspect` step entries add `prerequisites[{phaseId, stepId,
  status}]` and `dependents[{phaseId, stepId}]`. Open steps also get
  `readiness`, `unblocks` and `slack`.

**G6. Downstream counts and critical path (X6).** `unblocks` is the
number of distinct open plan steps that depend on the step, directly or
transitively. Resume ranks `active-work` items as follows: ready items
first, by `unblocks` descending with ties in plan order, then blocked items
in plan order, then findings and references as before. The page total and
cursor are unchanged; the cursor offset indexes the ranked list. The
critical path is the heaviest chain of open steps. A step weighs its
optional numeric `estimate` member (kept as unknown step metadata, a
positive finite number), else 1. V2 has no estimate field and no
writer sets one: `estimate` is an optional member that stays hand-edited,
preserved like any unknown step member (owner decision, 2026-09-30). Ties go to more steps, then to earlier
plan order. `workplan_inspect` (top level) and each `workplan_doctor` plan
entry add `criticalPath: {length, estimate?, steps[{phaseId, stepId,
status}], recommendation}` when it chains at least two open steps.
`estimate` appears only when a step on the path used one. `slack` is the
critical weight minus the heaviest chain through the step. These are
recommendations only: nothing is executed, reordered on disk or
refused because of them. Resume does not include the critical path, to
keep its budget. The current-step selection is unchanged from D.1 (a
fresh checkpoint position, else the first in-progress, else the first
unfinished step, shown as `blocked` when it is; owner decision).

**Error class of dependency refusals.** Every refusal whose message starts
with `Invalid dependency metadata` (the existing dependency-write
refusals, the G4 phase-replacement and G5 empty-entry refusals, and a
compaction apply over an invalid sidecar) now has the protocol and CLI
`--json` error class `invalid_structure` instead of `internal` (owner
decision, 2026-09-30). The message text is unchanged, so the oracle
mutation vectors (which record messages only) still pass unchanged; the
class is pinned by `TestPhaseReplacement` and `TestDependencyWrites`.

## 13. Approved design changes D.3 (APPROVED 2026-09-30)

Safety and robustness fixes from a live testing round against a copy of the
owner's roadmap. The owner approved the brief on 2026-09-30 (items 1–7).
Everything not listed here stays as in sections 1–12: the V2
plan/checkpoint/dependencies/journal formats, the `.opencode/workplan/`
layout, the thirteen `workplan_*` identities, the hash algorithm and the
generated Markdown bytes. D.1's resume budgeting and D.2's graph behaviour
are unchanged; resume output is not touched by D.3.

The oracle corpus is not edited. Vectors whose output changes on purpose
are pinned in the D.3 set (now in [`testdata/expected/expectations.json`](../testdata/expected/expectations.json))
(SHA-256 and UTF-16 length of the root-normalized Go output; for a refusal,
of the error text). Read vectors are judged like D.2
(`internal/conformance/diagnostics_test.go`): the same engine with the D.3
additions off (`Engine.noD3`, test-only) must pass every earlier check
(oracle, D.1/D.2 pins, declared divergences) unchanged, the D.3 output
minus the approved members must equal that output byte-for-byte, and the
additions are restated independently from the fixture's raw bytes
(raw-byte hashes from the hash contract, stale-checkpoint paths, missing
markers). There are 20 such tool vectors. Six mutation vectors have
dedicated comparators (below), and one input vector
(`validation/input/reset--bad-mode`) now lists `"wipe"` in its enum
message. Resume and paging vectors are unchanged.

**1. Reset.** `workplan_reset`:

- `mode: "draft"` (the default) resets **status only**: the plan, every
  phase and every step become `draft` and the checkpoint sidecar
  (execution state) is removed. Phases, steps, notes, review findings,
  scope, constraints and the dependency graph are kept, so the dependency
  sidecar stays consistent (D.2 G4 left this open). Generated or missing
  Markdown is regenerated; handwritten Markdown is kept unless
  `replaceMarkdown=true` (the earlier draft reset refused it). The result
  adds `checkpointRemoved: true` when a checkpoint was removed. The journal
  operation stays `reset:draft`; it may now also delete the checkpoint.
  The inherited draft reset wiped phases, steps, findings and notes with no
  archive and left a dangling dependency sidecar.
- `mode: "wipe"` (new) clears phases, findings and, unless
  `preserveNotes`, notes, and sets the status to `draft`. It is a
  preview → confirm flow like compaction. A call without `previewToken`
  and `confirmation` is a read-only preview (no intent, no authorization,
  nothing written) that returns `previewToken`, the exact removal counts
  and digest, the archive path and the write intent (write and delete
  paths). The apply call needs that exact token and
  `confirmation: "WIPE_PLAN_CONTENT"`. The token is
  `"v1-" + SHA256("workplan-reset-wipe-token-v1\n" + compact JSON
  {workplanId, stateHash, preserveNotes, removalsDigest,
  linkedMarkdownTreatment} + "\n")`, with `removalsDigest` =
  `SHA256("workplan-reset-wipe-removals-v1\n" + compact JSON {phases,
  reviewFindings, notes} + "\n")`; it is root-independent and is not
  authority. Apply is one transaction (journal operation `reset:wipe`):
  the archive `archive/<id>/state-<stateHash:12>-<token:12>.json` (mode
  0600, compaction archive layout with `archiveVersion: 1`,
  `operation: "reset:wipe"`, the removed content and the complete original
  JSON, Markdown, checkpoint and dependency bytes) is published first, then
  the plan and Markdown, then the checkpoint and dependency sidecars are
  deleted. Handwritten Markdown needs `replaceMarkdown=true` (its original
  is in the archive).
- `mode: "markdown-only"` is unchanged.
- `previewToken` and `confirmation` are refused outside `mode: "wipe"`
  (`previewToken and confirmation apply only to mode=wipe`); with either
  present, `confirmation` must be `WIPE_PLAN_CONTENT` and `previewToken`
  must be present (input errors on both surfaces), and a token that does
  not match the current state, removals, `preserveNotes` or Markdown
  treatment is refused before authorization.

Schema: `schema/v1/tools/workplan_reset.input.schema.json` adds `"wipe"`
to the `mode` enum and the `previewToken` and `confirmation` string
properties (with `x-shiori-rules`); the journal schema documents
`reset:wipe`. The adapter registration snapshot
(`adapter/opencode/src/registration.json`, key `d3`) lists these additions
and the four reset texts it changes (tool description, `mode`
description and enum, `preserveNotes` description), each with its
reference value. Removing the `d1` and `d3` keys and their additions and
restoring the reference values reproduces the reference snapshot
byte-for-byte (`d1.referenceSha256`, checked by `plugin.test.ts`).
Vectors: `mutations/reset-draft` and `reset-draft-preserve-notes` (the
comparator restates the expected JSON from the stored plan with every
status set to draft, checks the kept counts, the regenerated Markdown, the
removed checkpoint and that nothing else changed), and the input vector
above. Recovery validation (`validateJournal`) accepts `reset:wipe` with
plan, Markdown, archive, checkpoint and dependency targets and
`reset:draft` with a checkpoint target.

**2. Unreadable plans are repairable through the tools.** When a primary
plan JSON exists but cannot be loaded (unparseable, wrong shape, invalid
link), `workplan_doctor` (plan entry), `workplan_validate` and
`workplan_list` report `planHash`/`stateHash` computed from the raw bytes:
the plan manifest is the primary JSON alone, the state manifest adds the
checkpoint, dependency and journal sidecars (`workplan-plan-v1` /
`workplan-state-v1`, unchanged algorithm; the linked Markdown and specs are
unknown). `workplan_create {overwrite: true, expectedHash}` accepts that
state hash and rewrites the plan. The existing Markdown is kept unless
`replaceMarkdown=true` (nothing proves it was generated); an explicit
`planMarkdown` without `replaceMarkdown` is refused. A pending journal is
still refused. Recovery of such an overwrite accepts an undecodable
before-image of the primary (only for `create:overwrite`) and uses the
raw-byte hashes, so rollback restores the exact corrupt bytes. Pinned by
the `tools/invalid-schema/*` and `tools/list-mixed/*` doctor/validate/list
vectors, `TestUnreadablePlanRepair` (truncated plan → doctor hash →
create overwrite) and the fault-injection scenario
`create-overwrite-unreadable`.

**3. Stale Markdown copy.** `workplan_doctor` reports a root-level
`<id>.md` whose readable plan `<id>` now links another file (left behind
when the `planFile` moved) as a stray of kind `stale-markdown`, with the
warning `Stale Markdown copy .opencode/workplan/<id>.md: plan <id> now
links <planFile> ...` and the D.1 archive suggestion. Other unlinked
`*.md` files in the workplan root stay `unclassified` (D.1 item F). When
the plan cannot be read, its `<id>.md` is not classified. Doctor never
moves or deletes anything.

**4. Missing step markers.** When the linked Markdown is handwritten (the
D.1 drift warning applies), `workplan_validate`, each `workplan_doctor`
plan entry and `workplan_patch {validate:true}` add a non-failing warning
naming the steps whose `<!-- workplan-step-id: <id> -->` marker is absent
(`planFile: Linked Markdown <path> has no step marker (...) for N of M
steps: <p>/<s>, ...`, at most ten listed, then `and N more`). Generated
Markdown always has the markers. Handwritten Markdown is never rewritten.

**5. Polish.**

- *Stale checkpoint detail.* The doctor issue for a stale v2 checkpoint
  appends which manifest entries differ: `<path> changed (checkpoint
  sha256 <12>, now sha256 <12>|missing)`, `<path> was added to the plan's
  links ...`, `<path> is no longer linked ...` (at most five, then `and N
  more`), or, when every entry matches, that only the recorded planHash
  differs. Resume keeps the D.1 text so its budget is unchanged. *D.3.1
  (§14 item 3) adds a compact form to the resume `checkpoint.diagnostic`.*
- *Readable generated ids.* A phase or step without an id gets the slug
  of its title (the id normalization, cut at a word boundary to 48 units),
  with `-2`, `-3`, ... only when that id is taken. Generated step ids also
  avoid every other step id in the plan and explicit ids in the same
  input, so stepId-only references and Markdown markers stay unambiguous.
  A title without `[a-z0-9]` falls back to the earlier
  `<prefix>-<word>-<word>-<6 digits>` form. Existing ids never change.
  Vector `mutations/create-generated-ids`: the plan and Markdown equal the
  oracle's after substituting the two ids.
- *Spec files must exist.* A `specFiles` or `addSpecFiles` entry that
  names a missing file is refused during preparation, before
  authorization, like the D.2 dependency checks: `<field>.<i>: Linked spec
  file does not exist: <path>. Create the file first, or leave it out of
  the list.` (class `invalid_input`). Links already stored are not
  re-checked by unrelated writes. Vector `mutations/create-new-full` (its
  spec is absent in the fixture): refused with no prompt and no write; with
  the file present every changed file equals the oracle's.
- *List entries.* Every `workplan_list` entry has an `issues` array
  (empty when valid). An entry that cannot be listed (non-canonical file
  name, parse or schema error) is `{id, valid: false, issues: [message]}`
  plus, for an unreadable plan, `planHash`, `stateHash` and
  `recoveryRequired` (item 2). The single `issue` string of those entries
  is gone.
- *Note size.* A new note (`notes` on create, `appendNotes` on update)
  above 16384 UTF-8 bytes after trimming is refused before authorization:
  `<field>.<i>: Note is <n> bytes, above the 16384-byte (16 KiB) per-note
  limit. ...` (class `invalid_input`). Stored notes are never changed.
- *File modes.* A new plan JSON or linked Markdown takes the permission
  bits of the first existing primary plan in the workplan root (UTF-16
  order) with owner read/write added, or `0644` minus the process umask
  when there is none. Replaced files keep their mode. New sidecars,
  journals, staging files and archives stay `0600`.

**6. Interrupted-write recovery.** `TestKillRecovery` runs a mutation in
a separate process and SIGKILLs it at deterministic commit points (journal
published; mid-publication). Doctor then reports `recoveryRequired` with a
state hash. Recovery inside the 5-minute abandonment grace fails with
`lock_unavailable` (a proven-dead owner is still not reclaimed by age
alone, S11); past the grace it reclaims the dead owner's locks, and
`update {recovery: "resume"|"rollback"}` restores every journal target to
its exact after/before image with no lock or staging file left. Recovery
now also deletes the interrupted transaction's own staging files (names
derived from the journal's transaction id and target index, only for
transaction ids of the UUID/hex shape Shiori writes); they are listed as
delete resources of the recovery intent. Vectors
`mutations/update-recovery-resume` and `-rollback` (whose fixture contains
such a staging file): the only extra change is that file's removal.

**7. Unverified OpenCode versions.** (Narrowed by §19: this now applies to a
host version outside `hostPolicy`.) On a host version outside
`SUPPORTED_HOST_VERSIONS` the adapter still registers all
thirteen tools. `workplan_read`, `list`, `inspect`, `validate`, `resume`,
`doctor` and `compact_preview` work. `workplan_create`, `update`, `patch`,
`reset`, `checkpoint` and `compact` (the schema's mutating tools, including
the compact preview mode, which `compact_preview` covers) are refused
before any core request with class `unsupported_capability`: `Shiori
adapter not verified for OpenCode <v>; writes disabled — update Shiori
(verified: <list>; hostPolicy ...). ...`. The permission bridge is not started, so
no write can be authorized either. `workplan_doctor` reports
`runtimeFacts.host {opencodeVersion, verified, verifiedVersions, writes:
"enabled"|"disabled", detail}`; the core renders `host` only when the
adapter supplies it (type-checked and bounded like the other facts), so
CLI and corpus doctor output are unchanged.

## 14. Approved design changes D.3.1 (APPROVED 2026-09-30)

Changes from a third live testing round against a copy of the owner's
roadmap, approved by the owner on 2026-09-30 (items 1–5). Everything not
listed here stays as in sections 1–13: the V2
plan/checkpoint/dependencies/journal formats, the `.opencode/workplan/`
layout, the thirteen `workplan_*` identities and argument shapes (no
input schema changes), the hash algorithm, D.1's resume budgeting rules
and D.2's graph behaviour. Item 5b changes the generated Markdown bytes
and the timestamp format of new writes on purpose.

The oracle corpus is not edited. Every vector runs with the D.3.1 changes
off (`Engine.noD31`, test-only), which must pass every earlier check
(oracle, D.1/D.2/D.3 pins, declared divergences) unchanged, and with them
on. A vector whose output or written files differ is pinned in
the D.3.1 set (now in [`testdata/expected/expectations.json`](../testdata/expected/expectations.json))
(SHA-256 and UTF-16 length of the root-normalized output; for a refusal,
of the error text) and passes a comparator
(`internal/conformance`):

- read vectors: removing the approved members (resume
  `checkpoint.diagnostic` back to `null` and `criticalPath`; doctor
  `recoveredPlanFile` and the wiped-plan note) gives the D.3.1-off text
  byte-for-byte; the stale paths are restated from the raw checkpoint
  manifest and files, the recovered link from the raw `planFile` member,
  and the resume critical path from `workplan_inspect`;
- mutation vectors: the same authorization count and changed-file set;
  every changed file equals the D.3.1-off file after mapping the
  whole-second timestamp back to its millisecond form and each SHA-256 to
  a placeholder, except generated Markdown, which must be the current
  rendering of the written plan where the D.3.1-off file is the legacy
  rendering of its plan (the plans equal after the timestamp mapping); the
  output compares the same way and its hashes match the files on disk;
- Markdown render vectors (`internal/model`): the legacy rendering is
  byte-identical to the oracle file, and the current rendering equals it
  with a space inserted before each finding's `(status)`.

There are 27 pinned vectors: 3 doctor tools vectors (2b), 6 resume vectors
(3 stale diagnostic, 3 critical path), 17 mutation vectors (5b timestamps;
7 of them also refresh generated Markdown to the new finding rendering)
and 1 Markdown render vector (5b). No paging vector and no mutating-input
vector changes.

**1. Step status gate.** `workplan_update` (and, for the steps it
creates, `workplan_create`) refuses to set a step to
`in_progress`, `review` or `completed` while that step fails the per-step
structure rules of spec 01 §3 (`x-shiori-structure-rules`: nonblank and
unique `id`, nonblank `title`, `action` and `validation`), applied to the
resulting plan. A step counts as set by the call when it is new (by phase
and step id) or its status changes to a gated one, whichever edit did it
(`updateSteps`, `addSteps`, `addPhases`, a `phases` replacement). A step
whose gated status does not change is not re-checked, so an existing plan
stays editable and repairable. The refusal comes before authorization
(nothing is prepared, prompted or written) with class
`invalid_structure`: `Refusing to set step status while the step's
executable structure is incomplete: <p>/<s> -> <status>, .... Missing:
<path>: <message>; .... Complete the listed fields first (or in the same
update); draft, blocked and cancelled are allowed with incomplete
structure.` The CLI `--json` error and the protocol error carry
`issues[{path, message}]` like the D.1 plan gate. `draft`, `blocked` and
`cancelled` stay allowed; supplying the missing fields in the same call
is accepted. `workplan_create` applies the same gate to every step it
creates in a gated status (coordinator review, 2026-09-30), after the
D.1 plan-level gate, with the same message, class and `issues` shape;
`workplan_patch` is unchanged. No oracle vector changes because of it.

**2. Repair of unparseable plans** (extends §13 item 2).

- (a) `workplan_create {overwrite: true}` over an unreadable plan adds an
  archive target to the same `create:overwrite` transaction, published
  first: `archive/<id>/state-<stateHash:12>-<sha256(raw):12>.json` (mode
  0600; `archiveVersion: 1`, `workplanId`, `archivedAt`, `reason`,
  `operation: "create:overwrite"`, `stateHash`, and `source` with
  `workplanJsonPath`, `workplanJsonSha256`, the exact damaged bytes as
  `workplanJson` when they are valid UTF-8, else `workplanJson: null` and
  `workplanJsonBase64`, plus the linked Markdown, checkpoint and
  dependency bytes as they were). The result adds `archivePath`. Recovery
  validation accepts an archive target for `create:overwrite`.
- (b) `workplan_doctor` scans the raw bytes of an unreadable plan for a
  `"planFile": "<string>"` member (tolerant of truncation and of a broken
  document around it). When one value is found (members that disagree
  give nothing) and it passes the plan-file policy (inside the root, under
  `.opencode/workplan/`, Markdown, no backslash, not a symlink) and is not
  the linked Markdown of another readable plan, the plan entry adds
  `recoveredPlanFile`. The repair uses that Markdown path
  instead of `<id>.md` unless the input gives `planFile` (an explicit
  `planFile` wins). The raw-byte hashes are unchanged.
- (c) `workplan_update`, `patch`, `reset`, `checkpoint` and `compact`
  (apply) on an unreadable plan append to the load error: `The plan cannot
  be loaded, so it cannot be changed in place. Repair it with
  workplan_create overwrite=true and expectedHash=<stateHash> (the
  stateHash workplan_doctor reports for it; the damaged bytes are
  archived first, and the linked Markdown is kept unless
  replaceMarkdown=true).` The error class is unchanged. A plan with a
  pending journal keeps the recovery refusal.

**3. Stale checkpoint diagnostic in resume.** For a stale v2 checkpoint,
`checkpoint.diagnostic` (earlier `null`) is `changed: <path>[, <path>,
<path>] [+N more]`: the manifest paths that the §13 item 5 doctor detail
names (changed, added or no longer linked), at most three, or `changed:
planHash only` when every entry still matches. The text is capped at 240
UTF-16 code units at construction and is prose for the D.1 budget (it can
shrink to the 120 floor and, in the emergency tier, below it, listed in
`truncatedFields`). Fresh, legacy (v1), invalid and missing checkpoints
keep their earlier diagnostic.

**4. Compact critical path in resume.** When the plan has a valid
dependency sidecar and the D.2 critical path chains at least two open
steps, the packet adds `criticalPath: {length, nextStep: {phaseId,
stepId}}` after `currentDependencies`; `nextStep` is the first open step
on the path. The full path stays in `workplan_inspect` and
`workplan_doctor`. Without such a sidecar the packet is byte-identical to
D.3. The member is advisory, so it is dropped before any text would go
below the D.1 minimums (the level ladder is retried without it before the
emergency caps). `TestResumeBudgetSweep` passes unchanged, and
`TestResumeAdvisoryBudget` checks every budget from 4096 to 16000 (step 8
below 6000) on the synthetic graph roadmap with a stale checkpoint.

**5. Wiped plans and cosmetics.**

- (a) A doctor plan entry for a readable plan with no phases and status
  `draft` whose `archive/<id>/` holds a `reset:wipe` archive adds the
  warning `phases: Plan was wiped (workplan_reset mode=wipe); the removed
  content is archived at <newest archive path>. Add phases (workplan_update
  phases or addPhases) to continue; the missing-phases issue stays until
  then.` Validate keeps the `phases: At least one phase is required` issue
  and adds nothing.
- (b) Generated Markdown renders a finding as `- [<severity>] <title>
  (<status>)` with a space (the reference wrote `<title>(<status>)`); the
  rest of the rendering is unchanged. Detection of generated Markdown
  (validate/doctor drift and marker warnings, the handwritten guards of
  create, update, reset and compaction, and `MarkdownGenerated`) accepts
  both renderings, so Markdown generated by the reference or an earlier
  stage is still generated and a write refreshes it to the new rendering.
  New writes record whole-second UTC timestamps (`2026-09-30T15:07:56Z`)
  in the plan, checkpoint, dependency sidecar, archives and journals;
  reads keep accepting every `isoDatetimeUtc` form (milliseconds, whole
  seconds, minutes, any fraction), and stored timestamps are never
  rewritten except `updatedAt` by a write. Lock owner files keep their
  millisecond `startedAt` (it is parsed for lock age, not stored with the
  plan).

## 15. Approved design changes D.4 (APPROVED 2026-09-30)

D.4, approved 2026-09-30: the compaction advisor (spec 06 P2) and note
rollover (spec 06 P3). Everything not listed here stays as in sections
1–14: the V2 plan/checkpoint/dependencies/journal formats, the
`.opencode/workplan/` layout and archive layout, the thirteen `workplan_*`
identities, the hash algorithm, the generated Markdown, D.1's resume
budgeting rules, D.2's graph behaviour and the compaction flow (preview →
exact `previewToken` → `confirmation: ARCHIVE_SELECTED_HISTORY`, fresh
checkpoint, archive of the complete originals first). The only input
change is the optional `noteRollover` member of `workplan_compact` and
`workplan_compact_preview`.

The oracle corpus is not edited. Every tools/resume/paging vector runs
with the D.4 advice off (`Engine.noD4`, test-only), which must pass every
earlier check unchanged, and with it on; a difference must be pinned in
the D.4 set (now in [`testdata/expected/expectations.json`](../testdata/expected/expectations.json)) and
pass a comparator (`internal/conformance/compaction_advice_test.go`): removing
`compactionRecommended` gives the D.4-off text byte-for-byte and the
advice counts restate from the raw plan JSON by an independent
implementation of the rules below. With the default thresholds **no
corpus vector changes** (the largest eligible history, `large-paging`,
could save 13 080 of its 45 232 JSON bytes, under the 32 KiB minimum), so
the list is empty:
plans without qualifying history give output byte-identical to D.3.1.
`TestCompactionAdviceOnCorpus` runs the same comparator with lowered
thresholds. No mutation vector changes (they never pass `noteRollover`).

**1. Compaction advisor (P2).** Advice only; nothing is archived.

- *What compaction could archive* (the unchanged compaction rules):
  completed phases whose every step is completed ("archivable"; a
  completed phase with a cancelled step is not), the notes the rollover
  rule (item 2) selects with `keepLatest` = the configured `keepNotes`,
  and every finding with status `resolved`.
- *Estimate.* The plan JSON saving is computed from the pretty encoding of
  the removed elements, including the archive pointer note and the new
  `updatedAt`, so it equals the size apply writes. Doctor also renders the
  generated Markdown of the result (`treatment: generated-refresh`), or
  reports it unchanged (`preserved`, handwritten or missing).
- *Recommended* when the plan JSON saving is at least `minSavingsBytes`
  and at least one threshold is crossed: `notes` (eligible rollover
  notes), `terminalPercent` (archivable-phase bytes as a percentage of the
  plan JSON) or `planBytes` (plan JSON size). Defaults, grounded in the
  measured long plan (257 KB JSON, 214 notes; notes ≈48% of the JSON,
  terminal steps ≈48% of step bytes): `minSavingsBytes` 32 KiB, `notes`
  50, `terminalPercent` 25, `planBytes` 192 KiB, `keepNotes` 20. A plan
  JSON under `minSavingsBytes` is never examined further. Thresholds are
  trusted operator configuration (`shiori resume|doctor|serve
  --compaction-advice off|min-savings-kib=N,notes=N,terminal-percent=N,plan-kib=N,keep-notes=N`),
  never model input.
- `workplan_resume` adds `compactionRecommended: {savedJsonBytes,
  savedJsonPercent, notes, terminalSteps, resolvedFindings}` after
  `currentDependencies`/`criticalPath` (`notes` = eligible rollover notes,
  `terminalSteps` = steps in archivable phases). It is advisory: it is
  dropped before any text would go below the D.1 minimums (first, before
  the D.3.1 critical path). Like the D.2 readiness members it may cost a
  page item at a tight budget. (Superseded by §17 item 2: since D.4.2
  the advice is shown only when it costs no page content.)
- Each `workplan_doctor` plan entry adds `compactionRecommended` with
  `reasons`, `estimate` (`json`, `markdown` with `treatment`, `total`:
  `before`/`after`/`saved`/`percent`), `terminalSteps {total, archivable,
  archivableBytes}`, `notes {total, keepLatest, eligible, eligibleBytes,
  kept {latest, pinned, decision, openReference, archivePointer}}`,
  `resolvedFindings {count, bytes}`, the ready-to-preview `selection`
  (`completedPhaseIds`, `noteRollover {keepLatest}` when notes are
  eligible, `resolvedFindingIndexes`), `checkpointFreshness`, the
  effective `thresholds` and an `instruction`. The member appears only
  when recommended.

**2. Note rollover (P3).** `workplan_compact` and
`workplan_compact_preview` accept `noteRollover: {keepLatest?,
pinNoteIndexes?}` (`keepLatest` integer 1–10000, default 20;
`pinNoteIndexes` zero-based note indexes; unknown keys rejected). It
selects every note older than the latest `keepLatest` except notes that
are:

- *pinned*: the text contains `[pinned]` (any case) or the index is in
  `pinNoteIndexes`;
- *decision records* (the JSON twin of a Decision register line under the
  convention "record every user decision as a JSON note and a Decision
  register line"): the whole word `decision`, `decisions` or `decided` in
  any case, or the uppercase word `USER`;
- *open references*: the note names an open (not completed/cancelled)
  step as a whole `<phaseId>/<stepId>` token, or quotes the title of an
  open finding (titles of at least 12 code units);
- *archive pointers*: notes starting with `Compaction archive: ` (since
  D.4.2, §17 item 3: only the latest 3; older ones roll over).

Notes carry no timestamps, so "older than the newest checkpoint" is
enforced by the existing apply rule: apply requires a fresh checkpoint,
which binds the current plan bytes, so every stored note predates it. The
selected indexes become the `noteIndexes` of the selection; the removals,
digest, archive (complete original note texts in `removed.noteIndexes`
and the complete original JSON, Markdown, checkpoint and dependency bytes
in `source`) and apply are unchanged. The preview token binds
`canonicalSelection.noteRollover {keepLatest, pinNoteIndexes}` as well as
the resolved `noteIndexes`, so apply must repeat the same `noteRollover`
(else `previewToken does not match ...`). `noteRollover` with
`noteIndexes` is an input error on both surfaces (`noteRollover cannot be
combined with noteIndexes; noteRollover selects the notes`); a pin index
out of range or duplicated is refused (`noteRollover.pinNoteIndexes: Note
index out of range: <i>` / `... contains duplicate indexes`). A rollover
that selects nothing cannot be applied (the existing "Select at least
one ..." refusal). In rollover mode only, the preview adds
`noteRollover {keepLatest, pinNoteIndexes, noteCount, olderThanLatest,
selectedCount, kept, olderThanCheckpoint}` and `estimatedSavings` (the
exact plan JSON and Markdown bytes before/after of the apply intent), and
the apply result adds `savings` in the same shape. CLI: `shiori compact
<id> --reason R --rollover [--keep-notes N] [--pin-note I]...`.

Schema: `schema/v1/tools/workplan_compact.input.schema.json` and
`workplan_compact_preview.input.schema.json` add the `noteRollover`
property (with an `x-shiori-rules` entry for the combination). The
adapter registration snapshot (`adapter/opencode/src/registration.json`,
key `d4`) lists the two additions; removing the `d1`, `d3` and `d4` keys
and their additions and restoring the `d3` changes reproduces the
reference snapshot byte-for-byte (`d1.referenceSha256`, checked by
`plugin.test.ts`).

## 16. Approved design changes D.4.1 (APPROVED 2026-10-01)

D.4.1, approved 2026-10-01: expectedHash guidance. In real use an agent
called `workplan_patch` right after a `workplan_update` without
`expectedHash` and got `Invalid patch input: expectedHash: Native
existing-state writes require the current stateHash`. The refusal is
correct and stays; only the guidance changes. Everything not listed here
stays as in sections 1–15: every input shape, error class, refusal
condition and output member, the thirteen identities and the corpus
formats.

**1. Missing expectedHash (native).** The native refusal of every
existing-state writer (`workplan_update`, `patch`, `reset`, `checkpoint`,
`compact` apply and `create` with `overwrite: true`) keeps its path, class
(`invalid_input`) and leading sentence and appends guidance:
`expectedHash: Native existing-state writes require the current stateHash
— pass expectedHash set to the stateHash from your last successful write,
or re-read with workplan_resume or workplan_inspect first`. It names no
hash (the current hash is not echoed, so an agent re-reads before it
retries). The guidance contains no `; ` so the issue list stays separable.
The core surface message (`overwrite requires the current stateHash`) and
the CLI usage error are unchanged. The `x-shiori-rules` native messages in
the six `schema/v1/tools` input schemas carry the new text.

**2. Stale expectedHash.** The `stale_state` refusal keeps its reference
text (`Stale expectedHash; current stateHash is <hash>. Reread the plan and
recompute the mutation.`, which already names the current hash as the
reference did; the protocol error's `currentStateHash` is unchanged) and
appends ` Use workplan_resume or workplan_inspect for that re-read before
retrying, so the retry is based on the current plan.` The addition names no
hash. The commit-time `Workplan state changed after preparation: ...`
refusal (storage layer) is unchanged.

**3. Model-facing descriptions.** One sentence about `expectedHash` is
appended to the descriptions of the six mutating tools, in the adapter
registration snapshot (`adapter/opencode/src/registration.json`, generated
by `scripts/snapshot-registration.ts`, key `d4_1`) and in the top-level
`description` of the matching `schema/v1/tools` input schemas:
`Pass expectedHash = the stateHash from your latest read or successful
write.` (`workplan_update`, `patch`, `reset`, `checkpoint`), prefixed
`With overwrite=true, pass …` for `workplan_create` and `In apply mode,
pass …` for `workplan_compact`. `workplan_compact_preview` (read-only) is
unchanged. `d4_1.changes` records each description's previous value (for
`workplan_reset` the D.3 text); restoring them, then removing the `d1`,
`d3` and `d4` additions and restoring the `d3` changes, reproduces the
reference snapshot byte-for-byte (`d1.referenceSha256`, checked by
`plugin.test.ts`).

The oracle corpus is not edited. Vectors whose error text changes are
pinned in the D.4.1 set (now in [`testdata/expected/expectations.json`](../testdata/expected/expectations.json))
(SHA-256 and UTF-16 length of the root-normalized error text) and pass a
comparator (`internal/conformance/writes_test.go`): each guidance suffix
directly follows its reference sentence, echoes no hash, and removing the
suffixes gives the oracle text byte-for-byte. There are 6 such vectors: 5
mutating-input vectors (native surface; core unchanged) and 1 mutation
vector (`mutations/create-overwrite-stale-hash`). Every other vector is
unchanged from D.4.

## 17. Approved design changes D.4.2 (APPROVED 2026-10-01)

D.4.2, approved 2026-10-01: the owner's decisions on the D.4 review items
(STATUS "Owner decisions to review (D.4)"). Everything not listed here
stays as in sections 1–16: the V2 formats, the thirteen identities, every
input shape (the `noteRollover` member is unchanged), error classes, the
compaction flow and archive, the doctor advice shape and D.1/D.2/D.3.1's
resume budget rules and their order.

**1. Decision-record rule: confirmed.** The §15 item 2 rule stays as
implemented: a note is a decision record when it contains the whole word
`decision`, `decisions` or `decided` in any case, or the uppercase word
`USER`. No change.

**2. Resume advice never costs page content.** `workplan_resume` chooses
its packet without `compactionRecommended` by the unchanged D.1–D.3.1
rules (readable levels with the critical path, then without it, then the
emergency caps). The advice is then added only when the packet with it
has the same degradation level as the packet without it: the same number
of page items, the same compact/full item form and every text cap (list,
title, long, pinned, protected) equal — i.e. the chosen level rendered
with the member still fits `maxChars`. Otherwise it is omitted; an
emergency packet never carries it. So a packet with the advice is always
the advice-off packet plus that one member, byte-for-byte, and a packet
without it is the advice-off packet. `workplan_doctor` keeps the full
advice. (Replaces §15 item 1's "dropped before any text would go below
the D.1 minimums … may cost a page item".)

**3. Archive pointer retention.** Rollover keeps the latest 3 notes that
start with `Compaction archive: ` (counted over the whole note list in
order, including those inside the latest `keepLatest`); older pointer
notes are ordinary notes for rollover (kept only if pinned, a decision
record or an open reference, else selected and archived with their
complete text like any other note — never lost). The pointer that apply
appends is not counted, so after an apply a plan holds at most 4 pointer
notes. `kept.archivePointer` in the preview's `noteRollover` and in the
doctor advice counts only the retained ones; the advisor's `eligible`,
`eligibleBytes`, JSON/Markdown estimate and ready-to-preview selection
follow the same rule (they use the same selection). The model-facing
`noteRollover` descriptions (`schema/v1/tools/workplan_compact*.input.schema.json`,
`adapter/opencode/src/registration.json`) say "the latest 3 compaction
archive pointers"; the property is a `d4` addition, so the reference
reproduction (`d1.referenceSha256`) is unaffected.

**4. Adapter option.** The adapter accepts an optional plugin option
`compactionAdvice`: `"off"`, or an object with any of `minSavingsKiB`,
`notes`, `terminalPercent`, `planKiB`, `keepNotes` (each a positive
integer ≤ 2^30; `terminalPercent` ≤ 100, `keepNotes` ≤ 10000). It is
validated when the plugin loads (before any core or permission bridge is
started); anything else — another type, an unknown key, a non-integer or
out-of-range value — fails plugin load with `Shiori adapter: invalid
plugin option "compactionAdvice": …`. A valid value is passed to the
spawned core as the trusted `shiori serve --stdio --compaction-advice
<spec>` flag (keys `min-savings-kib`, `notes`, `terminal-percent`,
`plan-kib`, `keep-notes`; protocol `Options.Compaction`). Absent (or
`{}`), no flag is passed and the defaults apply. It is operator
configuration, never model input.

The oracle corpus is not edited.
The D.4.2 set (now in [`testdata/expected/expectations.json`](../testdata/expected/expectations.json))
lists no vector: with the default thresholds no corpus fixture is
recommended for compaction and no oracle vector passes `noteRollover`, so
every vector is byte-identical to D.4.1 (`TestCorpusParity`'s D.4 split
still runs every read vector with the advice on and off).
`TestCompactionAdviceOnCorpus` (lowered thresholds) now applies the
comparator to every advised resume packet, small budgets included, with
no budget-cost exemption.

## 18. Approved design changes D.4.3 (APPROVED 2026-10-01)

D.4.3, approved 2026-10-01: checkpoint safety after a real-use incident. A
plan's checkpoint was stale, so `workplan_resume` withheld its summary,
next action, guardrails, references and recent validation (summary and
next action rendered as `null`, the lists empty, the totals present, the
guardrails as unverified warnings). A model rebuilt the checkpoint from
that view, and because `workplan_checkpoint` replaces every field, it
stored the summary `null DEPLOYED …`, one validation line and empty
guardrails and references. Everything not listed here stays as in
sections 1–17: the V2 plan/checkpoint formats (a checkpoint is still
schema version 2 bound to the current plan manifest), the thirteen
identities, error classes, and D.1–D.4.2's resume budget rules and their
order.

**1. Resume names withheld fields.** When the checkpoint is readable but
not fresh (`stale` or `legacy-unverified`), the resume `checkpoint`
object adds `withheld` right after `freshness`: the names of the stored
fields the packet does not show, in the order `summary`, `nextAction`
(always: a stored checkpoint never has them blank), then `guardrails`,
`references` and `recentValidation`, each only when the stored list has
entries. Blockers are shown, so they are never withheld; the stored
phase/step position is not shown either (the packet's `current` is the
plan's), but it is not listed. The existing `null`/`[]` values and totals
are unchanged for compatibility. The instruction becomes the D.1 stale text
followed by ` Fields in checkpoint.withheld are hidden, not empty: never
copy null/[] into a checkpoint. Full checkpoint: <.opencode/workplan/<id>.checkpoint.json>
(workplan_read omits it). Refresh via workplan_checkpoint merge=true.`
`workplan_read` does not return the checkpoint (checked; its output is
unchanged), so the sentence names the sidecar file. Missing and invalid
checkpoints withhold nothing: no member, the D.1 text. The member is a
pinned safety item like the D.3.1 stale diagnostic: field names are never
shortened, the instruction is protected text, and the packet is chosen by
the unchanged D.1–D.3.1 levels with the member present, so it can cost page
content at a tight budget. On the synthetic 13-phase/38-step roadmap with a
stale checkpoint and every list set (`TestResumeWithheldBudget`, 951 packets
from 4096 to 16000 with limits 1, 8 and 20) every packet fits, 383 keep the
D.4.3-off level exactly, and 42 carry one page item fewer; the rest use a
lower text tier. The human `shiori resume` output adds a `withheld:` line.

**2. Checkpoint write guard.**

- *Refusal.* `workplan_checkpoint` refuses a `summary` or `nextAction`
  whose trimmed text is exactly `null` or `undefined` or starts with
  `null ` or `undefined ` (case-sensitive), on both surfaces, as an input
  rule (class `invalid_input`, before preparation and authorization, path
  `summary` or `nextAction`): `Checkpoint <field> starts with
  null/undefined, which is how workplan_resume shows a withheld field of a
  stale or legacy checkpoint, not its value — read the stored checkpoint
  first (the file named in the resume instruction), or pass merge=true and
  omit <field> to keep the stored value` (no `; `, so the issue list stays
  separable). `nullable …`, `Null …` and `NULL …` are accepted.
- *Warnings.* When a readable checkpoint (fresh, stale or legacy) exists,
  the result adds `warnings` (after `directorySync`, only when nonempty)
  with one entry per list that loses stored entries, in the order
  guardrails, references, recentValidation, blockers: `<list>: <before> →
  <after> (<n> previous entr(y|ies) not kept; merge=true keeps omitted
  fields)`, where before/after are the list lengths and n counts stored
  entries absent from the new list (so a same-length replacement warns
  too). A shorter summary or next action is not a warning; a blank one
  never reaches the comparison because it is refused (`Checkpoint summary
  cannot be empty`, unchanged). The write still happens: the warning is
  after the fact, the refusal and merge mode are the prevention.

**3. Merge mode.** `workplan_checkpoint` accepts optional `merge`
(boolean) and `appendValidation` (a string or an array of strings); the
CLI adds `--merge` and repeatable `--append-validation`. With
`merge: true`:

- `summary` and `nextAction` become optional; every omitted field keeps
  the stored checkpoint's value: summary, nextAction, the phase/step
  position, blockers, recentValidation, guardrails and references. Given
  fields replace theirs (an explicit `[]` clears a list and warns).
- The position: `phaseId`/`stepId` when given (the unchanged rules);
  otherwise the stored position when it still resolves to an open step,
  else it is re-derived as when omitted (for example after the stored step
  was completed).
- A stale checkpoint merges like a fresh one; a legacy v1 checkpoint's
  fields are carried over into a v2 checkpoint. A missing or unreadable
  checkpoint merges as an empty one: an omitted `summary` or `nextAction`
  is then refused during preparation (class `invalid_input`, before
  authorization): `merge=true keeps the stored <field>, but there is no
  readable stored checkpoint — pass <field>`.
- The written checkpoint binds the current plan state exactly like a
  replacing write (fresh `planHash` and manifest, `createdAt` kept,
  `updatedAt` now); `expectedHash` rules are unchanged.

`appendValidation` (with or without merge) appends to the resulting
`recentValidation` after merge or replacement: each line is trimmed, blank
lines and exact duplicates of a line already present are skipped. There is
no count limit on checkpoint lists, so none applies. Without `merge` (or
with `merge: false`) `summary` and `nextAction` stay required with the
reference message.

**4. Model-facing description.** The `workplan_checkpoint` description
(registration snapshot and `schema/v1/tools/workplan_checkpoint.input.schema.json`)
states that without merge=true it replaces the whole checkpoint and that
to update it one passes merge=true or reads the current checkpoint first.

Schema: `workplan_checkpoint.input.schema.json` adds `merge` and
`appendValidation`, requires only `id` plus `summary`/`nextAction` unless
`merge` is `true` (`if`/`else`), and lists both refusals in
`x-shiori-rules`; `TestCheckpointMergeSchemaAgreesWithParser` checks that
the schema and the Go parser agree. The adapter registration snapshot
(`adapter/opencode/src/registration.json`, key `d4_3`) lists the two
additions and four changes with their previous values (description,
`summary`/`nextAction` descriptions, `required` reduced to `["id"]`; no
top-level conditional, which some model hosts reject); restoring them
before the `d4_1` changes and then applying the earlier reversal
reproduces the reference snapshot byte-for-byte (`d1.referenceSha256`,
checked by `plugin.test.ts`).

The oracle corpus is not edited. Every tools/resume/paging vector runs
with the D.4.3 additions off (`Engine.noD43`, test-only), which must pass
every earlier check unchanged, and with them on; mutation vectors run the
earlier checks with them off and are then compared on against off.
The D.4.3 set (now in [`testdata/expected/expectations.json`](../testdata/expected/expectations.json))
pins 7 vectors: the 6 resume vectors of `checkpoint-stale-v2` and
`checkpoint-legacy-v1` (comparator: `withheld` equals the list restated
from the raw checkpoint file, the instruction is the D.1 text plus the
sentence, and removing both gives the D.4.3-off packet byte-for-byte —
every corpus packet keeps its level) and `mutations/checkpoint-ok`
(comparator: files and authorizations equal, the result differs only by
`warnings`, restated from the fixture's stored checkpoint and the vector
input). The input rules are not behind the flag: no corpus input uses
`merge`, `appendValidation` or a `null …` text.

## 19. Host version policy (APPROVED 2026-10-01)

OpenCode ships 2.0.x patches almost daily, and an exact allow-list turned
writes off on each one. The permission bridge already proves every
authorization at runtime (host binding through the RPC proof, event-stream
readiness, exact ask correlation, no self-approval) and fails closed on any
mismatch, so a changed host contract shows up as a refused write rather
than an unsafe one. The allow-list therefore records which versions were
tested, while a policy decides which versions may write.

**1. `hostPolicy` plugin option.** `"patch"` (the default) or `"exact"`.
Anything else fails plugin load with an actionable message, like
`compactionAdvice`.

**2. Trust classes** (`hostTrust` in `adapter/opencode/src/plugin.ts`):

| Class | Rule | Writes | Bridge |
| --- | --- | --- | --- |
| `verified` | in `SUPPORTED_HOST_VERSIONS` | enabled | started |
| `patch` | policy `patch`; a stable `X.Y.Z` (no pre-release or build suffix) in the same `X.Y` line as a verified version, with `Z` at or above that line's lowest verified patch | enabled | started |
| `unverified` | anything else, including a new minor or major, a pre-release, an older patch below the floor, and every unlisted version under `exact` | refused as in §13 item 7 | not started |

**3. Doctor.** `runtimeFacts.host` keeps its five fields, so the core,
schema and corpus are unchanged: `verified` is true only for a listed
version, and `writes` is `"enabled"` for `verified` and `patch`. A `patch`
host's `detail` says it is an unverified patch and names the
`bun run verify-host <version>` command. The refusal message gains the
policy in its parenthesis.

**4. `bun run verify-host [version] [--dry-run]`** (in `adapter/opencode`,
`scripts/verify-host.ts`) verifies a release against the newest verified
version and, if every gate passes, adds it to `SUPPORTED_HOST_VERSIONS`.
Nothing is committed. Gates, in order:

1. Contract: from the published `@opencode/client` packages, the
   declarations of the five routes `host-client.ts` calls (method, path,
   body, statuses), the generated types they and the permission events use
   (closure over `ServerInfo`, `SessionGetOutput`, `PermissionCreateInput`,
   `PermissionCreateOutput`, `PermissionAsked`, `PermissionReplied`,
   `RpcCallInput`, `RpcCallOutput`) and the service-discovery modules are
   unchanged, as is every declaration file of `@opencode/plugin`. Bundler
   chunk hashes are ignored. Any difference stops the run for review by
   hand.
2. The adapter test suite.
3. The live runtime smoke test against an installed binary of exactly that
   version (`OPENCODE_BIN`, Homebrew, or the desktop app's bundled CLI),
   on `testdata/fixtures/full-valid` unless `SHIORI_SMOKE_FIXTURE` is set.

## 20. Approved design changes E1 (APPROVED 2026-10-07)

E1: the evidence ledger (spec 06 X2). Everything else stays as in
sections 1–19: no V2 plan field, no new or renamed tool, the same hashes
and generated Markdown. The only input change is the optional
`recordEvidence` member of `workplan_update`. No corpus vector changes:
every new output member appears only for a plan that has a ledger.

**1. Sidecar.** `.opencode/workplan/<id>.evidence.json`, schema
[`evidence-v1`](../schema/v1/evidence-v1.schema.json): `{schemaVersion: 1,
id, updatedAt, records[]}`, each record `{phaseId, stepId, command,
exitCode, outputDigest|null, summary?, treeOid|null, scope?: [{path,
digest|null}], source, recordedAt}`. It is classified (`evidence`) and
never listed as a plan; one without a primary plan is an
`orphaned-sidecar` stray. It is **not** in the state manifest, so writing
it changes neither `planHash` nor `stateHash`. Retention: the newest 5
records per (step, command) and 2000 overall.

**2. Tree binding.** `treeOid` is the git tree of the project root's
working state: tracked files with uncommitted changes plus untracked,
non-ignored files, without `.opencode/workplan`. Shiori computes it with
git on a copy of the index and a private, empty object directory with no
alternates, so the repository (index, objects, refs and their mtimes) is
only read. A scope entry's `digest` is the sha256 of `git ls-files -s -z
-- <path>` in that tree (null when the path has no entries). Outside a
git work tree `treeOid` is null. Filter drivers (for example LFS clean)
still run as on `git add`.

**3. States.** Per (step, command), the latest record: `failing` (non-zero
exit), else `stale` (scope digests, or without a scope the tree, differ
from now), `unknown` (no tree now or then), `fresh`. A step's state is its
worst command; `none` without records.

**4. `recordEvidence`** (max 20 items): `phaseId`, `stepId`, `command`
(nonblank, ≤ 2000), `exitCode` (32-bit integer), `output` (only its
sha256 is stored) or `outputDigest`, `summary` (≤ 500), `scope` (≤ 50
project-relative paths, never under `.opencode/workplan`). Steps must
exist after the update. `source` comes from the surface: `agent` (native),
`cli`, or `cli-run` (`shiori evidence ... -- COMMAND`, which runs the
command in the root, binds the tree from before the run and records the
real exit code and output digest). An update carrying only
`recordEvidence` writes the ledger alone (plan JSON, `updatedAt`,
Markdown and hashes unchanged). A ledger that does not decode is never
overwritten: recording is refused. The ledger is an `update` journal
target, so recovery resumes or rolls it back like any other.

**5. Read surfaces** (only when the ledger exists): `workplan_inspect`
adds `evidence {path, valid, tree|issues}` and a per-step `evidence
{state, commands[]}`; `workplan_doctor` adds per plan `evidence {path,
valid, records, tree, steps, completedUnverified {count, steps},
orphanRecords?}` (an invalid ledger never makes the plan invalid);
`workplan_resume` adds compact `evidence {steps, completedUnverified,
current}` under the same no-page-cost rule as the compaction advice
(evidence first).

**6. Completion warnings.** When an update completes steps of a plan that
has a ledger (or records evidence in the same call), each completed step
whose evidence is not `fresh` gets a non-failing warning. Completion is
not gated.

**Rollback.** The reference plugin lists `<id>.evidence.json` as an
invalid primary plan (`<id>.evidence`) and does not recognise a pending
journal that targets it; resolve journals before switching back.

Schema: `workplan_update.input.schema.json` adds `recordEvidence`. The
adapter registration (`registration.json`, key `e1`) lists the addition;
removing it before the d4_3…d1 reversal reproduces the reference snapshot.

## 21. Approved design changes P4 (APPROVED 2026-10-07)

P4: transaction journal v2 by reference (spec 06 P4). Writes use v2 by
default; `--journal-version 1` on the mutation commands and on `serve`
keeps writing v1. Both versions stay readable and recoverable. Hashes,
plan formats and tool surfaces are unchanged.

**1. Format** ([`transaction-journal-v2`](../schema/v1/transaction-journal-v2.schema.json)):
v1's fields, but each target carries `beforeBackup` and `afterStage`
instead of `beforeContent`/`afterContent`. Names are fixed:
`.<base>.<tx>.<i>.stage` (as in v1) and `.<base>.<tx>.<i>.before`, next
to the target. Any other name invalidates the journal.

**2. Commit.** After staging and before the journal: each existing target
is hard-linked to its `.before` name, the link must be the same file
(device and inode, size, mtime) the locked recheck just hashed (a change
is a stale-state refusal with nothing published), and the target
directories are synced. The journal follows
as in v1. On a failure after the journal, the staged files and links are
kept (they are the images). After the journal is removed, the links are
removed. The links are listed as staging paths of the intent, so the
host authorizes them; compaction/reset previews list them the same way
(the corpus compares them like the lock auxiliary paths).

**3. Recovery.** Each image comes from the target itself when it already
holds that hash, else from its link or staged file. An image found
nowhere refuses only the direction that needs it ("the before image of
... is missing or changed"); third-state detection is unchanged. Recovery
removes the transaction's staged files and links.

**4. Cost.** On the measured 286 KB owner plan, one note append wrote
1 048 240 bytes with v1 (staged plan plus a 762 494-byte journal) and
286 440 with v2 (a 694-byte journal): 3.66× fewer.

**Rollback.** The reference plugin cannot read a pending v2 journal.
Resolve pending journals (as the rollback procedure already requires), or
run with `--journal-version 1`, before switching back.

## 22. Approved design changes X3 (APPROVED 2026-10-07)

X3: worktree lanes (spec 06 X3, spec 04). The only input change is the
optional `lanes` member of `workplan_update` (and `lane` on a
`recordEvidence` item). No corpus vector changes.

**1. Sidecar** `.opencode/workplan/<id>.lanes.json`, schema
[`lanes-v1`](../schema/v1/lanes-v1.schema.json): per lane `laneId`,
`state`, `steps`, `claims`, `baseline {treeOid, head, dirty}`, `checkout
{path, branch}|null`, `history` and timestamps. Classified (`lanes`),
outside the state manifest (lane changes never alter `planHash` or
`stateHash`), an `update` journal target. A record only: Shiori never
creates, merges or removes worktrees or runs git commands that write.

**2. Operations**, applied in order, each refused as a whole update on
error (`Invalid lanes input: lanes.<i>: ...`):

- `propose {laneId, steps, claims?}`: a new id (never reused); each step
  exists and belongs to no other active lane; claims are project-relative
  paths outside `.opencode/workplan` (a path claims everything under it).
  The baseline is the parent working tree (as for evidence), HEAD, and the
  count of paths that differ from HEAD; a dirty baseline warns that a
  worktree created from HEAD omits them (spec 04 §3).
- `transition {laneId, state, checkout?}`: claimed → prepared →
  running → review → (running | integrating) → merged, and any active
  state → abandoned. `checkout` only on → prepared: an existing worktree
  of the same repository (same common git dir), outside the project root,
  on `branch` when given (recorded when omitted), used by no other active
  lane. Without it the lane shares the parent checkout. → merged warns when
  a lane step is still open or the checkout's HEAD is not in the project
  HEAD; it never refuses (patch handoffs are allowed).
- `claims {laneId, add?, remove?}` on an active lane.

After every operation, the claims of active lanes must not overlap.

**3. Doctor** (per plan, when the sidecar exists): `lanes {path, valid,
lanes[], mergeOrder, mergeCycle?, unownedWorktrees?}`. Per lane: steps
and steps done, claims, `checkout {path, branch, exists, outsideClaims
{count, paths}}` (paths changed since the baseline commit that no claim
covers), `blockedBy` (active lanes owning a prerequisite) and `issues`:
missing checkout, cleanup required (a merged/abandoned lane's checkout
still exists; never removed), branch moved, a workplan journal inside the
checkout, changes outside claims, every step done but not integrated.
`mergeOrder` is a topological order of active lanes by cross-lane step
dependencies (creation order breaks ties); `unownedWorktrees` lists the
repository's worktrees no lane records. **Inspect** adds `lanes {path,
valid, active[]}` and a per-step `lane {laneId, state, checkout}`;
**resume** adds the advisory `lanes {active, current?}` (tried first of
the advisory members, same no-page-cost rule).

**4. Evidence.** A record may name an active `lane`: it is bound to that
lane's checkout tree and compared with it while the lane is active, and
with the project tree afterwards, so lane results go stale until they are
repeated on the combined state (unless the combined state is identical).
`shiori evidence --lane L -- COMMAND` runs in the lane checkout.

## 23. MCP server (APPROVED 2026-10-07)

`shiori mcp` exposes the thirteen tools over the Model Context Protocol
(stdio, JSON-RPC 2.0; revisions 2025-03-26, 2025-06-18 and 2025-11-25,
newest offered when the client asks for an unknown one). It is a second
adapter over the same engine, not a new tool surface.

1. **Tools.** Names are the `workplan_*` identities without the prefix
   (`--tool-prefix` re-adds one); descriptions and input schemas are the
   adapter's `registration.json`, embedded verbatim
   (`internal/mcp/registration.json`, kept byte-equal by a test). Read
   tools and `compact_preview` carry `readOnlyHint`; create, update,
   reset and compact `destructiveHint`. Results are the exact tool text
   (`isError` with the core's message on failure); no
   `structuredContent`, so resume stays within its budget.
2. **Root.** `--root`, else the client's roots (exactly one `file://`
   root; re-listed after `notifications/roots/list_changed`). Native
   input rules apply, so `workspaceRoot` is refused.
3. **Writes.** Prepared without side effects; approval by elicitation (a
   form with one boolean, naming the plan, the operation and every path
   written, deleted or archived) or, by operator choice or when the
   client lacks elicitation under `auto`, by the client's tool-call
   approval. No lock or file exists while the prompt is open; a decline
   or cancellation (`notifications/cancelled`) changes nothing; locked
   rechecks are unchanged. Writes stay limited to the approved platform
   matrix (§5.6).
4. **Doctor** runtime facts: the registered tool names, plugin id
   `shiori-mcp`, and the client and approval mode as the permission
   detail; host version facts are absent.
5. **Resources** (added 2026-10-07, read-only): `workplan://<id>/resume`
   (the resume packet with default arguments), `/report` (§25, commit
   links over 20 commits) and `/history` (the newest 50 log entries,
   §24), listed per plan and as templates. `resources/subscribe` is
   supported: a subscribed plan is checked after each write through the
   server and every two seconds (cached stat-trusting reads of the plan,
   plus the evidence, lanes and change-log files), and a change sends
   `notifications/resources/updated`; once the client has listed
   resources, a changed set of plans sends
   `notifications/resources/list_changed`. Unknown URIs answer -32002.

Not yet: MCP 2026-07-28 (stateless requests, multi-round-trip
elicitation, roots deprecated): `--root` already covers its
configuration-based root.

## 24. Approved design changes X4 (APPROVED 2026-10-07)

The change log of spec 06 X4 and the rebased writes it enables. Notes
stay in the plan (the separate-notes question of X4 remains deferred);
undo is not part of this change.

1. **Sidecar.** `<id>.history.jsonl`
   ([history-v1](../schema/v1/history-v1.schema.json)): one compact JSON
   line per committed write, `{seq, prev, v, at, op, source, tx, before,
   after, changes}`. `prev` is the sha256 of the previous line, so lines
   form a hash chain; `before`/`after` are the plan and state hashes
   around the write (`null` for a plan that did not or no longer
   exists); `source` is `agent` (native adapter), `cli` or `mcp`, set by
   the trusted caller. `changes` names the plan elements the write
   changed: `goal`, `status`, `notes` (`appended` with a count, else
   `changed`), `findings/<index>`, `phases/<phaseId>`,
   `phases/<phaseId>/steps/<stepId>` (with `from`/`to` on status
   changes), `phases` and `.../steps` when reordered, plus `markdown`
   (explicit Markdown, patch or a move), `dependencies`, `checkpoint`,
   `evidence` and `lanes`. `updatedAt` alone is not a change.
2. **Writing.** The entry is part of the prepared intent (its path is a
   write resource, its payload digest part of the intent digest) and is
   appended after the transaction completes, still under both locks;
   storage adds `seq` and `prev` from the last line and cuts a torn last
   line first. A failed append leaves the committed write standing.
   Every write path logs: create, update, patch, checkpoint, compaction,
   reset and explicit recovery. Engines may turn the log off
   (`NoHistory`); the conformance corpus leaves the file out of its
   comparisons because the reference writes none.
3. **Advisory, never authority.** The log is outside the plan and state
   hashes and is never a precondition. Lost appends and edits outside
   Shiori show up as gaps (`before` ≠ the previous `after`); an edited or
   undecodable line breaks the chain and only the part after the last
   break is trusted. Readers read at most the newest 4 MiB.
4. **Rotation.** At 4 MiB the log moves to
   `archive/<id>/history-<createdAt>-<tx8>.jsonl` before the next
   append; the first line of the new segment chains to the last of the
   archived one. A write prepared within 256 KiB of the limit names that
   archive path among its resources, so the intent still lists every
   path the commit may write.
5. **Rebased updates.** `workplan_update.rebase` (boolean; the default
   is the operator's `--rebase` on `serve`, `mcp` and CLI `update`,
   otherwise false): when `expectedHash` is stale, the update is computed
   on the current state if the log connects `expectedHash` to the
   current state hash, and committed if no element it changes overlaps
   an element a newer write changed. Overlap is the same path, or a
   parent and child where either side adds, removes or reorders; two note
   appends never overlap. Changes to evidence, lanes and links also
   conflict when the log includes them in the rebase range, including
   sidecar-only writes; they remain outside the state hash. The result
   carries `rebased: {fromHash, over: [{seq, op,
   source, at}]}`. Otherwise the reference stale refusal is returned with
   `Not rebased: <reason>.` appended. Without `rebase` the stale
   refusal is unchanged. The locked recheck still binds the commit to
   the state the rebase was computed on.
6. **Views.** Resume adds `sinceCheckpoint` (writes after the newest
   logged checkpoint, when the log reaches the current state: count,
   sources, step status moves first→last, findings added and resolved,
   notes appended); it is advisory, added only when the packet still
   fits its budget. Doctor adds `history` per plan with a log: entries,
   last seq, whether the log reaches the current state, issues, and
   `stalledSteps` (in progress for over 72 hours per the log, with no
   evidence recorded since). `shiori history <id> [--since HASH]
   [--limit N]` prints the log.

## 25. Operator commands: verify and report (APPROVED 2026-10-07)

CLI only; no tool, schema or sidecar format changes.

1. **`shiori verify <id>`** lists the latest record of each command for
   steps whose evidence is stale or failing (`--all`: every step with
   records; `--step P/S` narrows), and only those recorded with source
   `cli-run` (`shiori evidence -- COMMAND`): commands asserted by an
   agent or the operator are named and skipped, never run. The exact list
   is printed; running needs `yes` on a terminal or `--yes`. Each command
   runs in the root (or its lane's checkout) against a tree taken just
   before it, and its result is recorded through
   `workplan_update.recordEvidence` (source `cli-run`; the intent is
   printed). The stored command text must split back into words exactly
   as `shiori evidence` joined them, else it is refused. Exit status 1
   when any re-run fails.
2. **Commit links.** A passing record (outside a lane) links to the
   newest commits from HEAD (`--commits N`, default 50) whose content is
   what it tested: the whole tree (the commit's tree, as seen from the
   root and without the workplan directory, computed in memory and equal
   to the record's tree OID) or, for scoped records, every scope digest.
   Over a run of matching commits the oldest is named. Read-only git.
3. **`shiori report <id>`** renders progress, open work (readiness from
   the dependency sidecar), the critical path, evidence with commit
   links, open blocker/critical/major findings, lanes and the last ten
   logged writes, as Markdown or `--json`.

## 26. Plan links, templates and quality checks (APPROVED 2026-10-07)

1. **Plan links (spec 06 X5).** `workplan_update.planLinks` (at most
   100 `{planId, relation, note?}`, relation `blocks`, `blockedBy` or
   `related`) replaces `<id>.links.json`
   ([links-v1](../schema/v1/links-v1.schema.json)), a parent-owned
   sidecar outside the state manifest; with only sidecar members the plan
   and its hashes stay unchanged. Self-links and duplicate
   (plan, relation) pairs are refused; links to plans that do not exist
   yet are kept with a warning. Adapter key `x5`.
2. **Portfolio.** Read-only and advisory, built from every links
   sidecar: each plan's status and progress, `blocks` edges (`blockedBy`
   reversed), `related` pairs, which plans wait on unfinished
   (not completed or cancelled) blockers, one cycle if any, and links to
   missing plans. Shown as doctor's top-level `portfolio` (unfiltered
   doctor, only when some plan has links), as resume's advisory
   `waitingOnPlans`, and by `shiori portfolio`. Cross-plan writes do not
   exist.
3. **Templates.** `shiori create --template feature|bugfix|migration`
   fills `phases` with standard steps whose validations name a
   placeholder command (`TEST_COMMAND`, `CHECK_COMMAND`); `kind` defaults
   to the template name. CLI only; exclusive with `phases` in `--input`.
4. **Quality checks.** Advisory `quality` per plan: open steps without a
   validation, open steps whose validation names no command (no code
   span and no leading command word), and open blocker/critical/major
   findings while no step is open. Doctor shows it only for plans that
   record evidence (as with the completion warnings); `shiori report`
   always does. Doctor's `lanes` adds `changeOverlaps`: paths that two
   active lanes' checkouts both change, whatever their claims.
