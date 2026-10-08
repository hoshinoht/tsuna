# Setup and profile connection

Run the [README quick start](../README.md#quick-start) from a local checkout. The
CLI version in that example is the validated official OMP release; the SDK in
`package.json` is pinned separately.

## Requirements

| Component | Used for |
|---|---|
| Bun | OMP, dependencies and Hoshi scripts |
| Go 1.27.1 or newer | Building missing local MCP binaries; see the vendored `go.mod` files |
| Node.js 20 or newer | The LSP MCP server |
| Docker with Compose | The CLIProxyAPI gateway |
| Pandoc and a TeX installation | Document compilation; optional for coding workflows |

`bun run setup` builds missing Go binaries, links agents and skills, and generates
the runtime profile from tracked configuration. Keep permanent settings in
`config/`; rerunning setup replaces generated runtime settings.

## Connect the default profile

> [!IMPORTANT]
> `bun scripts/connect-official.ts` switches the official CLI's default profile
> by pointing `~/.omp/agent` at this checkout's `.runtime/omp/agent`.

An existing default profile is preserved at `~/.omp/agent-before-hoshi-*`.
Connecting again to the same checkout leaves the link intact. Keep the checkout
in place while using this profile. Existing Hoshi sessions stay in the runtime
directory; this step does not import OpenCode conversations.

`bun scripts/install-path.ts` creates `~/.local/bin/hoshi-omp` and refuses to
replace an unrelated command. Add `~/.local/bin` and `~/.bun/bin` to your shell's
PATH. The README's `export PATH=...` applies to the current shell.

## Authenticate and launch

`hoshi-omp proxy start` initializes private gateway files and starts the container.
Run `hoshi-omp proxy login claude` and `hoshi-omp proxy login codex`; open each
printed login URL in your browser. The configured roles use both providers.

`hoshi-omp proxy models` checks the authenticated model listing without making a
model completion request. Model availability and account limits depend on your
provider accounts. See the [gateway guide](../proxy/README.md).

Launch `hoshi-omp` or `omp` from the project directory. The Hoshi launcher selects
the default orchestrator model; `/hoshi-agent orchestrator` selects that role
inside an existing session. Use `/hoshi-review` for Hoshi's review workflow;
the native `/review` command retains OMP's behavior.

## Project instructions and skills

The project-context extension loads named instruction files and `skills/` from
`.opencode`, `.codex`, `.claude`, `.agents`, `.agent` and `.gemini` directories
between the Git root and the current directory. Outside Git, it checks only the
current directory. Home-level configuration is excluded.

It reads `AGENTS.md` in each folder, plus `.claude/CLAUDE.md` and
`.gemini/GEMINI.md`. Skills use OMP's native catalog and exclusions. Other files
remain readable on demand; foreign agents, commands, hooks and MCP settings
retain their existing discovery settings. See [compatibility](compatibility.md#permissions).

Restart OMP after changing extension code or its configured extension list.
For runtime updates and resuming conversations, see the [pilot guide](pilot.md).
