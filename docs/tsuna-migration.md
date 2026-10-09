# Tsuna rename

The project is Tsuna (綱), with repository `git@github.com:hoshinoht/tsuna.git`.
Its CLI is `tsuna`, Rust crate is `native/tsuna`, and canonical checkout location
is `~/.config/tsuna`. This is a naming change; existing behavior, private runtime
data and unrelated working-tree changes are preserved.

New commands use `/tsuna-*` and configuration uses `TSUNA_*` environment
variables (`TSUNA_ROOT` identifies the checkout). Saved Hoshi role/compression
entries remain readable so existing conversations retain their state.

The old checkout path remains a filesystem link to keep existing absolute
references valid. The official `~/.omp/agent` link points at the same runtime
directory under the new checkout. Credentials and sessions are moved with the
checkout, not regenerated, and generated profile settings receive only the
necessary naming/path edits.

Docker's existing Compose project/container identity (`hoshi-omp` and
`hoshi-omp-gateway`) and the shared `~/.config/hoshi-secrets` directory retain
their names to preserve the running gateway and shared credential references.
Historical import records, source snapshots and attribution retain their names.

The destination repository retains the checkout's existing history, and
`origin` points at the Tsuna URL. The migration commits contain the Rust/Tsuna
work; unrelated local permission and MCP changes remain uncommitted.

Restart OMP after migration to load the renamed extension and commands. The
gateway does not need a restart for this rename.

Validated on macOS: 25 Rust tests, 61 Bun unit tests and 2 SDK integration tests
on the staged snapshot, plus typecheck, formatting and clippy. The broader local
working tree also passes its 69 unit tests and the same 2 integration tests.

SDK integration checks run in a separate process. The smoke runner disposes its
session, closes the stores/caches it owns, and uses the SDK's CLI shutdown API
for process-wide cleanup. Passing assertions alone is not treated as completion.

The path migration verifies that the Git directory, runtime profile, proxy state
and credential-file inodes are preserved. Generated runtime files receive only
path/extension edits; their original copies are retained in a private ignored
`.runtime/tsuna-rename-*` backup.
