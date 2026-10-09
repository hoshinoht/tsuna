use serde_json::Value;
use std::path::Path;
use std::process::{Command, Output};

fn cli(root: &Path, args: &[&str]) -> Output {
    Command::new(env!("CARGO_BIN_EXE_tsuna"))
        .arg("--root")
        .arg(root)
        .args(args)
        .output()
        .expect("run tsuna test CLI")
}

fn start(root: &Path, command: &[&str]) -> Value {
    let mut args = vec!["job", "start", "--timeout", "5", "--"];
    args.extend_from_slice(command);
    let output = cli(root, &args);
    assert!(
        output.status.success(),
        "start failed: {}",
        String::from_utf8_lossy(&output.stderr)
    );
    serde_json::from_slice(&output.stdout).expect("start prints JSON")
}

#[test]
fn cli_preserves_exit_codes_and_bounds_waits() {
    let root = tempfile::tempdir().expect("create test root");
    let nonzero = start(root.path(), &["/bin/sh", "-c", "exit 7"]);
    let run_directory = nonzero["run_directory"].as_str().expect("run directory");
    let waited = cli(
        root.path(),
        &["job", "wait", run_directory, "--timeout", "5"],
    );
    assert_eq!(waited.status.code(), Some(7));
    let status: Value = serde_json::from_slice(&waited.stdout).expect("wait prints JSON");
    assert_eq!(status["state"], "completed");
    assert_eq!(status["exit_code"], 7);

    let running = start(root.path(), &["/bin/sh", "-c", "sleep 2"]);
    let run_directory = running["run_directory"].as_str().expect("run directory");
    let bounded = cli(
        root.path(),
        &["job", "wait", run_directory, "--timeout", "1"],
    );
    assert_eq!(bounded.status.code(), Some(124));
    let still_running: Value = serde_json::from_slice(&bounded.stdout).expect("wait prints JSON");
    assert_eq!(still_running["state"], "running");

    let completed = cli(
        root.path(),
        &["job", "wait", run_directory, "--timeout", "5"],
    );
    assert!(completed.status.success());
    let status: Value = serde_json::from_slice(&completed.stdout).expect("wait prints JSON");
    assert_eq!(status["state"], "completed");
}

#[test]
fn cli_rejects_unbounded_or_malformed_job_commands() {
    let root = tempfile::tempdir().expect("create test root");
    let zero = cli(
        root.path(),
        &["job", "start", "--timeout", "0", "--", "/bin/true"],
    );
    assert!(!zero.status.success());
    assert!(String::from_utf8_lossy(&zero.stderr).contains("greater than zero"));

    let extra = cli(root.path(), &["job", "status"]);
    assert!(!extra.status.success());
    assert!(String::from_utf8_lossy(&extra.stderr).contains("usage"));
}

#[test]
fn jobs_use_the_callers_project_directory() {
    let root = tempfile::tempdir().unwrap();
    let project = tempfile::tempdir().unwrap();
    std::fs::create_dir(project.path().join("child")).unwrap();
    for cwd_args in [vec![], vec!["--cwd", "child"]] {
        let started = Command::new(env!("CARGO_BIN_EXE_tsuna"))
            .arg("--root")
            .arg(root.path())
            .args(["job", "start", "--timeout", "5"])
            .args(&cwd_args)
            .args(["--", "/bin/pwd"])
            .current_dir(project.path())
            .output()
            .unwrap();
        assert!(
            started.status.success(),
            "{}",
            String::from_utf8_lossy(&started.stderr)
        );
        let run: Value = serde_json::from_slice(&started.stdout).unwrap();
        let directory = run["run_directory"].as_str().unwrap();
        assert!(
            cli(root.path(), &["job", "wait", directory, "--timeout", "5"])
                .status
                .success()
        );
        let expected = if cwd_args.is_empty() {
            project.path().to_path_buf()
        } else {
            project.path().join("child")
        };
        let actual = std::fs::read_to_string(Path::new(directory).join("stdout.log")).unwrap();
        assert_eq!(
            Path::new(actual.trim()).canonicalize().unwrap(),
            expected.canonicalize().unwrap()
        );
    }
}
