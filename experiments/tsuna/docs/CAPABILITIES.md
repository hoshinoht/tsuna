# Capability matrix

Legend: **Implemented** = built and covered by automated tests or the offline demo;
**Partial** = works with stated gaps; **Deferred** = designed for, not built.

## Runtime and orchestration

| Capability | Status | Evidence / notes |
|---|---|---|
| Standalone entry point executing Tsuna's runtime (`./bin/tsuna`, no `omp`) | Implemented | demo, `src/cli.ts` |
| Pi SDK session backend behind an adapter | Implemented | `src/backend/pi.ts`; no reads of `~/.pi` (spike + demo HOME check) |
| External agent packs, validation, provenance | Implemented | `test/definitions.test.ts` |
| Trusted role identity bound to the runtime record (no global env) | Implemented | `gateSessionFor`, definitions/policy tests |
| Spawn (`task`), parallel dispatch (`dispatch`), foreground + background | Implemented | orchestration tests, demo |
| Per-parent concurrency 4, max depth 2, deadlock-free nesting | Implemented | “parallel dispatch …”, “nested delegation …”, “maximum depth …” |
| Permit release on success/error/cancel/startup failure | Implemented | “startup failure and cancellation release permits” |
| Sibling results retained when one child fails | Implemented | “one failing child does not erase …” |
| Agent list / activity, transcript + accepted output reads | Implemented | `agent_list`, `agent_read`, `/agents`, `/read` |
| Steering a running turn vs follow-up assignment | Implemented | steering/follow-up tests, demo steps 4 and 6 |
| Delivered / queued / revived / rejected outcomes | Implemented | messaging tests |
| Interrupt (session kept) vs permanent cancel (cascades) | Implemented | “interrupt keeps the session; cancel is permanent …” |
| Generation guards; stale late results rejected | Implemented | same test (late provider answer after cancel) |
| Park / revive with coalescing; no dispose during acquisition | Implemented | “concurrent park and revive …” |
| Structured terminal + incremental results, schema validation | Implemented | smoke test, sample pack output schemas |
| Mixed tool batch cannot finalize a child (D7) | Implemented | “a terminal yield mixed with other tool calls …” |
| Settlement from Pi `agent_settled` / `prompt()` | Implemented | adapter; yield reminder ladder test |
| Delivery contract (task/wait/steer/prompt; transcript-marker reconciliation) | Implemented | delivery tests; restart reconciliation in `restore` |
| Ownership-scoped, bounded `wait`; no context polling | Implemented | wait tests |
| Persistence + restart recovery of tree and full contract | Implemented | restart tests, demo step 8 |
| Fail-closed recovery (tampered/corrupt/missing metadata, changed model) | Implemented | “missing or corrupt recovery metadata fails closed” |
| Recoverable interruption on crash; no automatic replay | Implemented | “in-flight runs become recoverable interruptions …” |
| Cross-root isolation of messaging and transcript reads | Implemented | “cross-session messaging and transcript reads are rejected” |
| Single-process ownership of a root (lock with PID birth identity) | Implemented | restart test (second open refused) |
| Worktree isolation / apply / merge / commit | Deferred | contract stores `cwd`; automatic commits/merges intentionally absent |
| OMP Agent Hub parity, completion probes, workpools, speculative launch, IRC broadcast | Deferred | not claimed |

## Policy

| Capability | Status | Evidence / notes |
|---|---|---|
| Ordered last-match-wins rules, explicit deny, missing match = deny | Implemented | `test/policy.test.ts` |
| External-directory checks (git worktree aware) | Implemented | policy tests, demo step 9 |
| Role-specific capabilities from definitions; catalog filtering + execution-time gate | Implemented | policy + MCP tests |
| Shiori workplan ownership | Implemented | MCP test with Shiori-shaped fixture (`--write-approval client` semantics) |
| Interactive approvals for the primary; headless fail-closed | Implemented | CLI pty smoke (prompt → deny → audit), gate tests |
| Unknown tools / unconfigured MCP origins denied | Implemented | policy + MCP tests |
| Enforcement on built-ins, MCP calls/resources, nested `batch`, spawn/message/read/cancel, filesystem, shell, native ops | Implemented | `invokeTool` is the single path; tests per surface |
| Exec-capable flag guard (`rg --pre`, `fd -x`, `find -exec`, `sed -i`, `xargs`) | Implemented | policy tests; see RUST-AND-CLI-TOOLS.md |
| Harness-level deny rules (cannot be overridden by packs) | Implemented | policy tests |
| Model-based approval reviewer | Partial | gate interface + tests with a stub reviewer (generic asks only, never overrides deny, failure preserves ask); not wired to a live model |
| OS sandbox | Not provided | application policy only |

## MCP, native engines, supervision

| Capability | Status | Evidence / notes |
|---|---|---|
| Separate MCP config, enabled/disabled preserved, no discovery | Implemented | MCP tests |
| stdio + streamable HTTP (`@earendil-works/pi-mcp`) | Implemented | stdio and HTTP fixtures |
| Explicit single workspace root | Implemented | roots probe test |
| Credentials only via env allowlist | Implemented | HTTP fixture test |
| Cancellation + shutdown of connections | Implemented | MCP tests (server pids exit), demo |
| Live Shiori / gofetch / researcher-mcp / Context7 / lsp-tools | Not verified | prerequisites in README; Go 1.27.1 unavailable here; no live credentials used |
| pi-natives grep/glob/hashline tag, loader isolated to state dir | Implemented | natives tool tests; demo HOME check |
| Stale-edit rejection (content tag) | Implemented | natives tool tests |
| Supervised shell (exit / timeout / cancel / startup-failure / unknown), restart-readable, `--id` single execution | Implemented | `test/jobs.test.ts`, Rust tests |
| Custom slim natives bindings crate | Deferred | footprint recorded in DESIGN §8 |

## Context and UI

| Capability | Status | Evidence / notes |
|---|---|---|
| Manual tool-result compression (transcript unchanged) incl. replay on resume | Implemented | `test/context.test.ts` |
| Image budgeting with persisted decisions | Implemented | context tests |
| Cache advisories | Implemented (advisory events) | context tests |
| Role reasoning preferences, bounded `[reasoning:…]` hints, backend capability check | Implemented | definitions/orchestration tests |
| Project instructions and skills discovery (Tsuna folders only) | Implemented | context tests |
| Fork / branch replay | Partial | transforms are pure functions of messages + stored decisions; branch navigation is not exposed in the UI |
| Interactive REPL, headless JSONL, scripted human mode, agent view, transcript view, steering/stop/revive | Implemented | demo, pty smoke |
| Rich TUI | Deferred | intentionally modest |
| Live-provider smoke test | Not run | no credentials authorised; `api` provider type implemented but unexercised |

## Models

| Capability | Status | Evidence / notes |
|---|---|---|
| Logical model entries separate from agent definitions | Implemented | providers config; definitions name entries |
| Deterministic fixture provider (tests, demo) | Implemented | all suites |
| `api` provider (Pi's built-in wire clients, e.g. CLIProxyAPI) | Partial | implemented; not exercised live |
| `omp` provider: OMP's wire clients (Anthropic Messages, OpenAI-compatible, …) | Implemented (offline) | `test/omp-models.test.ts` against a local SSE fixture: tool calls, thinking + signature replay across restart, effort mapping, abort |
| Catalog-validated limits and reasoning levels (OMP catalog) | Implemented | same file: unknown model, inflated window, unsupported level rejected |
| Keys only from configured env; no OMP auth-storage/env fallback | Implemented | same file |
| OMP OAuth, account pools, usage, Cursor/Copilot device flows | Deferred | need OMP credential stores; out of scope without authorisation |
