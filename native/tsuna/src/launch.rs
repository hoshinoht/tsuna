use anyhow::{Context, Result, bail};
use serde_json::Value;
use std::ffi::{OsStr, OsString};
use std::fs;
use std::os::unix::process::CommandExt;
use std::path::{Path, PathBuf};
use std::process::Command;

pub fn home() -> Result<PathBuf> {
    std::env::var_os("HOME")
        .filter(|v| !v.is_empty())
        .map(PathBuf::from)
        .context("HOME is required")
}

pub fn bun() -> OsString {
    std::env::var_os("TSUNA_BUN_BIN").unwrap_or_else(|| "bun".into())
}

fn relative_path(from: &Path, to: &Path) -> PathBuf {
    let from: Vec<_> = from.components().collect();
    let to: Vec<_> = to.components().collect();
    let common = from.iter().zip(&to).take_while(|(a, b)| a == b).count();
    let mut result = PathBuf::new();
    for _ in common..from.len() {
        result.push("..");
    }
    for part in &to[common..] {
        result.push(part.as_os_str());
    }
    result
}

fn build_command(
    root: &Path,
    home: &Path,
    args: &[OsString],
    official: &Path,
    bun: &OsStr,
) -> Result<Command> {
    let mut args = args.to_vec();
    let index = args.iter().position(|arg| arg == "--agent");
    let mut role = "orchestrator".to_owned();
    if let Some(index) = index {
        role = args
            .get(index + 1)
            .and_then(|s| s.to_str())
            .context("--agent requires a role")?
            .to_owned();
        args.drain(index..=index + 1);
    }
    if role == "build" {
        role = "orchestrator".into();
    }
    let roles: Value = serde_json::from_slice(&fs::read(root.join("config/roles.json"))?)?;
    let model = roles
        .get(&role)
        .and_then(|r| r.get("model"))
        .and_then(Value::as_str)
        .with_context(|| format!("Unknown Tsuna role: {role}"))?;
    let agent_dir = root.join(".runtime/omp/agent");
    if !agent_dir.join("config.yml").is_file() {
        bail!("Run tsuna setup first");
    }
    let official_target = fs::canonicalize(official).ok();
    let use_official = official_target.is_some()
        && official_target
            != std::env::current_exe()
                .ok()
                .and_then(|p| fs::canonicalize(p).ok())
        && ["tsuna", "hoshi-omp"]
            .iter()
            .all(|name| official_target != fs::canonicalize(root.join("bin").join(name)).ok());
    let mut command = if use_official {
        Command::new(official)
    } else {
        let mut cmd = Command::new(bun);
        cmd.arg(root.join("node_modules/@oh-my-pi/pi-coding-agent/dist/cli.js"));
        cmd
    };
    let resumes = args
        .iter()
        .filter_map(|a| a.to_str())
        .any(|a| ["--resume", "-r", "--continue", "-c"].contains(&a) || a.starts_with("--resume="));
    if index.is_some() || !resumes {
        command.args(["--model", model]);
    }
    let key = match fs::read_to_string(root.join(".runtime/proxy/client-key")) {
        Ok(key) => key,
        Err(err) if err.kind() == std::io::ErrorKind::NotFound => String::new(),
        Err(err) => return Err(err.into()),
    };
    command
        .args(args)
        .env("TSUNA_ROOT", root)
        .env("TSUNA_AGENT", role)
        .env(
            "TSUNA_AGENT_EXPLICIT",
            if index.is_some() { "1" } else { "0" },
        )
        .env("TSUNA_PROXY_KEY", key.trim())
        .env(
            "PI_CONFIG_DIR",
            if use_official {
                PathBuf::from(".omp")
            } else {
                relative_path(home, &root.join(".runtime/omp"))
            },
        )
        .env(
            "PI_CODING_AGENT_DIR",
            if use_official {
                home.join(".omp/agent")
            } else {
                agent_dir
            },
        )
        .env("OMP_PROFILE", "")
        .env("PI_PROFILE", "");
    Ok(command)
}

pub fn run(root: &Path, args: &[OsString]) -> Result<i32> {
    let home = home()?;
    let official = std::env::var_os("TSUNA_OMP_BIN")
        .map(PathBuf::from)
        .unwrap_or_else(|| home.join(".bun/bin/omp"));
    let mut command = build_command(root, &home, args, &official, &bun())?;
    // Replacing the launcher lets terminal signals and exit status reach OMP directly.
    Err(command.exec()).context("Cannot launch OMP")
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn preserves_resume_model_and_explicit_role() {
        let dir = tempfile::tempdir().unwrap();
        let root = dir.path().join("repo");
        fs::create_dir_all(root.join("config")).unwrap();
        fs::create_dir_all(root.join(".runtime/omp/agent")).unwrap();
        fs::write(
            root.join("config/roles.json"),
            r#"{"orchestrator":{"model":"provider/default"},"tester":{"model":"provider/test"}}"#,
        )
        .unwrap();
        fs::write(root.join(".runtime/omp/agent/config.yml"), "{}").unwrap();
        fs::create_dir_all(root.join("bin")).unwrap();
        fs::write(root.join("bin/hoshi-omp"), "fixture").unwrap();
        let official = dir.path().join("omp");
        fs::write(&official, "fixture").unwrap();
        let command = build_command(
            &root,
            dir.path(),
            &["--resume".into()],
            &official,
            OsStr::new("bun"),
        )
        .unwrap();
        assert_eq!(
            command.get_args().collect::<Vec<_>>(),
            [OsStr::new("--resume")]
        );
        let command = build_command(
            &root,
            dir.path(),
            &["--agent".into(), "tester".into(), "--resume".into()],
            &official,
            OsStr::new("bun"),
        )
        .unwrap();
        assert_eq!(
            command.get_args().collect::<Vec<_>>(),
            ["--model", "provider/test", "--resume"]
        );
        assert!(
            command
                .get_envs()
                .any(|(k, v)| k == "TSUNA_AGENT" && v == Some(OsStr::new("tester")))
        );
        assert!(
            build_command(
                &root,
                dir.path(),
                &["--agent".into()],
                &official,
                OsStr::new("bun")
            )
            .is_err()
        );
        assert!(
            build_command(
                &root,
                dir.path(),
                &["--agent".into(), "invalid".into()],
                &official,
                OsStr::new("bun")
            )
            .is_err()
        );
        let command = build_command(
            &root,
            dir.path(),
            &["--agent".into(), "build".into()],
            &official,
            OsStr::new("bun"),
        )
        .unwrap();
        assert!(
            command
                .get_envs()
                .any(|(k, v)| k == "TSUNA_AGENT" && v == Some(OsStr::new("orchestrator")))
        );
        let fallback = build_command(
            &root,
            dir.path(),
            &[],
            &root.join("missing"),
            OsStr::new("custom-bun"),
        )
        .unwrap();
        assert_eq!(fallback.get_program(), "custom-bun");
        assert!(
            fallback
                .get_args()
                .next()
                .unwrap()
                .to_string_lossy()
                .ends_with("dist/cli.js")
        );
        let legacy = build_command(
            &root,
            dir.path(),
            &[],
            &root.join("bin/hoshi-omp"),
            OsStr::new("custom-bun"),
        )
        .unwrap();
        assert_eq!(legacy.get_program(), "custom-bun");
    }
}
