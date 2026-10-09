# tsuna-sample agent pack

External agent definitions for the Tsuna harness experiment. The harness loads this directory only
because the experiment's config lists it in `agentPacks`; delete or replace the directory (and the
config entry) without changing harness code.

* Format: `pack.json` + `agents/<name>.md` (YAML frontmatter + prompt body). See `docs/DESIGN.md` §3.
* Provenance: prompt bodies are condensed adaptations of Tsuna `agent/prompts/{orchestrator,explore,
  code-engineer,code-checker,tester}.md` at `92c4728`; permission rules are translated from Tsuna
  `config/permissions.json` (same project, GPL-3.0-or-later). Rules use the harness action names
  (`subagent_message`, `subagent_resume`, `job_cancel`, `mcp_resource`, …).
* `model` values are logical entries resolved by the separate provider config, never provider IDs.
* This pack imports nothing from the harness.
