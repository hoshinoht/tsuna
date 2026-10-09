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
before role rules and cannot be overridden. Gate order: deny → headless fail-closed → optional reviewer
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
See `CAPABILITIES.md` for the implemented/partial/deferred matrix and validation evidence.
