# Tsuna harness (experiment)

An isolated, standalone coding-agent harness that combines Tsuna's policies, roles and job
supervision with orchestration behaviour adapted from Oh My Pi, running on the Pi SDK. It executes
its own runtime: it does **not** launch `omp`, replace the installed `tsuna` command, or read the
installed Tsuna profile.

> Experimental. Policy here is application-level enforcement inside the harness process, **not an
> operating-system sandbox**.

* Design and decisions: [`docs/DESIGN.md`](docs/DESIGN.md)
* What works / partial / deferred: [`docs/CAPABILITIES.md`](docs/CAPABILITIES.md)
* Licences and provenance: [`docs/PROVENANCE.md`](docs/PROVENANCE.md), [`NOTICE`](NOTICE)
* Rust and modern CLI tools study: [`docs/RUST-AND-CLI-TOOLS.md`](docs/RUST-AND-CLI-TOOLS.md)

## Requirements

Bun 1.4.2 (validated), Rust 1.89+ (validated with 1.97), git. Linux x64 validated; macOS is the
reference platform for Tsuna but this experiment was not run there. No credentials, gateway or
network access are needed for the tests or the demo.

## Setup

```sh
cd experiments/tsuna
bun install --frozen-lockfile        # project-local; package cache in ./.cache (see bunfig.toml)
bun run build:native                 # builds native/supervisor into ./.cache/cargo-target
```

## Run

Always invoke the experiment by path:

```sh
./bin/tsuna --config fixtures/demo/tsuna.config.json --cwd /path/to/project           # interactive
./bin/tsuna --config CONFIG --cwd DIR --headless --prompt "…" --format jsonl            # headless events
./bin/tsuna --config CONFIG --cwd DIR --headless --script commands.tsuna                # scripted human
./bin/tsuna --config CONFIG --cwd DIR --resume ROOT_ID                                  # after a restart
./bin/tsuna roots                                                                       # list root sessions
```

Interactive commands (`/help`): `/agents`, `/read`, `/steer`, `/followup`, `/interrupt`, `/cancel`,
`/resume`, `/park`, `/wait`, `/audit`, `/mcp`, `/root`, `/quit`; plain text prompts the primary agent
(or steers it while it runs). Permission prompts appear inline (`allow? [y/N]`). Headless runs never
prompt: anything that needs approval is denied.

### State and configuration

* State root precedence: `--state DIR` > `TSUNA_EXPERIMENT_STATE` > `experiments/tsuna/.state/`.
  `TSUNA_ROOT` is ignored and unset by the launcher; a state dir that resolves inside the installed
  reference, `~/.omp`, `~/.pi`, `~/.config/tsuna`, `~/.config/hoshi-omp` or `~/.config/hoshi-secrets`
  is refused.
* Config precedence: CLI flags > `--config` / `TSUNA_EXPERIMENT_CONFIG` / `experiments/tsuna/tsuna.config.json` > defaults.
* Three separate inputs: **agent packs** (`agentPacks`, e.g. `agent-packs/sample`), **providers**
  (`providers.json`: provider + logical model entries; keys only via `apiKeyEnv`), **MCP servers**
  (`mcp.json`; `enabled:false` entries are kept but never started; `${VAR}` only from `envAllow`).

Example live-provider entry (not exercised in validation; CLIProxyAPI is optional and its live
instance is never modified):

```json
{ "providers": { "gateway": { "type": "api", "api": "openai-completions", "baseUrl": "http://127.0.0.1:18317/v1", "apiKeyEnv": "TSUNA_PROXY_KEY" } },
  "models": { "primary": { "provider": "gateway", "id": "claude-opus-5-5", "reasoning": true, "reasoningLevels": ["low","medium","high"], "contextWindow": 200000, "maxTokens": 32000 } } }
```

### MCP service prerequisites (integration targets)

Service source code, builds and credentials stay outside the harness. Point `mcp.json` at
binaries you build from the reference repository:

| Server | Type | Prerequisite |
|---|---|---|
| Shiori (`workplan`) | stdio `shiori mcp --write-approval client` | `vendor/shiori` needs Go ≥ 1.27.1 (this environment has 1.24.7, so real Shiori was **not** run; a Shiori-shaped fixture was) |
| gofetch, researcher-mcp | stdio | Go builds of `mcps/gofetch-mcp`, `mcps/researcher-mcp` |
| lsp-tools | stdio `node mcps/lsp-tools-mcp/mcp-no-idle.mjs` | `npm ci && npm run build` in that directory, plus language servers |
| Context7 | http `https://mcp.context7.com/mcp` | `CONTEXT7_API_KEY` in the environment, listed in `envAllow` |

## Validate

```sh
bun run typecheck
bun run test:native          # Rust supervisor tests
bun run test                 # Bun suite (fixture model, temp workspaces, local MCP fixtures)
bun run demo                 # reproducible two-process offline demonstration
```

Tests run with a throwaway HOME (`test/setup.ts`) and explicit `./test` paths.

## Layout

```
bin/tsuna                launcher (by path only)
src/                     harness (see docs/DESIGN.md §2)
agent-packs/sample/      external sample agent pack (removable)
fixtures/demo/           demo config, providers, MCP config, deterministic model, session scripts
fixtures/mcp/            stdio and streamable-HTTP MCP fixture servers
native/supervisor/       Rust supervised-job CLI
test/                    automated tests
docs/                    design, capabilities, provenance, studies
```
