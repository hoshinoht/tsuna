# Paused on 2026-10-07

The user requested: “lets wrap up for now.” The full port goal is paused, not complete.

## Installed and usable

Repository: `~/.config/hoshi-omp`. As of 2026-10-08, `omp` is the official Bun installation at `~/.bun/bin/omp`; `hoshi-omp` remains the role-selection convenience launcher in `~/.local/bin`. The official default agent directory links to `.runtime/omp/agent` to retain the Hoshi profile and conversations. Earlier default data and the former PATH shortcut were backed up. CLIProxyAPI remains the digest-pinned Docker gateway on localhost port 18317. Do not interrupt the user's OMP sessions or gateway on resume. See `docs/pilot.md` for the longer daily-driver pilot.

The 17-agent trial, seven commands, MCP definitions, Shiori, document adapter, quota adapter and initial runtime policies are installed. The original OpenCode repository's changes were preserved.

The latest installed fix recognizes the repository root and registered sibling Git worktrees rather than treating the launch directory as the only trusted boundary. Automatic permission mode is selected in `config/permission-mode.json`. Routine actions auto-approve; explicit denials and specific destructive/publishing/secret-file asks remain. The installed adapter passed 13 focused tests and `bun run typecheck`.

Restart to load that adapter while retaining the conversation:

```sh
hoshi-omp --agent orchestrator --continue
```

In a newly loaded session, `/hoshi-permissions auto` or `/hoshi-permissions source` selects the current-session policy. `/hoshi-agent orchestrator` changes the primary role; `/goal set <objective>` enables native continuation.

## Preserved work awaiting integration

The staged source snapshot is saved under `.staging/fidelity/`, excluding dependencies, runtime state and Git metadata. It originated at `/private/tmp/hoshi-omp-fidelity.IbuStH`; use the permanent snapshot on resume and compare it with the current installed repository before copying files.

- `extensions/runtime.ts`, `lib/runtime.ts`, runtime tests: staged child-only provider-request reasoning routing, image read-path preservation and compaction pruning, configurable cache admission. Focused mocked-provider tests passed; confirm-mode live UI still needs validation.
- `extensions/long-context.ts`, `lib/long-context.ts`, tests: staged full 1M alias logic with exact source limits, metadata cloning, fixed extended-context cap and upstream wire rewriting. Focused library/registry tests passed. The parent began setup/catalog integration, which still needs end-to-end registry and wire checks.
- `lib/models.ts`, `config/helper-roles.json`, changed importer/setup/role files: staged catalog and helper-role integration. Verify serialized model metadata, all eligible aliases, upstream wire IDs, helper model selection and helper thinking levels before installation. Do not assume typecheck proves effective helper behavior.
- `extensions/compression.ts`, `lib/compression.ts`, tests: independent range-compression implementation from a behavior specification. Five focused tests passed, but full source equivalence is not proven. Verify real block IDs versus ordinary IDs, branch/resume mapping, multiple tool-call pairing, nested blocks, nudge delivery/persistence, manual/decompress/recompress/sweep semantics and atomic failures. Replace the old `compress` registration in `extensions/hoshi.ts` only after validation. Map `dcp_inspect` in the permissions adapter.
- `packages/status-line-core/`, `extensions/status-line.ts`, tests: source pure math and colors plus an OMP widget and native VCS adapter. Fifty focused/core tests passed. Add registration/attribution and verify a live render, rates, restoration and cleanup.
- `extensions/notifier.ts`, `lib/notifier.ts`, `lib/notify-events.ts`, tests: staged notifier adapter. Its final session-scoping/focus/error/question fixes were interrupted for the pause and are unverified. Review for duplicate delivery across rebound child factories, focus suppression, headless filtering, safe subprocess handling, error/permission/question events and dedup cleanup. Copy the upstream MIT sound assets and license; wire Hoshi confirmation events and registration only after checks.
- `agent/skills/opencode-plugin-dev/SKILL.md`, canonical `config/skill-overrides/opencode-plugin-dev.md`, skill test and config: staged actual OMP development instructions under the retained skill ID. Actual discovery test proved all 18 IDs available. These updates are not installed yet.

Do not bulk-copy the snapshot or run its setup against the live profile before reviewing the changes. It contains unfinished parent integration and may contain partially edited notifier code.

## Remaining fidelity audit

The earlier startup smoke test established a usable trial, not a complete port. Remaining requirements include effective helper routing; full range DCP behavior; notifier and status UI fidelity; per-session/source-equivalent subagent control; usage-provider coverage and context reminders (including Copilot); optional/disabled source component accounting; and a complete source-to-target requirement audit.

Source: `~/.config/opencode`, commit `f1586302b214c1ae84b81c7ca508cd1f20004428` plus the working tree at import. Re-read the actual source configuration on resume. Preserve user edits in both repositories. The user authorized committing and pushing the pilot changes to their origin on 2026-10-08.

## Commit checkpoint

The installed trial is committed separately from the unfinished fidelity work. `patches/fidelity.patch` preserves the parked source changes as a reviewable patch; it has not been applied to the installed trial. Its baseline is the initial trial commit. Changes for the official profile and composer now overlap its `scripts/setup.ts` hunk, so it no longer applies cleanly to the current working tree. Review/apply it in an isolated checkout of its recorded baseline, then reconcile the new profile/composer behavior when integrating selected work. The patch and original `.staging/fidelity/` snapshot are preserved unchanged.

The 2026-10-08 pilot additions include the closed four-row editor and GPT-6-Luna permission review. Auto approval is limited to aligned low-risk bounded inspection; hard denials and specific asks stay protected. A live Luna review and official Bun interactive startup were validated. The compiled binary failed to resolve some SDK extension dependencies, so the official Bun method was selected. The pilot additions are included in the commit checkpoint.

Mouse capture was disabled in the tracked default and current generated profile on 2026-10-08 so ordinary wheel scrolling works. OMP's settings listener applies that value to a running terminal; Shift + wheel also bypasses capture if it is enabled later.

## rice-omp evaluation

The user requested evaluation only. No rice-omp files were installed. Useful candidates: CLIProxyAPI `/v1/models` discovery, separate image provider, tested Codex remote compaction and optional Hound browser MCP. Its workplan engine duplicates/replaces Shiori and uses `.omp/workplan`; preserve Shiori. Its provider forces all chat models through Codex Responses and assigns zero reference costs, so adaptation is required. Its declared GPL-2.0 terms need checking before copying code into this GPL-3.0-or-later repository; independently implement needed behavior instead of assuming a grant. Reference: https://github.com/LLJY/rice-omp (default branch `master`).
