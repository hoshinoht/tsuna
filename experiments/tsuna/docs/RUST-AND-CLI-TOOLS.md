# More Rust? Modern CLI tools (rg, fd, bat, …)?

Study done during the experiment (2026-10-09), answering: “can we port more things to Rust, if it is
worth it, and use modern variants of POSIX tools such as ripgrep and bat?” Read-only investigation;
verdicts below are recommendations, not changes to the reference Tsuna project.

## Bottom line

* The worthwhile Rust ports in the reference are largely done (`native/tsuna`: launcher/profile,
  proxy, setup/import, supervised jobs; see `docs/rust-migration.md`). The remaining TypeScript runs
  *inside* the Bun/Pi session on every tool call or turn (policy, context transforms, project
  context) — moving it behind an N-API hop would add serialisation cost and a second copy of the
  logic without a speed gain. Rule of thumb: standalone CLIs and long-running supervisors → Rust;
  in-session hooks → TypeScript.
* `@oh-my-pi/pi-natives@18.8.6` already embeds modern tools in-process: content search on ripgrep's
  library crates (`grep-searcher`, `grep-regex`, `ignore`), fd-style walking (`ignore`, `globset`),
  syntect highlighting (the engine behind bat), Myers/`similar` diffs, `ast-grep-core`, html→markdown,
  and, through its embedded brush shell, builtin `rg` (15.1.0), `fd` (10.4.2), `jq` (jaq), `sed`,
  `find`, `diff`, `ls` and ~70 uutils commands. No `bat`/`eza`/`delta`/`dust`/`hyperfine` CLI.
* On this Linux host only `rg` 14.1.0, `jq` and `yq` are installed; `/usr/bin/sg` is the shadow-utils
  switch-group command, **not** ast-grep.

## Verdicts

| Component | Verdict | Why |
|---|---|---|
| Policy engine (`lib/permissions.ts`, harness `src/policy`) | keep TS | µs-scale, needs SDK tool shapes and session identity |
| Context transforms (`lib/runtime.ts`) | keep TS | rewrites JS message objects every turn; FFI would be slower |
| Project context, usage-core, docs package | keep TS | trivial I/O or wraps pandoc |
| Supervisor (`jobs.rs` → `native/supervisor`) | Rust (done) | process groups, deadlines, exit truth; now the harness `bash` backend |
| `mcps/lsp-tools-mcp` (5k lines TS) | maybe later | single binary, but large rewrite for little user gain |
| gofetch / researcher-mcp / Shiori (Go) | keep | already static binaries; Shiori is vendored upstream |
| Own slim search/highlight addon (`grep-searcher`, `ignore`, `globset`, `syntect`, `similar`) | maybe later | trigger: the ~183 MB addon load, or the licence question below |

## Integration options for the harness

1. **pi-natives in-process (chosen).** `grep`/`glob` tools use it; same behaviour on every host;
   builtin `rg` rejects `--pre`. Costs: 366 MB on disk (two x64 variants), ~183 MB loaded.
2. **External binaries when present.** Fine for human-facing display (bat/delta/difftastic), never
   something agents depend on. Behaviour varies by host/version. Licences (MIT, Apache-2.0,
   Unlicense) are GPL-3.0-compatible and separate programs raise no linking question.
3. **Own crate.** All candidate crates are MIT or MIT/Apache; 1–2 weeks including the platform build
   matrix. Deferred.

## Security finding acted on

Whole-string shell rules such as `shell rg *` also match `rg --pre=/bin/sh …` (runs an arbitrary
program per file), `fd -x/-X/--exec`, `find -exec/-execdir/-ok/-delete` and `sed -i`/`e`/`w`. The
copied OMP chain extractor rejects only `-c/-e/--command/--eval`. The harness policy now treats these
exec-capable flags as requiring approval (headless children are denied) regardless of the role's
allow rule; see `execCapableReason` in `src/policy/engine.ts` and its tests. The same gap exists in the
reference profile's `scholar` rule (`"rg *"`); not changed there (reference is read-only).

## Licence caveat (not legal advice)

The pi-natives `THIRD-PARTY-NOTICES.txt` lists `inferno` (CDDL-1.0), apparently only for its
profiling path. CDDL is generally considered GPL-incompatible, so *redistributing* the prebuilt addon
inside a GPL-3.0-or-later bundle is a grey area worth an owner decision. The experiment installs the
addon from npm as a separate dependency and does not redistribute it.

## Recommended next steps

1. Keep the exec-flag guard and extend tests per tool as allowlists grow (done for rg/fd/find/sed).
2. Implement harness `grep`/`glob` on pi-natives (done) and consider `executeShell` builtins for
   read-only commands so `rg`/`fd`/`jq` behave identically across macOS and Linux.
3. Optional host tools for display only (bat/delta/difft), detected at runtime.
4. Revisit a slim bindings crate only if footprint or the CDDL question becomes a blocker.

Not verified: licence strings of tools not installed locally (from crates.io knowledge); whether
inferno is reachable at runtime.
