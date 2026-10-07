# hoshi-omp trial port

Requested 2026-10-07: import the full current hoshi-opencode2 configuration into a fresh hoshi-omp repository. Keep the OpenCode source and its uncommitted changes intact. On 2026-10-08 the user authorized local commits of the current code; push and remote publication remain unauthorized.

Source: /Users/cantabile/.config/opencode at f1586302b214c1ae84b81c7ca508cd1f20004428 plus its current working tree. OMP is pinned to 18.8.0.

Build and validation workspace: /private/tmp/hoshi-omp.YqKMeF. Final repository: /Users/cantabile/.config/hoshi-omp. All runtime paths must be relocatable. Do not embed staging paths in configuration.

## Stage ownership

- Parent: configuration/agent/skill/command import, launcher, dependency installation, provenance, documentation, integration and final sync.
- Permissions implementer: extensions/permissions.ts, lib/permissions.ts and tests/permissions.test.ts only. Preserve ordered per-agent policies and Shiori ownership checks using trusted session identity. Unknown mutation channels fail closed. Verify permission enforcement with meaningful tests.
- Documents implementer: packages/docs/**, extensions/docs.ts and tests/docs-adapter.test.ts only. Reuse the existing host-independent document service and assets with attribution; replace OpenCode registration with the OMP extension API. Validate tool execution and cancellation.
- Runtime implementer: extensions/runtime.ts, lib/runtime.ts and tests/runtime.test.ts only. Port image-budget and cache-guard behavior and configured reasoning bounds where OMP hooks allow. Prefer native usage, subagent and compression features to duplicate implementations. Report any parity gap precisely.

## Acceptance

All 17 source agents, seven commands, available skills and all MCP definitions are accounted for, including disabled definitions. Source configuration and permissions remain unchanged. Shiori connects against the current project, with lifecycle role checks. Document tools work. Native replacements and remaining differences are documented. Test the actual OMP loader/discovery and an offline session with stubbed model behavior, then perform a live local MCP smoke check without spending model credits. A new user login may remain necessary; do not print or copy credentials into the repository.

## Review

After integration, independently review the significant port for behavior, missing components and permission gaps. Fix actionable findings and run relevant checks. Preserve source licenses. Publish nothing.

## Delivered

Configuration import, permission adapter, document adapter, runtime policies, worker controls, manual compression and quota adapter are implemented. CLIProxyAPI runs as a digest-pinned Docker service; `hoshi-omp` and `omp` PATH shortcuts launch the isolated repository-local profile. Independent review passed after URI routing and fresh-install build fixes. See docs/validation.md for evidence and docs/compatibility.md for differences. Provider sign-in and live model validation remain user-side steps; no account credentials were copied from OpenCode.
