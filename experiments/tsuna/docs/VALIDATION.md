# Validation record (2026-10-09, Linux x64, Bun 1.4.2, Rust 1.97.0)

All checks below were run in this environment and passed unless stated. Nothing here used a live
model, live credentials or the network (other than installing pinned packages).

| Command (from `experiments/tsuna`) | Result |
|---|---|
| `bun install --frozen-lockfile` | lockfile unchanged |
| `bun run typecheck` | 0 errors |
| `bun run test:native` | 10 Rust tests passed |
| `cargo fmt --check`, `cargo clippy --all-targets -- -D warnings` (supervisor) | clean |
| `bun run test` | **120 passed, 0 failed** across 10 files |
| `bun run demo` | 16/16 verification checks passed (two separate processes) |
| pty smoke of the interactive REPL | `/agents`, `/mcp`, unknown-agent error, inline approval prompt → user “n” → denied + audited |

Per file: context 17, definitions 23, jobs 4, mcp 6, natives-tools 9, omp-models 6, orchestration 22,
policy 23, review-fixes 9, smoke 1.

OMP model layer (added after the first review): real OMP Anthropic-Messages and OpenAI-Completions
clients against `fixtures/models/llm-server.ts` through full Tsuna/Pi sessions — delegation round
trip, signed-thinking replay after restart, catalog validation, key isolation, abort. Not run against
a live endpoint.

## What the suite demonstrates (acceptance list)

Parallel dispatch limit (4 per parent) · nested delegation without deadlock (4×4 under depth 2) ·
startup failure and cancellation release permits · sibling results retained on failure · steering a
running child · follow-up continues the same session · stale updates after cancel rejected ·
concurrent park/revive coalesce and never dispose during acquisition · restart restores tree and
contract · missing/corrupt/tampered metadata fails closed (transcript still readable) · permissions
enforced after revival · cross-root messaging/reads rejected · delivery exactly once per documented
contract (task, wait, steer, prompt) · ownership-scoped bounded wait · mixed tool batch cannot
finalize · MCP calls (and nested batch MCP calls) gated · Shiori-shaped `client` write approval only
via the gate and role ownership · context transforms preserve transcript and tool pairing, replayed
after resume · stale edits rejected · supervisor exit/timeout/cancel/startup-failure/unknown
distinct · caller restart observes a surviving job without replay · killed supervisor / PID reuse →
unknown promptly · duplicate start for one id never runs twice · shutdown releases sessions, MCP
processes, the root lock · tests and natives never touch the real `~/.omp`/`~/.pi`.

## Independent review

A separate reviewer audited `src/` and `native/supervisor` against DESIGN.md. Findings and dispositions:

| Finding | Disposition | Regression test |
|---|---|---|
| B1 nested `batch` inputs unvalidated → path policy bypass | **Fixed**: every invocation validated against its schema before the gate; non-string path/command fields denied | review-fixes B1 |
| B2 unparseable shell commands skipped harness/role denies; exec-flag check skipped segment denies | **Fixed**: fragment-level deny checks, never reviewer-waivable, exec check after deny checks | review-fixes B2, policy |
| M1/M2 interrupt/shutdown of a queued run marked the agent `failed` | **Fixed**: only a failure to open the session is a startup failure | review-fixes M1/M2 |
| M3 interrupting a parent blocked in foreground `task` hung | **Fixed**: `task`/`dispatch` honour the abort signal and interrupt foreground children | review-fixes M3 |
| M4 concurrent CLI approvals stranded | **Fixed**: FIFO approval queue; unanswered approvals deny at exit | (manual pty check) |
| m1 delivery confirmed by any session's echo | **Fixed**: only the parent's non-assistant messages, only `delivering` results | review-fixes m1 |
| m2 prompt-taken results stuck `delivering`; steer revert too broad | **Fixed**: per-attempt delivery ids, revert on prompt failure | orchestration delivery tests |
| m3 MCP processes leaked when start failed | **Fixed**: MCP shut down on start failure | — |
| m4 truncated job ids could collide; not root-scoped | **Fixed**: sha256 of (root, agent, call id) | jobs |
| m5 lock-creation race | **Fixed**: owner record written then hard-linked into place | review-fixes m5 |
| m6 hash did not cover parent/depth; transcript path unconfined; later harness denies not applied; crash mid-cancel | **Fixed** (hash covers id/root/parent/depth; transcript must be under the agent's session dir; current denies are added at gate time; cancel cascade completed at recovery). Unkeyed hash and tool narrowing are documented decisions D10/D11 | review-fixes m6 |
| Rejected yields could loop forever (found by the test writer) | **Fixed**: 5 rejected terminal yields → failure result | review-fixes |
| Primary agent never got context transforms (found by the test writer) | **Fixed**: transforms constructed before the primary session opens | context |
| Notes: `agent_read` scope is the whole root tree; `bash` env inherited secrets | read scope kept (matches the reference); **env now scrubbed to an allowlist** | review-fixes env |

## Not verified

* Live providers (CLIProxyAPI or direct APIs): no credentials were authorised; the `api` provider
  type is implemented but unexercised.
* Real Shiori, gofetch, researcher-mcp, lsp-tools, Context7: Go 1.27.1 is not available here and live
  services need credentials; fixtures stand in.
* macOS (the reference platform): only Linux x64 was run. The supervisor's macOS identity code is
  inherited unchanged from the reference.
* Natural teardown of every Pi process-global resource: the suite verifies owned sessions, MCP child
  processes and locks; Pi's global API registry and module-level state persist for the process
  lifetime by design.
* Reference working-tree differences at `/Users/cantabile/.config/tsuna`: not accessible here.
