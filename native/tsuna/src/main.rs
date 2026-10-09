mod import;
mod jobs;
mod launch;
mod profile;
mod proxy;
mod setup;

use anyhow::{Context, Result, bail};
use std::ffi::OsString;
use std::os::unix::process::CommandExt;
use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};

fn no_args(args: &[OsString], command: &str) -> Result<()> {
    if !args.is_empty() {
        bail!("Usage: tsuna {command}");
    }
    Ok(())
}

fn sdk_script(root: &Path, name: &str) -> Result<i32> {
    Err(Command::new(launch::bun())
        .arg(root.join("scripts").join(name))
        .env("TSUNA_ROOT", root)
        .exec())
    .context("Cannot start OMP SDK bridge")
}

fn run() -> Result<i32> {
    let mut args: Vec<OsString> = std::env::args_os().skip(1).collect();
    let root = if args.first().is_some_and(|arg| arg == "--root") {
        let path = args.get(1).context("--root requires a directory")?.clone();
        args.drain(..2);
        PathBuf::from(path)
    } else {
        std::env::var_os("TSUNA_ROOT")
            .map(PathBuf::from)
            .unwrap_or_else(|| PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../.."))
    }
    .canonicalize()
    .context("Cannot resolve Tsuna repository root")?;
    let command = args.first().and_then(|arg| arg.to_str()).unwrap_or("");
    let rest = args.get(1..).unwrap_or_default();
    match command {
        "--help" | "help" => {
            println!(
                "Tsuna CLI\n\nUsage: tsuna [--root REPOSITORY] [COMMAND]\n\n  setup                          Prepare the OMP profile\n  proxy <init|start|stop|status|models|login claude|codex>\n  install-path                   Install the launcher symlink\n  connect-official               Connect the official OMP profile\n  import-opencode SOURCE TARGET  Import an OpenCode profile\n  check-workplan WORKSPACE ID    Validate a Shiori plan\n  smoke                          Run OMP SDK smoke checks\n  mcp-smoke                      Run MCP SDK smoke checks\n  job start --timeout SECONDS [--cwd DIR] -- COMMAND [ARGS...]\n  job status RUN_DIRECTORY\n  job wait RUN_DIRECTORY --timeout SECONDS\n\nOther arguments launch OMP, including --agent ROLE and --resume.\nUse `tsuna launch --help` for OMP's own help."
            );
            Ok(0)
        }
        "proxy" => proxy::run(&root, rest),
        "job" => jobs::run(&root, rest),
        "setup" => setup::run(&root, rest),
        "import-opencode" => import::run(&root, rest),
        "install-path" => {
            no_args(rest, command)?;
            profile::install_path(&root, &launch::home()?)?;
            Ok(0)
        }
        "connect-official" => {
            no_args(rest, command)?;
            let backup = profile::connect(&root, &launch::home()?)?;
            println!("Official OMP now uses the Tsuna agent profile and existing sessions.");
            if let Some(backup) = backup {
                println!("Previous default profile preserved at {}", backup.display());
            }
            Ok(0)
        }
        "check-workplan" => {
            if rest.len() != 2 {
                eprintln!("Usage: tsuna check-workplan <workspace-root> <workplan-id>");
                return Ok(2);
            }
            let workspace = std::path::absolute(Path::new(&rest[0]))?;
            Err(Command::new(root.join("vendor/shiori/shiori"))
                .arg("validate")
                .arg(&rest[1])
                .arg("--root")
                .arg(workspace)
                .arg("--json")
                .stdin(Stdio::null())
                .exec())
            .context("Cannot run Shiori validation")
        }
        "smoke" => {
            no_args(rest, command)?;
            setup::run(&root, &[])?;
            sdk_script(&root, "omp-smoke.ts")
        }
        "mcp-smoke" => {
            no_args(rest, command)?;
            sdk_script(&root, "omp-mcp-smoke.ts")
        }
        "launch" => launch::run(&root, rest),
        _ => launch::run(&root, &args),
    }
}

fn main() {
    match run() {
        Ok(code) => std::process::exit(code),
        Err(error) => {
            eprintln!("tsuna: {error:#}");
            std::process::exit(1);
        }
    }
}
