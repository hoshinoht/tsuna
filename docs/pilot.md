# Daily-driver pilot

OMP is installed through the official Bun method. `omp` resolves to `~/.bun/bin/omp`, owned by the global OMP package. Use `omp update` to update that runtime. The former `~/.local/bin/omp` Tsuna shortcut is backed up, not placed ahead of the official command.

`~/.omp/agent` links to this repository's `.runtime/omp/agent`, preserving existing conversations, models, MCPs and themes. The earlier default directory is backed up under `~/.omp/agent-before-hoshi-*`. Gateway credentials remain in `.runtime/proxy`; model configuration contains a command that reads the private client-key file, never the key itself.

Start `omp` from your project, or `omp --continue` to resume. Select a role with `/tsuna-agent orchestrator`; `tsuna --agent orchestrator --continue` retains the earlier convenience syntax. Alt+A opens the native subagent Hub. The input box has a separate complete bottom border and four typing rows, growing to eight visible rows before scrolling.

Permission prompts use human approval. Explicit asks, destructive operations, credentials, arbitrary scripts and unknown scope remain subject to the existing controls. Native OMP prompt policies remain authoritative. `/tsuna-permissions` shows the approval mode from `config/permission-mode.json`. Code and plan reviews use Sonnet 5.5 with medium reasoning, configured in `config/roles.json`. Restart OMP after changing extension code or model configuration.

During the pilot, track false-positive prompts, subagent steering/resume reliability, context recovery, memory usage and quota overhead. Shiori remains the workplan source of truth. The broader fidelity snapshot stays parked; this pilot does not activate unfinished DCP, notifier or long-context work.
