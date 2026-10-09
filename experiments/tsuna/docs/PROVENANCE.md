# Provenance and licensing record

Experiment licence: **GPL-3.0-or-later** (same as the enclosing Tsuna repository). Third-party code
keeps its own licence; MIT notices are reproduced in `third_party/licenses/`. Not legal advice.

Upstream revisions used:

| Upstream | Revision | Licence | Notices |
|---|---|---|---|
| Tsuna (reference) | `92c4728936e3a06115f00809710d2d0e19662ea6` | GPL-3.0-or-later (© 2026 hoshinoht) | repository `LICENSE`, `NOTICE` |
| Oh My Pi | tag `v18.8.6` = `f068751e2f1dbdbc195977776d47a26db8697495` | MIT — © 2025 Mario Zechner; © 2025-2026 Can Bölük; © 2026 Stencil Labs, Inc. (`packages/coding-agent/LICENSE` identical to root) | `third_party/licenses/oh-my-pi.LICENSE` |
| OMP natives | `@oh-my-pi/pi-natives@18.8.6` (+ platform leaf) | MIT — © 2025-2026 Can Bölük; © 2026 Stencil Labs, Inc.; bundled third-party notices in the package's `THIRD-PARTY-NOTICES.txt` | `third_party/licenses/pi-natives.LICENSE` |
| Pi | tag `v1.1.0` = `abe508e1b89912adde45528136c3221eb69acdd7`; npm `@earendil-works/*@1.1.0` | MIT — © 2025 Mario Zechner | `third_party/licenses/pi.LICENSE` |

None of the studied OMP TypeScript files carries a per-file licence header; the package licence
applies. The reference working tree at `/Users/cantabile/.config/tsuna` was not available in this
environment; **no uncommitted working-tree changes were inspected or adopted** (no file hashes to
record).

## Component record

| Destination | Source | Upstream paths | Licence | Relationship | Significant modifications |
|---|---|---|---|---|---|
| `src/orchestration/semaphore.ts` | OMP v18.8.6 | `packages/coding-agent/src/task/parallel.ts` (`normalizeConcurrencyLimit`, `Semaphore`, `semaphoreAbortReason`) | MIT | **Copied** | header; added `inFlight`/`waiting` getters |
| `src/policy/shell-tokenize.ts` | OMP v18.8.6 | `packages/coding-agent/src/tools/shell-tokenize.ts` (`LiteralShellCommandSegment`, `SHELL_STATEFUL_COMMANDS`, `SHELL_INTERPRETER_COMMANDS`, `SHELL_ASSIGNMENT`, `SHELL_REINTERPRET_OPTION`, `extractLiteralAndChainSegments`) | MIT | **Copied** | exported interface; other tokenizers omitted |
| `src/orchestration/runtime.ts` | OMP v18.8.6 (behaviour) | `registry/agent-registry.ts`, `registry/agent-lifecycle.ts`, `task/spawn-run.ts`, `task/index.ts`, `tools/wait.ts`, `tools/yield.ts`, `irc/bus.ts`, `task/executor.ts` | MIT | **Adapted (re-implemented from studied logic)** | Pi SDK backend; numeric `(generation, runId)` guards instead of ref-identity CAS; cancelled-is-sticky rule; park detaches synchronously and refuses during revival; revival coalescing per agent; per-parent permits held for a run's duration with `finally` release (SpawnRun pattern); owner-scoped wait that never waits on peers; yield-reminder ladder (2 reminders vs OMP's 3 + forced tool choice); terminal yield must be alone (OMP allows siblings); explicit tools instead of `agent://`/`proc://` URLs; delivery reconciliation by transcript marker (OMP uses in-memory consumed sets) |
| `src/orchestration/tools.ts` | OMP v18.8.6 (tool surface) | `task/types.ts` (task schema), `tools/wait.ts`, `tools/yield.ts`, `internal-urls/agent-protocol.ts`, `async/job-control.ts` | MIT | **Adapted** | independent schemas; see DESIGN §5 for name/action differences |
| `src/policy/rules.ts` | Tsuna 92c4728 | `lib/permissions.ts` (`globMatches`, `evaluateRules`, `canonicalPath`, `gitBoundary`, `isExternal`) | GPL-3.0-or-later | **Adapted** | removed OpenCode skill-path remap and `TSUNA_ROOT` lookup; added `evaluateAction` |
| `src/policy/engine.ts` | Tsuna 92c4728 | `lib/permissions.ts` (`decidePermission`, `enforceWorkplanOwnership`, `canSurfaceTool`) | GPL-3.0-or-later | **Adapted** | new intent mapping for harness tools; harness-level denies; path canonicalisation also for grep/glob; exec-capable flag guard (new); `genericAsk` flag for the reviewer |
| `src/policy/gate.ts` | Tsuna 92c4728 | `extensions/permissions.ts` (headless fail-closed, approval flow), `lib/permission-review.ts` (reviewer only waives generic asks) | GPL-3.0-or-later | **Adapted** | host-independent gate; reviewer interface pluggable, not wired to a model |
| `src/context/transforms.ts` | Tsuna 92c4728 | `lib/runtime.ts` (image budget, cache risk), `lib/tsuna.ts` (`compressToolResults`) | GPL-3.0-or-later | **Adapted** | Pi message fields (`timestamp`), decisions persisted in the agent record, no per-provider budgets yet |
| `src/context/project.ts` | Tsuna 92c4728 | `lib/project-context.ts` | GPL-3.0-or-later | **Adapted** | explicit Tsuna folder list (`.tsuna`, `.agents`, top-level `AGENTS.md`); synchronous; skill listing |
| `native/supervisor/src/jobs.rs` | Tsuna 92c4728 | `native/tsuna/src/jobs.rs` (sha256 `6d94528fb7e4acff33f0fd39497aaa0c1ad73b945156d410e630742f8968a749`) | GPL-3.0-or-later | **Adapted** | root is `<state>/jobs` (never `.runtime/jobs`); `--id` result identity; `cancel` + `cancelled` state; tests adjusted and two added |
| `native/supervisor/src/main.rs`, `Cargo.toml` | — | — | GPL-3.0-or-later | Independent | minimal CLI; dependencies reduced to anyhow, libc, rand, serde, serde_json |
| `agent-packs/sample/agents/*.md` | Tsuna 92c4728 | `agent/prompts/{orchestrator,explore,code-engineer,code-checker,tester}.md`, `config/permissions.json`, `config/roles.json` | GPL-3.0-or-later | **Adapted** (condensed prompts; rules translated to harness actions) | output schemas added; model names are logical entries |
| `src/backend/pi.ts` | Pi 1.1.0 (API use) | uses `createAgentSession`, `DefaultResourceLoader`, `SessionManager`, `SettingsManager`, `ModelRuntime`, `createFauxCore` | MIT (dependency) | Independent (calls the published API) | in-memory credential store; fixture dispatcher over the faux provider |
| `src/mcp/manager.ts` | Pi 1.1.0 (API use) | `@earendil-works/pi-mcp` `McpClient`, `StdioTransport`, `StreamableHttpTransport` | MIT (dependency) | Independent | `mcp__<server>__<tool>` naming follows Pi's extension convention |
| `src/backend/omp-models.ts` | OMP v18.8.6 (API use) | `@oh-my-pi/pi-ai` `streamSimple`; `@oh-my-pi/pi-catalog` `getBundledModel`, `getSupportedEfforts`, `isGeneratedProvider`; `@oh-my-pi/pi-utils` `logger.setTransports`/`registerLogSink` | MIT (dependency) | Independent adapter (calls the published API) | context/event conversion to Pi; catalog-only limits; explicit per-request keys |
| `fixtures/models/llm-server.ts` | — | — | GPL-3.0-or-later | Independent | Anthropic Messages + OpenAI Chat SSE fixture written from the public wire formats |
| `src/tools/natives.ts`, `src/tools/builtins.ts` | OMP natives (API use) | `grep`, `glob`, `hashlineFileHash`, `hashlineFormatNumberedLines`, `invalidateFsScanCache` | MIT (dependency) | Independent | content tag = native hashline tag + sha256 prefix |
| everything else under `src/`, `test/`, `fixtures/`, `scripts/`, `bin/` | — | — | GPL-3.0-or-later | Independent | — |

## Runtime dependencies (not vendored; installed from npm, locked in `bun.lock`)

* `@earendil-works/pi-coding-agent`, `pi-ai`, `pi-agent-core`, `pi-mcp` `1.1.0` (MIT) and their
  transitive dependencies (Anthropic/OpenAI/Google SDKs, typebox, …).
* `@oh-my-pi/pi-ai`, `pi-catalog`, `pi-utils`, `pi-wire`, `omptype` `18.8.6` (MIT — © 2025 Mario
  Zechner for pi-ai; © 2025-2026 Can Bölük; © 2026 Stencil Labs, Inc.). pi-ai ships its own
  `THIRD-PARTY-NOTICES.txt` (same aggregate as the natives package).
* `@oh-my-pi/pi-natives@18.8.6` (MIT) with `@oh-my-pi/pi-natives-<platform>@18.8.6`. The addon's own
  `THIRD-PARTY-NOTICES.txt` lists vendored MIT code (brush-core, uutils-derived builtins), a CC BY 4.0
  font, MPL-2.0 JS dependencies and Rust crates under MIT/Apache-2.0/ISC/BSD/Zlib/Unicode/BSL/CC0 and
  one CDDL-1.0 crate (`inferno`). **Owner decision needed before redistributing the prebuilt addon
  inside a GPL bundle**; the experiment only installs it as a separate npm dependency.
* Rust: anyhow, libc, rand, serde, serde_json (MIT OR Apache-2.0), locked in
  `native/supervisor/Cargo.lock`.

No third-party assets (fonts, images, templates) are included in the experiment.
