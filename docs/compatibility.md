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

All seven commands have guaranteed `/hoshi-<name>` forms. Short forms are registered when OMP does not reserve the name. `/hoshi-agent` changes a primary role; `--agent` selects the initial one. Bundled OMP commands continue to use OMP semantics.

Claude traffic uses `cliproxy-anthropic` with the Anthropic Messages API. OpenAI traffic uses `cliproxy-openai` with the Responses API. Base model IDs and metadata come from the pinned OMP catalog; synthetic OpenCode `-1m` suffixes are removed. Model access, actual request limits, subscription availability and billing are only established after account login and live requests. No completion requests were sent during setup.

## Permissions

The bridge preserves last-match-wins action rules and Shiori's role ownership. OMP tool names map to source actions. Source `ask` rules confirm in interactive primary sessions and fail closed in headless children. URL, skill and MCP-resource reads have separate routes from filesystem paths.

The original OpenCode shell scanner is not embedded: literal `&&` chains are checked segment by segment, while more complex shell syntax requires interactive confirmation. General Eval, unknown tools, and unverified internal mutation paths are denied rather than silently bypassing imported policies. Native LSP can still be reached through the imported LSP MCP. This deliberately restricts some OMP features until their policy mapping is implemented.

## Data and reproducibility

- All eight MCP definitions are retained; the five enabled definitions keep their enabled state. Local server copies are included. Disabled DeepWiki, ATS-tailor and grep.app remain disabled.
- Shiori stays at `03a1d9a4786d0f7270775d958365f4c8f8b51e02`; setup builds a missing binary. MCP roots select the current project.
- Source instruction/skill bodies are adapted for OMP. `scripts/import-opencode.ts` is a bootstrap importer, not a bidirectional sync: rerunning it replaces adapted generated files. Review changes before using it again.
- `hoshi-omp setup` regenerates runtime config from tracked templates; put permanent edits in `config/` and agent files, not generated `.runtime/omp/agent/config.yml`.
- Root settings, native defaults and extension APIs are pinned to OMP 18.8.0. Upgrade only after rerunning the tests and smoke checks.
