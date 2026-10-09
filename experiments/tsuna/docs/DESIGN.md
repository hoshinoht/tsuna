# Tsuna harness experiment — design and decision record

Status: experimental. This document is the plan *and* the running decision log; entries marked
**Decision** record consequential choices and their effect.

## 1. Baseline (verified 2026-10-09)

| Item | Value | How verified |
|---|---|---|
| Reference Tsuna | `92c4728936e3a06115f00809710d2d0e19662ea6` (`master`) | `git log` in the clone at `/home/user/tsuna` (cloud checkout; the macOS reference workspace `/Users/cantabile/.config/tsuna` is not present in this environment, so no working-tree differences were inspected or adopted) |
| Pi | `@earendil-works/*@1.1.0`, tag `v1.1.0` = `abe508e1b89912adde45528136c3221eb69acdd7` | npm registry + upstream clone |
| OMP | `@oh-my-pi/*@18.8.6`, tag `v18.8.6` = `f068751e2f1dbdbc195977776d47a26db8697495` | npm registry + upstream clone |
| Bun | 1.4.2 (Linux x64 in this environment; reference validated on macOS) | `bun --version` |
| Rust | 1.97.0 | `cargo --version` |

**Decision D1 — versions.** Use the newest *released* versions observed (Pi 1.1.0, OMP natives 18.8.6),
pinned exactly in `package.json` and `bun.lock`; never a branch. Pi 1.1.0 is the stable SDK named in the
brief; OMP 18.8.6 is the release whose sources were studied. The reference's OMP 18.8.0 SDK is not used
at runtime at all (the harness does not depend on `@oh-my-pi/pi-coding-agent`).

## 2. Architecture

```
bin/tsuna ──> src/cli.ts ──> Harness (src/harness.ts)
                               ├─ config      src/config.ts         Tsuna-owned settings/providers (precedence §6)
                               ├─ paths       src/paths.ts          state root + isolation guard
                               ├─ agents      src/agents/           external definition packs (loader/validator)
                               ├─ policy      src/policy/           rules engine, intents, gate, approvals
                               ├─ backend     src/backend/          Pi SDK adapter + fixture model provider
                               ├─ runtime     src/orchestration/    agent tree, lifecycle, scheduler, results, store
                               ├─ tools       src/tools/            fs/search (pi-natives), shell (supervisor), batch
                               ├─ mcp         src/mcp/              MCP manager over @earendil-works/pi-mcp
                               ├─ context     src/context/          transforms, project instructions, skills
                               ├─ jobs        src/jobs/             client for native/supervisor (Rust)
                               └─ ui          src/ui/               interactive REPL + headless JSONL
agent-packs/sample/   external agent definitions (removable)
fixtures/             demo configuration, fixture model scripts, fixture MCP servers
native/supervisor/    Rust supervised-job CLI (adapted from Tsuna jobs.rs)
```

Dependency direction: `ui -> harness -> {orchestration, tools, mcp, context} -> {policy, backend, agents, jobs}`.
Agent packs and MCP servers import nothing from the harness.

**Decision D2 — Pi as backend, Tsuna owns the loop around it.** Each agent is one Pi `AgentSession`
created with `createAgentSession` and *only* Tsuna custom tools (`tools:[...]`, `customTools`), an
in-memory `SettingsManager` (retry/compaction off by default, cache warming off), an in-memory
credential store, a `DefaultResourceLoader` with every discovery switch off (`noExtensions`,
`noSkills`, `noPromptTemplates`, `noThemes`, `noContextFiles`) and an `agentDir` inside Tsuna state, and a
file `SessionManager` writing JSONL under Tsuna state. The spike (scripts in the history of this doc)
confirmed no writes to `$HOME` or `~/.pi`. Settlement uses Pi's `agent_settled` event / `prompt()`
resolution, never `agent_end` or `turn_end`.

**Decision D3 — policy enforced inside Tsuna tool execution.** Every Tsuna tool's `execute` calls the
`PermissionGate` before acting; Pi's `tool_call` hook is not the enforcement point (it is per-session
and does not cover direct runtime calls). The same `invokeTool` path is used for nested `batch` calls
and MCP calls, so nested execution cannot bypass policy. Catalog filtering is additional.

**Decision D4 — no OMP coding-agent runtime dependency.** OMP helpers are copied with attribution
(`Semaphore`, the literal `&&` shell-chain extractor) or re-implemented from studied behaviour
(registry CAS, park/revive coalescing, wait semantics). Native engines come from the prebuilt
`@oh-my-pi/pi-natives@18.8.6` (see §8).

## 3. Agent definitions (stage 2)

Packs: `<pack>/pack.json` + `<pack>/agents/<name>.md` with YAML frontmatter
(`name, description, model, reasoning, tools, spawns, permissions, primary, output`) and the prompt body.
Only directories listed in the trusted Tsuna config are loaded. Unknown keys, name/file mismatch,
duplicate agent or pack names and unknown spawn targets are rejected. Each definition records
`{pack, packVersion, file, sha256}`.

Trusted identity: a running agent's role, policy and contract are stored in the runtime's
`AgentRecord`, bound to its session object. Tool inputs never carry identity; there is no
process-global role variable.

**Decision D5 — the contract snapshot is authoritative for recovery.** At spawn the runtime records a
contract `{role, definition provenance, system prompt, model entry + resolved provider/id, reasoning,
tools, spawns, permission rules, harness denies, cwd, output schema, approval mode}` and its sha256.
Recovery restores exactly that snapshot; it fails closed (status `failed`, transcript still readable)
if the hash does not verify, the model entry no longer resolves to the same provider/id, a tool is no
longer available, the workspace is gone, or the parent chain is broken. A later edit to the pack
cannot widen a restored child (drift is reported, not applied).

## 4. Orchestration (stages 3–4)

### Identity and tree
Agent IDs are `<role>-<n>-<4 hex>` unique per root; `rootId` names a root session tree. Each record
has `parentId`, `depth` (root = 0) and a session JSONL path.

### Scheduling
**Decision D6 — per-parent permits (OMP model).** Each agent that spawns owns a `Semaphore(4)`; a
child's *run* (not its existence) holds one permit from its parent's semaphore. Max depth is 2 (root's
children are depth 1, grandchildren depth 2; depth-2 agents cannot spawn). A parent never acquires from
its own pool, so a parent waiting on children cannot deadlock them; worst-case concurrent runs per root
= 4 + 4×4. Permits are released in `finally` on success, error, cancellation and startup failure; an
aborted acquire leaves the queue (copied OMP `Semaphore`).

### Lifecycle
`queued → running → idle ⇄ parked`, plus terminal `cancelled` and `failed`.
* `idle`: run settled, session live. `parked`: session disposed, revivable from JSONL + contract.
* `interrupt`: aborts the current run, keeps the session (`idle`), run result = failure `interrupted`.
* `cancel`: permanent; disposes, cascades to descendants, rejects every later update.
* `failed`: the agent cannot continue (startup failure, unrecoverable contract). Ordinary run errors
  produce a failure *result* and leave the agent `idle` (eligible for follow-up).
* Generation guards: every session attach bumps `generation`; every run gets `runId`. Async completions
  compare `(generation, runId)` and drop stale updates; `cancelled` is sticky.
* Revival coalesces per agent (`revivals` map); parking detaches synchronously before awaiting dispose,
  refuses while a run or revival is in flight, and revival awaits any in-flight dispose.
* After a Tsuna restart, agents recorded as `running`/`queued` become `parked` with
  `interruptedRunId` (recoverable interruption). Nothing is replayed automatically.

### Results and delivery
Boundaries are explicit: (1) `yield` produced output; (2) the yield was schema-validated (rejected
yields return an error to the model); (3) the run *settled* (`agent_settled`), at which point the last
valid terminal yield becomes the accepted result `resultId = <agentId>/r<runSeq>` and is persisted;
(4) delivery to the parent.

**Decision D7 — terminal yield must be the only tool call in its assistant message.** A terminal
`yield` in a mixed batch is rejected (siblings still execute) so a child cannot finalize before seeing
sibling results. Incremental `yield` (`final:false`) may be mixed.

**Decision D8 — delivery contract.** A result is delivered by exactly one of: the foreground `task`
return, a `wait` return, a steer into a running parent, or injection into the parent's next prompt.
The store moves `pending → delivering(deliveryId) → delivered` under a per-root lock; within one
process this is exactly-once. On restart, `delivering` entries are reconciled by searching the
parent's JSONL for the result marker (`[tsuna-result <resultId>]`): found → `delivered`, absent →
`pending`. A crash can therefore at most re-offer a result whose marker never reached the parent's
transcript.

### Messaging
`agent_send {id, message, mode: steer|followup}`; agent callers may address only their **direct,
non-cancelled children** (Tsuna's existing restriction); the human UI may address any agent in its own
root. Outcomes: `delivered` (steer into a running turn, or follow-up started), `queued` (steer to an
idle agent, held for its next run; follow-up while running), `revived` (parked agent restarted for the
follow-up), `rejected` (with reason). Cross-root IDs are rejected as unknown.

### Waiting
`wait {ids?, timeout_seconds ≤ 600}` covers only the caller's direct children. It returns immediately
with undelivered results; otherwise it blocks until a child result, a message for the caller,
cancellation or the bound, then returns per-child partial status. It never injects polling messages
into context. External processes are waited with `job_wait`, which reads supervisor state only.

### Isolation
Children share the parent's workspace (reference profile: no automatic isolation, apply, merge or
commit). The contract stores `cwd` so a later worktree mode can record its own; worktree isolation is
**deferred**.

## 5. Permissions (stage 5)
Engine adapted from Tsuna `lib/permissions.ts` (ordered last-match-wins, explicit deny, missing match
= deny, external-directory checks with git-worktree awareness, Shiori ownership, shell `&&` segments,
auto-mode generic asks, destructive guards). Harness-level deny rules (Tsuna config) are evaluated
before role rules and cannot be overridden; a restored agent gets its recorded denies plus the
*current* config's denies. Every invocation, nested `batch` calls included, is validated against the
tool schema before policy evaluation. Shell commands that cannot be split into literal `&&` segments
are checked fragment by fragment against harness denies and explicit role denies and are never
reviewer-waivable; exec-capable flags (`rg --pre`, `fd -x`, `find -exec`, `sed -i`, `xargs`) always
need approval. Supervised commands get a scrubbed environment (no provider keys). Gate order: deny → headless fail-closed → optional reviewer
(generic asks only; failure preserves the ask) → human approver. Unknown tools and unconfigured MCP
servers are denied. Application policy only — not an OS sandbox.

Action names for harness tools (differences from OMP/Tsuna are intentional; OMP routes these through
`agent://`/`proc://` URLs):

| Tool | Action / resource |
|---|---|
| read | `read <path>` |
| write, edit | `edit <path>` |
| grep, glob | `grep|glob <path>` (now path-canonicalised and external-checked; reference did not) |
| bash | `shell <command>` |
| task, dispatch | `subagent <agent>` per item |
| agent_list, agent_read | `subagent_list <id>` |
| agent_send | `subagent_message <id>` |
| agent_interrupt, agent_cancel | `subagent_stop <id>` |
| agent_resume | `subagent_resume <id>` |
| job_cancel | `job_cancel *` |
| mcp__S__T | `S_T *` (configured servers only) |
| mcp_resource | `mcp_resource <server>` |
| wait, yield, batch, job_status, job_wait | coordination (ownership enforced by runtime) |

## 6. Configuration precedence
1. CLI flags (`--config`, `--state`, `--cwd`, `--role`).
2. The Tsuna config file named by `--config`, else `TSUNA_EXPERIMENT_CONFIG`, else
   `<experiment>/tsuna.config.json` if present. Relative paths resolve against the config file.
3. Built-in defaults (no agent packs, no MCP servers, fixture model only when configured).
`TSUNA_ROOT`, OMP YAML profiles, `~/.pi`, OpenCode/Claude/Codex folders are never read as
configuration. Project instruction files (`AGENTS.md` in the workspace's Tsuna-listed folders) are
prompt content, not configuration, and cannot add agents, MCP servers or executable providers.

## 7. MCP (stage 6)
Separate `mcp.json`; `enabled:false` entries are kept and reported, never started. stdio and
streamable HTTP via `@earendil-works/pi-mcp`. Each client advertises exactly one root: the workspace
(`file://<cwd>`), which Shiori requires. `${VAR}` placeholders resolve only from an explicit
`env` allowlist per server; no credentials are copied. Tool names `mcp__<server>__<tool>` (Pi's
separator; OMP used a single `_`). Every call and resource read passes the gate; calls take an abort
signal; shutdown closes every client.

## 8. Native engines
`@oh-my-pi/pi-natives@18.8.6` (MIT) provides `grep`, `glob` and `hashlineFileHash`. The loader prunes
older version directories under `~/.omp/natives` after every load; Tsuna sets `PI_NATIVES_DIR` to
`<state>/natives` before importing it. Footprint: the JS package is 1.3 MB but the Linux x64 leaf
package ships two full addon variants (modern + baseline) totalling 366 MB; a narrow import still loads
a ~183 MB addon containing PTY, audio, WebRTC, image and other unrelated engines. A custom bindings
crate is deferred.

External processes use `native/supervisor` (adapted from Tsuna `jobs.rs`): state under
`<state>/jobs`, `--id` gives one result identity per tool call (a second start returns the existing
record, never a second process), `cancel` publishes `cancelled`. Agent lifecycle and process lifecycle
are separate: the runtime owns agents; the supervisor owns processes.

## 9. Stages and status
See `CAPABILITIES.md` for the implemented/partial/deferred matrix and `VALIDATION.md` for the
exact commands, results, independent-review dispositions and what was not verified.

## 9a. OMP model layer (`omp` provider type)

**Decision D14 — OMP's model API as a provider adapter, Pi sessions unchanged** (owner's choice,
2026-10-09). A Tsuna provider with `"type": "omp"` streams through Oh My Pi's own provider
implementations (`@oh-my-pi/pi-ai@18.8.6`: self-contained wire clients for Anthropic Messages, OpenAI
Responses/Completions, Codex, Gemini, Bedrock, Ollama, …) and validates every model entry against
OMP's bundled catalog (`@oh-my-pi/pi-catalog@18.8.6`). `src/backend/omp-models.ts` registers it on
Pi's `ModelRuntime` as a `streamSimple` provider:

* **Context:** Pi's transcript (system messages carrying prompt + tool deltas) → OMP `Context`
  (`systemPrompt[]`, `messages`, `tools`); Tsuna-owned assistant turns get OMP's `api`/`provider`
  back so OMP can replay provider-native state (thinking signatures, response ids). Turns from other
  providers pass through for OMP's own cross-provider transform.
* **Events:** OMP and Pi share the assistant event protocol; messages are re-labelled with the Tsuna
  provider identity and keep `upstreamApi`/`upstreamProvider`.
* **Catalog authority:** context window and max output come from the catalog and may only be
  narrowed; `reasoningLevels` must be a subset of the catalog's supported efforts; a definition whose
  reasoning preference the model does not support is rejected at spawn (never translated); unknown
  catalog models fail harness start.
* **Credentials:** only from the env var named by `apiKeyEnv`, passed per request. A missing key fails
  the request before dispatch; OMP's auth storage (`~/.omp` agent.db), OAuth flows and its own
  env-variable map (`ANTHROPIC_API_KEY`, …) are never consulted.
* **Isolation:** OMP's logger defaults to a rotating file in `~/.omp/logs`; Tsuna disables the file
  transport and forwards warnings. Provider in-flight file leases are only used when limits are
  configured, which Tsuna never does. Tests assert no `~/.omp` after real OMP requests.
* **Not adopted:** OMP account pools, OAuth/login, usage tracking, auth gateway/broker, Cursor/Copilot
  device flows (they need OMP's credential stores).
* **Footprint:** pi-ai 11 MB, pi-catalog 17 MB, pi-utils 3.4 MB, pi-wire 1.3 MB, omptype 2 MB (source,
  run directly by Bun); natives already present.

## 10. Compatibility findings (verified against the pinned revisions)

| Finding from the brief | Result | Evidence | Effect on the harness |
|---|---|---|---|
| OMP and Pi use different MCP tool-name separators | **Confirmed.** OMP/Tsuna: `mcp__<server>_<tool>` (Tsuna maps via a fixed server list, `lib/permissions.ts:44-53`); Pi: `mcp__<server>__<tool>` (`packages/coding-agent/src/extensions/mcp/tools.ts`) | source reads | Harness uses Pi's double separator and derives server names from `mcp.json`, so no hard-coded server list |
| Tsuna prompt hooks return arrays where Pi expects strings | **Confirmed.** `extensions/project-context.ts:19-26` and `extensions/tsuna.ts:48-54` return `systemPrompt: [...]`; Pi's `before_agent_start` result `systemPrompt` is a `string` assigned without a type check | Pi `extensions/types.ts`, `runner.ts` | Harness composes one system-prompt string per contract; no prompt hooks |
| Coordination depends on OMP registry / internal-URL APIs | **Confirmed.** `lib/coordination.ts` imports `AgentRegistry`, `internal-urls/*`, `registry/persisted-agents`, `session/session-loader` | source | Replaced by explicit runtime tools with their own policy actions |
| Permission module imports OMP shell-tokenization | **Confirmed.** `lib/permissions.ts:11` imports `extractLiteralAndChainSegments` | source | Function copied with MIT notice (`src/policy/shell-tokenize.ts`) |
| Several adapters use Bun-specific APIs | **Confirmed** (e.g. `import.meta.dir`, `Bun.spawn`); Pi declares `node >=22.19` but works on Bun 1.4.2 in every test here | test suite | Harness is Bun-only by design (launcher runs `bun`) |
| Native OMP approval events do not exist in Pi | **Confirmed.** Pi offers a `tool_call` block hook but no approval/UI-confirm event in SDK sessions | Pi SDK research | Approvals are entirely Tsuna's gate (`src/policy/gate.ts`) |
| Shiori trusts client-side write approval | **Confirmed** (`--write-approval client` → `clientApproved.Authorize` returns nil, `vendor/shiori/internal/mcp/server.go`) | source | Proven with a Shiori-shaped fixture that only gate-approved writes reach the server (MCP tests). Real Shiori not run (Go 1.27.1 unavailable). Retaining `client` mode is safe **only** behind this gate |
| Pi 1.1.0 / OMP 18.8.6 versions | **Confirmed** as the latest published releases on 2026-10-09 | npm registry | pinned exactly |
| pi-natives writes to shared locations | **Found:** after every load it prunes older version dirs under `~/.omp/natives` (or `PI_NATIVES_DIR`), and writes `__PI_NATIVE_VARIANT_CACHE` into `process.env` | `native/loader-state.js` | `PI_NATIVES_DIR` set to `<state>/natives` before import |
| Pi `dispose()` does not emit `session_shutdown` and does not wait for idleness | **Confirmed** | `agent-session.ts:1393-1414` | adapter aborts then disposes; MCP clients are owned and closed by the harness, not by Pi extensions |
| Pi faux provider queue is process-global FIFO | **Found** | `providers/faux.ts` | fixture provider appends one identical dispatcher per request, so concurrent agents never consume each other's scripted turns |

## 11. Additional decisions made during implementation

* **D9 — explicit coordination tools.** OMP exposes `task`, `wait`, hidden `yield`, and routes messaging,
  listing and cancellation through `agent://`/`proc://` URLs on `read`/`write`. Tsuna exposes
  `task`, `dispatch`, `agent_list`, `agent_read`, `agent_send`, `agent_interrupt`, `agent_cancel`,
  `agent_resume`, `wait`, `yield`, so each operation has a distinct permission action.
* **D10 — integrity, not authenticity, for state.** The contract hash detects corruption and accidental
  edits. Anyone able to write the 0700 state directory can recompute it; state is trusted local data.
* **D11 — capability narrowing on recovery is allowed, widening is not.** A contract tool that is no
  longer available (e.g. an MCP server down) is omitted with a warning; the recorded role, rules and
  model must match exactly or recovery fails closed.
* **D12 — follow-up results and the parent.** A human follow-up to a child produces a result whose
  parent is still the child's parent; it stays `pending` until the parent's next `wait`, prompt or
  running turn (visible as `delivery pending` in `/read`).
* **D13 — background jobs outlive the harness by design.** Supervised commands started with
  `background:true` keep running across a harness restart and are observed (never replayed) afterwards;
  foreground commands are cancelled when their tool call is aborted.

## 12. Known limitations and recommended next stages

1. **Live validation**: run the same scripts against CLIProxyAPI (`api` provider) with the owner's
   authorisation; verify reasoning-level mapping per real model.
2. **Real MCP services**: build Shiori (Go ≥ 1.27.1), gofetch, researcher-mcp and lsp-tools; repeat
   the MCP suite against them, including Shiori's own precondition checks.
3. **Reviewer**: wire the model-based approval reviewer to a configured model entry (interface and
   safety rules already tested with a stub).
4. **Worktree isolation**: optional per-child worktrees recorded in the contract; revival must fail
   closed when the worktree is gone.
5. **Natives footprint/licence**: decide on the CDDL `inferno` question; consider a slim bindings
   crate (see RUST-AND-CLI-TOOLS.md).
6. **UI**: branch/fork navigation, richer agent view; the current REPL is deliberately modest.
7. **Delivery**: the marker-based reconciliation assumes the parent's JSONL is the source of truth;
   a dedicated delivery entry type would be more robust than text markers.
8. **macOS run** of the full suite (reference platform).
9. **OMP model layer live check**: point an `omp` provider at CLIProxyAPI (`baseUrl`) or a real API
   with the owner's key and repeat `test/omp-models.test.ts`-style checks; decide whether to adopt
   OMP account pools/OAuth behind an explicit Tsuna credential adapter.
