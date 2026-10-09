//! `tsuna-supervisor`: experiment-local supervised jobs.
//!
//! Usage: tsuna-supervisor --root STATE_DIR job <start|status|wait|cancel> ...
//! The root is mandatory and must be an absolute, existing directory owned by
//! the experiment; there is no implicit default and no `TSUNA_ROOT` fallback.

mod jobs;

use anyhow::{Context, Result, bail};
use std::ffi::OsString;
use std::path::PathBuf;

fn main() {
    match run() {
        Ok(code) => std::process::exit(code),
        Err(error) => {
            eprintln!("tsuna-supervisor: {error:#}");
            std::process::exit(2);
        }
    }
}

fn run() -> Result<i32> {
    let args: Vec<OsString> = std::env::args_os().skip(1).collect();
    if args.first().and_then(|a| a.to_str()) != Some("--root") {
        bail!("usage: tsuna-supervisor --root STATE_DIR job <start|status|wait|cancel> ...");
    }
    let root = PathBuf::from(args.get(1).context("--root requires a directory")?);
    jobs::run(&root, &args[2..])
}
