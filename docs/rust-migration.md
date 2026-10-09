# Rust script migration

Approved scope, 2026-10-09: migrate the standalone launcher and scripts to a
single Rust CLI, and implement a supervised job runner for restart recovery.
Keep TypeScript only for OMP's in-process extension, catalog and smoke APIs.
Existing `bun scripts/*.ts` entry points remain thin CLI compatibility shims.
No commits, publishing, service restarts or replacement of active jobs are part
of this migration.

## Stages and ownership

1. Process supervisor: `native/tsuna/src/jobs.rs` and its tests. Provide start,
   status and bounded wait, preserve real exit status, impose command deadlines,
   and report missing/dead supervisors as unknown rather than fabricated success.
   Validate with real child processes and temporary run directories.
2. Proxy controls: `native/tsuna/src/proxy.rs` and its tests. Preserve private
   credentials, configuration, Docker arguments and bounded HTTP health checks.
   Validate with temporary files, fake commands and a local HTTP server.
3. Import and setup: `native/tsuna/src/import.rs`, `setup.rs`, catalog bridge and
   their tests. Preserve semantic JSON/YAML configuration and prompt content.
   YAML serialization formatting may change; parsed values must match. Validate
   generated fixture trees and installed OMP catalog integration.
4. Parent integration: CLI dispatch, launcher, installation/profile connection,
   workplan validation, script shims, smoke entry points and documentation.
   Run Rust tests, formatting, clippy, TypeScript checks and repository tests.
5. Independent review: correct actionable findings, rerun affected checks, and
   record final evidence and remaining limitations here.

## Implementation constraints

- Preserve unrelated working-tree edits and existing user credentials.
- Local filesystem and subprocess operations move to Rust. SDK-owned values
  are obtained from a small Bun bridge rather than copied into Rust.
- Use one Cargo package (`native/tsuna`) and a locked dependency graph. Reuse
  standard library facilities; use existing cached crates for structured data,
  HTTP, secure random bytes and signal handling.
- The supervisor owns its test child in a separate process group. Native job
  tracking is preferred; explicit detached jobs use a separate supervisor
  session, unique run directories and atomically published status.
- A killed supervisor cannot promise an exit record. Readers must distinguish
  running, completed, timed out and unknown/interrupted, and enforce their own
  wait deadline. Do not infer success from a missing record or PID alone.

## Supervised commands

```sh
tsuna job start --timeout 900 --cwd /absolute/project -- bun run e2e
# Use the absolute run_directory printed by start:
tsuna job status /absolute/tsuna/.runtime/jobs/RUN_ID
tsuna job wait /absolute/tsuna/.runtime/jobs/RUN_ID --timeout 60
```

Commands run in the caller's working directory unless `--cwd` is provided;
relative `--cwd` paths also resolve from the caller's directory. Each run stores
command metadata, separate stdout/stderr logs and status JSON in
a private directory. The detached supervisor retains the real child exit code
or signal. Deadline expiry terminates the child's process group. Waiting has a
separate deadline and does not cancel the command: exit 124 denotes a deadline,
125 denotes unknown/startup failure, and completed commands retain their exit
code. Read the JSON state to distinguish these from a command returning 124/125.

Lost supervisors and reused PIDs produce unknown status, without rewriting a
terminal record. Process identity is checked using kernel start times. This is
restart recovery for the caller; SIGKILL of the supervisor itself can still
leave a child running, so unknown does not mean the command has stopped.

## Further tooling assessment

- Add a read-only `job diagnose` / `logs` command next: combine status, supervisor
  identity and bounded log tails so agents do not reconstruct recovery commands.
- Add a read-only `doctor` command for generated-profile and local MCP executable
  readiness. Keep provider requests opt-in and OMP SDK checks in the Bun bridge.
- Keep Go servers (Shiori, gofetch and researcher) in Go: they already own their
  recovery, deadlines and provider logic. No measured migration benefit has been
  established. Keep LSP, document, usage and OMP extension code in TypeScript for
  their existing SDK integration.
- Stabilize the job CLI's status schema before exposing it through an MCP adapter.

## Validation

The local implementation is complete and independently reviewed. On macOS:

- 24 Rust tests passed, including real supervised child processes and localhost
  HTTP authentication/deadline checks (the HTTP listener needs sandbox permission).
- 68 Bun tests passed, including Rust setup through the real Bun/OMP catalog and
  SDK session in a temporary profile, quoting paths with apostrophes, compatibility
  script invocation and preservation of a prior official profile.
- TypeScript typecheck, LSP diagnostics, Rust formatting and clippy with warnings
  denied passed. No unrelated working-tree changes were reverted.

The existing smoke assertion assumed source-mode permissions even though the
default profile now permits ordinary external reads in auto mode. Its isolated
SDK process explicitly selects source mode for that approval assertion.

Live Docker/OAuth, Linux execution and killing/restarting a live OMP harness were
not exercised. No user service or session was restarted. Extension changes take
effect after restarting OMP. The project was renamed to Tsuna on 2026-10-09;
see [the rename notes](tsuna-migration.md).
