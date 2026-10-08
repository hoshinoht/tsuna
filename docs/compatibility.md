# Import and compatibility

Imported on 2026-10-07 from hoshi-opencode2 commit `f1586302b214c1ae84b81c7ca508cd1f20004428` plus the current working tree. Agent frontmatter supplies the active permissions; the generator YAML is preserved under `source-config/` because it has separate uncommitted edits. The source repository was not changed.

## Plugins

| Source component | OMP implementation | Difference |
|---|---|---|
| workplan-permissions | `extensions/permissions.ts` | Trusted OMP role identity; original Shiori lifecycle checks |
| reasoning-router | `extensions/runtime.ts` | Uses OMP thinking-level API rather than provider request mutation; gateway families map to original provider policies |
| openai-long-context | Generated `models.yml` | `-1m` aliases become local context limits on upstream IDs; actual upstream capacity requires a live account check |
| docs | `extensions/docs.ts` and `packages/docs` | Same 11 tools, draft storage and templates |
| subagent-control | `extensions/hoshi.ts` | Same list/stop names and direct-child restriction; uses OMP agent registry |
| image-budget | `extensions/runtime.ts` | Outgoing-context pruning, stable decisions, latest images retained |
| cache-guard | `extensions/runtime.ts` | Same configured advisory threshold, based on response cache usage |
| status-line | Native custom status line | Context, cache, rate, git, elapsed time and usage; visual layout differs |
| usage-tracker | `extensions/usage.ts`, `/hoshi-usage` | Reads gateway Claude/Codex OAuth accounts for provider quota windows and low-quota reminders. Native `/usage` remains separate; live quota endpoints require account login |
| opencode-anthropic-auth | CLIProxyAPI Claude OAuth | Independent credential store; sign in again |
| opencode-notifier | Native completion/error/ask notifications | Delivery/sound follows OMP and terminal support |
| opencode-dcp | `compress` tool | Explicit tool-result summaries only; all user messages and stored history retained. No automatic dedup/error purge. Not DCP's full message-range algorithm |
| quota-fallback | Not enabled | Source plugin was not registered; OMP native fallback chains remain available |
| workplan-tools/native adapter | Shiori MCP | Source interfaces were not registered; Go core and compatible plan formats retained |

OpenCode desktop CSS/themes and TUI plugin modules cannot execute in OMP. Their functional settings are mapped where supported; original `cli.json` and DCP settings are retained for reference. The OpenCode-specific plugin-development skill is retained but excluded from the skill catalog.

## Commands and models

`build` is merged into `orchestrator`, whose default reasoning is medium. Small tasks are handled directly; complex work uses planning and specialists as needed. Former build commands target orchestrator. The launcher accepts `--agent build` as a legacy alias, and saved build sessions restore as orchestrator. Restart OMP and select `/hoshi-agent orchestrator` to apply the model and effort to an existing session.

All seven commands have guaranteed `/hoshi-<name>` forms. Short forms are registered when OMP does not reserve the name. `/hoshi-agent` changes a primary role; `--agent` selects the initial one. Bundled OMP commands continue to use OMP semantics.

Claude traffic uses `cliproxy-anthropic` with the Anthropic Messages API. OpenAI traffic uses `cliproxy-openai` with the Responses API. Base model IDs and metadata come from the pinned OMP catalog; synthetic OpenCode `-1m` suffixes are removed. Model access, actual request limits, subscription availability and billing are only established after account login and live requests. No completion requests were sent during setup.

## Permissions

`extensions/project-context.ts` automatically reads named instruction files and discovers `skills/` in project-local `.opencode`, `.codex`, `.claude`, `.agents`, `.agent` and `.gemini` folders. It loads `AGENTS.md` in each folder, plus `.claude/CLAUDE.md` and `.gemini/GEMINI.md`. Discovery runs from the Git root to the current directory (only the current directory outside Git); it excludes home-level configuration. Skills use OMP's native catalog, duplicate-name handling and exclusions, with runtime-only paths refreshed on session start/switch. Instruction contents refresh each turn. Other files remain available through ordinary reads; agents, commands, hooks, MCP configuration and foreign user-level sources keep their existing discovery settings. Restart OMP to load the extension.

The bridge preserves last-match-wins action rules and Shiori's role ownership. OMP tool names map to source actions. Source `ask` rules confirm in interactive primary sessions and fail closed in headless children. URL, skill and MCP-resource reads have separate routes from filesystem paths.

The original OpenCode shell scanner is not embedded: literal `&&` chains are checked segment by segment. Automatic mode permits ordinary pipelines, heredocs and substitutions while retaining destructive/publishing guards and explicit denials. Source mode preserves confirmation for complex syntax. General Eval, browser and memory tools, unknown tools, and unverified mutation paths remain denied until their policy mapping is implemented. Native LSP can still be reached through the imported LSP MCP.

Coordination reads (`history://<id>` and `agent://<id>`) require a registered target in the caller's current persisted session tree. The transcript index (`read history://`) is routed to a filtered virtual index so other sessions' transcripts are not listed. Output reads are pinned to the target's owned artifact/transcript instead of searching global artifact directories for a matching name. Inline selectors, output JSON paths, and parked children are supported; advisor transcripts stay in the human-facing Agent Hub. `history://current/full` remains subject to OMP's experimental context setting.

`write agent://<id>` uses the source parent-control permission and only permits non-aborted direct children in that same tree. Native OMP handles message delivery and parked-child revival. Broadcasts, unrelated sessions, advisor targets and edit-tool mutations are denied. Missing caller/target provenance fails closed. Restart OMP to load changed extension code; Alt+A opens the native Agent Hub for human steering.

GPT-6-Luna reviews pending primary-session approvals through CLIProxyAPI. `config/permission-reviewer.json` enables auto mode: only a low-risk verdict with scope `within-request` can waive a generic Hoshi ask for bounded inspection. Specific asks, denials, credential access, mutations and arbitrary scripts keep their existing controls. Native OMP approval events receive advisory reviews; native prompt policies still apply. Disabled/missing models, malformed output and the 15-second timeout preserve manual approval. Common credentials are redacted; audit entries record verdict, usage and approval source without command text.

The official Bun installation owns `omp` and `omp update`. `~/.omp/agent` points to the existing repository-local profile so agents, sessions, MCPs and themes stay together. The previous default directory is preserved by `scripts/connect-official.ts`. CLIProxyAPI keys use OMP's command-backed credential resolver, which reads the private client-key file without embedding its value in configuration. `hoshi-omp` remains the role-selection convenience launcher and uses the same official runtime.

## Data and reproducibility

- All eight MCP definitions are retained; the five enabled definitions keep their enabled state. Local server copies are included. Disabled DeepWiki, ATS-tailor and grep.app remain disabled.
- Shiori stays at `03a1d9a4786d0f7270775d958365f4c8f8b51e02`; setup builds a missing binary. MCP roots select the current project.
- Source instruction/skill bodies are adapted for OMP. `scripts/import-opencode.ts` is a bootstrap importer, not a bidirectional sync: rerunning it replaces adapted generated files. Review changes before using it again.
- `hoshi-omp setup` regenerates runtime config from tracked templates; put permanent edits in `config/` and agent files, not generated `.runtime/omp/agent/config.yml`.
- The development SDK/catalog remains pinned in `package.json`; the official Bun runtime was installed and updated to v18.8.3. Hoshi extension loading and the closed editor were validated with the official source installation. Rerun adapter tests and an interactive smoke check after runtime upgrades.
