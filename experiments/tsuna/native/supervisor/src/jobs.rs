//! Restart-safe supervision for long-running local commands.
//!
//! The parent process may disappear at any point.  A missing terminal record
//! therefore means "unknown", never a successful command.
//!
//! Adapted for the Tsuna harness experiment from Tsuna's
//! `native/tsuna/src/jobs.rs` at commit 92c4728 (GPL-3.0-or-later, same
//! author). Changes: the job root is an explicit, experiment-owned state
//! directory (`<root>/jobs`, never `.runtime/jobs`); `start --id` gives a
//! caller-chosen result identity that can never start a second worker; and
//! `cancel` requests process-group cancellation that the live supervisor
//! publishes as the terminal `cancelled` state.

use anyhow::{Context, Result, anyhow, bail};
use serde::{Deserialize, Serialize};
use std::ffi::OsString;
use std::fs::{self, OpenOptions};
use std::io::{self, Write};
use std::os::fd::AsRawFd;
use std::os::unix::fs::PermissionsExt;
use std::os::unix::process::{CommandExt, ExitStatusExt};
use std::path::{Path, PathBuf};
use std::process::{Command, ExitStatus, Stdio};
use std::thread;
use std::time::{Duration, Instant, SystemTime, UNIX_EPOCH};

const STATUS_FILE: &str = "status.json";
const CONFIG_FILE: &str = "command.json";
const STATUS_LOCK_FILE: &str = "status.lock";
const CANCEL_FILE: &str = "cancel.request";
const POLL_INTERVAL: Duration = Duration::from_millis(50);
const TERM_GRACE: Duration = Duration::from_secs(1);
const UNKNOWN_EXIT: i32 = 125;
const WAIT_TIMEOUT_EXIT: i32 = 124;

#[derive(Clone, Debug, Deserialize, Serialize, PartialEq, Eq)]
#[serde(rename_all = "snake_case")]
enum JobState {
    Starting,
    Running,
    Completed,
    TimedOut,
    StartupFailed,
    Cancelled,
    Unknown,
}

#[derive(Clone, Debug, Deserialize, Serialize)]
struct StatusRecord {
    version: u8,
    id: String,
    run_directory: String,
    state: JobState,
    supervisor_pid: Option<u32>,
    supervisor_start: Option<String>,
    started_at_ms: u64,
    finished_at_ms: Option<u64>,
    exit_code: Option<i32>,
    signal: Option<i32>,
    detail: Option<String>,
}

#[derive(Debug, Deserialize, Serialize)]
struct JobConfig {
    command: Vec<String>,
    cwd: String,
    timeout_seconds: u64,
}

#[derive(Debug, Serialize)]
struct StartOutput<'a> {
    id: &'a str,
    run_directory: &'a str,
    /// False when `--id` named an existing run: nothing new was started.
    created: bool,
}

#[derive(Debug)]
struct StartSpec {
    id: Option<String>,
    timeout_seconds: u64,
    cwd: PathBuf,
    command: Vec<String>,
}

/// Runs the `tsuna job` subcommand. `args` may contain either `job` followed
/// by a subcommand, or the subcommand alone so the top-level dispatcher can
/// hand over its remaining arguments directly.
pub fn run(root: &Path, args: &[OsString]) -> Result<i32> {
    let root = absolute_root(root)?;
    let args = if args.first().and_then(|value| value.to_str()) == Some("job") {
        &args[1..]
    } else {
        args
    };
    let Some(subcommand) = args.first().and_then(|value| value.to_str()) else {
        bail!("usage: tsuna-supervisor job <start|status|wait|cancel> ...");
    };

    match subcommand {
        "start" => start(&root, &args[1..]),
        "status" => status(&root, &args[1..]),
        "wait" => wait(&root, &args[1..]),
        "cancel" => cancel(&root, &args[1..]),
        "__worker" => worker(&root, &args[1..]),
        _ => bail!("unknown job subcommand: {subcommand}"),
    }
}

fn start(root: &Path, args: &[OsString]) -> Result<i32> {
    let spec = parse_start(args)?;
    let (run_directory, created) = create_run_directory(root, &spec)?;
    if !created {
        // The identity already exists. Never start a second worker for it;
        // report the existing record so the caller can observe or wait.
        let record = observed_status(&run_directory)?;
        println!(
            "{}",
            serde_json::to_string(&StartOutput {
                id: &record.id,
                run_directory: &record.run_directory,
                created: false,
            })?
        );
        return Ok(0);
    }
    let executable = std::env::current_exe().context("locate the tsuna executable")?;
    let stdout = OpenOptions::new()
        .create(true)
        .append(true)
        .open(run_directory.join("stdout.log"))
        .context("open job stdout log")?;
    let stderr = OpenOptions::new()
        .create(true)
        .append(true)
        .open(run_directory.join("stderr.log"))
        .context("open job stderr log")?;

    let mut command = Command::new(executable);
    command
        .arg("--root")
        .arg(root)
        .arg("job")
        .arg("__worker")
        .arg(&run_directory)
        .stdin(Stdio::null())
        .stdout(Stdio::from(stdout))
        .stderr(Stdio::from(stderr));
    // `pre_exec` runs after fork. `setsid` is async-signal-safe, and this
    // closure deliberately does no allocation, locking, or Rust I/O.
    unsafe {
        command.pre_exec(|| {
            if libc::setsid() == -1 {
                return Err(io::Error::last_os_error());
            }
            Ok(())
        });
    }

    let child = match command.spawn() {
        Ok(child) => child,
        Err(error) => {
            finish(
                &run_directory,
                JobState::StartupFailed,
                None,
                None,
                Some(format!("could not start supervisor: {error}")),
            )?;
            print_status(&read_status(&run_directory)?)?;
            return Ok(UNKNOWN_EXIT);
        }
    };
    let pid = child.id();
    let identity = wait_for_identity(pid)?;
    if let Some(identity) = identity {
        mark_running(&run_directory, pid, identity)?;
    } else if !is_terminal(&read_status(&run_directory)?.state) {
        finish(
            &run_directory,
            JobState::StartupFailed,
            None,
            None,
            Some("supervisor exited before its process identity was recorded".into()),
        )?;
    }

    let record = read_status(&run_directory)?;
    println!(
        "{}",
        serde_json::to_string(&StartOutput {
            id: &record.id,
            run_directory: &record.run_directory,
            created: true,
        })?
    );
    Ok(if record.state == JobState::StartupFailed {
        UNKNOWN_EXIT
    } else {
        0
    })
}

fn status(root: &Path, args: &[OsString]) -> Result<i32> {
    if args.len() != 1 {
        bail!("usage: tsuna job status RUN_DIRECTORY");
    }
    let run_directory = supplied_run_directory(root, &args[0])?;
    let record = observed_status(&run_directory)?;
    let exit = status_exit(&record);
    print_status(&record)?;
    Ok(exit)
}

fn wait(root: &Path, args: &[OsString]) -> Result<i32> {
    if args.len() != 3 || args[1].to_str() != Some("--timeout") {
        bail!("usage: tsuna job wait RUN_DIRECTORY --timeout SECONDS");
    }
    let run_directory = supplied_run_directory(root, &args[0])?;
    let timeout_seconds = parse_positive_seconds(&args[2])?;
    let deadline = deadline_after(timeout_seconds)?;

    loop {
        let record = observed_status(&run_directory)?;
        if is_terminal(&record.state) {
            let exit = status_exit(&record);
            print_status(&record)?;
            return Ok(exit);
        }
        if record.state == JobState::Unknown {
            print_status(&record)?;
            return Ok(UNKNOWN_EXIT);
        }
        let remaining = deadline.saturating_duration_since(Instant::now());
        if remaining.is_zero() {
            print_status(&record)?;
            return Ok(WAIT_TIMEOUT_EXIT);
        }
        thread::sleep(remaining.min(POLL_INTERVAL));
    }
}

fn cancel(root: &Path, args: &[OsString]) -> Result<i32> {
    if args.len() != 1 {
        bail!("usage: tsuna-supervisor job cancel RUN_DIRECTORY");
    }
    let run_directory = supplied_run_directory(root, &args[0])?;
    let record = observed_status(&run_directory)?;
    if record.state == JobState::Running || record.state == JobState::Starting {
        // Only a live supervisor with a verified identity acts on the request;
        // it signals its own process group and publishes `cancelled`.
        write_json_atomic(&run_directory.join(CANCEL_FILE), &now_ms())?;
    }
    print_status(&observed_status(&run_directory)?)?;
    Ok(status_exit(&observed_status(&run_directory)?))
}

fn worker(root: &Path, args: &[OsString]) -> Result<i32> {
    if args.len() != 1 {
        bail!("internal job worker accepts exactly one run directory");
    }
    let run_directory = supplied_run_directory(root, &args[0])?;
    run_worker(&run_directory)
}

fn parse_start(args: &[OsString]) -> Result<StartSpec> {
    let mut index = 0;
    let mut timeout = None;
    let mut id = None;
    let caller_cwd = std::env::current_dir().context("read caller working directory")?;
    let mut cwd = caller_cwd.clone();
    while index < args.len() {
        match args[index].to_str() {
            Some("--timeout") => {
                let value = args
                    .get(index + 1)
                    .ok_or_else(|| anyhow!("--timeout requires SECONDS"))?;
                timeout = Some(parse_positive_seconds(value)?);
                index += 2;
            }
            Some("--id") => {
                let value = args
                    .get(index + 1)
                    .ok_or_else(|| anyhow!("--id requires ID"))?;
                let value = os_string(value)?;
                if value.is_empty()
                    || value.len() > 96
                    || !value
                        .chars()
                        .all(|c| c.is_ascii_alphanumeric() || c == '-' || c == '_')
                {
                    bail!("--id must be 1-96 characters of [A-Za-z0-9_-]");
                }
                id = Some(value);
                index += 2;
            }
            Some("--cwd") => {
                let value = args
                    .get(index + 1)
                    .ok_or_else(|| anyhow!("--cwd requires DIR"))?;
                let value = PathBuf::from(value);
                cwd = if value.is_absolute() {
                    value
                } else {
                    caller_cwd.join(value)
                };
                index += 2;
            }
            Some("--") => {
                index += 1;
                break;
            }
            Some(flag) if flag.starts_with('-') => bail!("unknown job start option: {flag}"),
            _ => bail!(
                "usage: tsuna-supervisor job start --timeout SECONDS [--id ID] [--cwd DIR] -- COMMAND [ARGS...]"
            ),
        }
    }
    let timeout_seconds = timeout.ok_or_else(|| anyhow!("job start requires --timeout SECONDS"))?;
    if index == args.len() {
        bail!("job start requires a command after --");
    }
    let command = args[index..]
        .iter()
        .map(os_string)
        .collect::<Result<Vec<_>>>()?;
    Ok(StartSpec {
        id,
        timeout_seconds,
        cwd,
        command,
    })
}

fn os_string(value: &OsString) -> Result<String> {
    value
        .clone()
        .into_string()
        .map_err(|_| anyhow!("job arguments must be valid UTF-8"))
}

fn parse_positive_seconds(value: &OsString) -> Result<u64> {
    let value = value
        .to_str()
        .ok_or_else(|| anyhow!("timeout must be valid UTF-8"))?;
    let seconds = value
        .parse::<u64>()
        .with_context(|| format!("invalid timeout: {value}"))?;
    if seconds == 0 {
        bail!("timeout must be greater than zero");
    }
    if Instant::now()
        .checked_add(Duration::from_secs(seconds))
        .is_none()
    {
        bail!("timeout is too large for this system");
    }
    Ok(seconds)
}

fn absolute_root(root: &Path) -> Result<PathBuf> {
    if !root.is_absolute() {
        bail!("the job root must be an absolute path");
    }
    root.canonicalize()
        .with_context(|| format!("resolve job root {}", root.display()))
}

fn jobs_directory(root: &Path) -> PathBuf {
    root.join("jobs")
}

fn supplied_run_directory(root: &Path, raw: &OsString) -> Result<PathBuf> {
    let path = PathBuf::from(raw);
    if !path.is_absolute() {
        bail!(
            "run directory must be a direct child of {}",
            jobs_directory(root).display()
        );
    }
    // Resolve both sides so records created before the checkout moved through
    // a compatibility symlink retain their original path identity.
    let jobs = jobs_directory(root)
        .canonicalize()
        .with_context(|| format!("resolve job directory {}", jobs_directory(root).display()))?;
    let path = path
        .canonicalize()
        .with_context(|| format!("resolve run directory {}", path.display()))?;
    if path.parent() != Some(jobs.as_path()) {
        bail!("run directory must be a direct child of {}", jobs.display());
    }
    Ok(path)
}

fn create_run_directory(root: &Path, spec: &StartSpec) -> Result<(PathBuf, bool)> {
    let jobs = jobs_directory(root);
    fs::create_dir_all(&jobs).with_context(|| format!("create {}", jobs.display()))?;
    fs::set_permissions(&jobs, fs::Permissions::from_mode(0o700))
        .with_context(|| format!("protect {}", jobs.display()))?;
    for _ in 0..16 {
        let id = match &spec.id {
            Some(id) => id.clone(),
            None => format!("{:x}-{:032x}", now_ms(), rand::random::<u128>()),
        };
        let run_directory = jobs.join(&id);
        match fs::create_dir(&run_directory) {
            Ok(()) => {
                fs::set_permissions(&run_directory, fs::Permissions::from_mode(0o700))
                    .with_context(|| format!("protect {}", run_directory.display()))?;
                let config = JobConfig {
                    command: spec.command.clone(),
                    cwd: spec.cwd.to_string_lossy().into_owned(),
                    timeout_seconds: spec.timeout_seconds,
                };
                write_json_atomic(&run_directory.join(CONFIG_FILE), &config)?;
                let status = StatusRecord {
                    version: 1,
                    id,
                    run_directory: run_directory.to_string_lossy().into_owned(),
                    state: JobState::Starting,
                    supervisor_pid: None,
                    supervisor_start: None,
                    started_at_ms: now_ms(),
                    finished_at_ms: None,
                    exit_code: None,
                    signal: None,
                    detail: None,
                };
                write_status(&run_directory, &status)?;
                return Ok((run_directory, true));
            }
            Err(error) if error.kind() == io::ErrorKind::AlreadyExists && spec.id.is_some() => {
                // `mkdir` is the atomic claim for a caller-chosen identity.
                // Wait briefly for the creator to publish its first record.
                for _ in 0..100 {
                    if run_directory.join(STATUS_FILE).exists() {
                        return Ok((run_directory, false));
                    }
                    thread::sleep(Duration::from_millis(10));
                }
                bail!("run {} exists without a status record", id);
            }
            Err(error) if error.kind() == io::ErrorKind::AlreadyExists => continue,
            Err(error) => return Err(error).context("create unique job run directory"),
        }
    }
    bail!("could not allocate a unique job run directory")
}

fn run_worker(run_directory: &Path) -> Result<i32> {
    let identity = process_identity(std::process::id())?
        .ok_or_else(|| anyhow!("worker process identity disappeared before startup"))?;
    claim_worker(run_directory, std::process::id(), identity)?;
    let config: JobConfig = read_json(&run_directory.join(CONFIG_FILE))?;

    if !Path::new(&config.cwd).is_dir() {
        return finish_and_exit(
            run_directory,
            JobState::StartupFailed,
            None,
            None,
            Some(format!("working directory does not exist: {}", config.cwd)),
        );
    }
    let (program, arguments) = config
        .command
        .split_first()
        .ok_or_else(|| anyhow!("job configuration has no command"))?;
    let mut command = Command::new(program);
    command
        .args(arguments)
        .current_dir(&config.cwd)
        .stdin(Stdio::null());
    // A separate group lets the supervisor terminate every descendant without
    // signalling itself, even though it is already in its own session.
    command.process_group(0);
    let mut child = match command.spawn() {
        Ok(child) => child,
        Err(error) => {
            return finish_and_exit(
                run_directory,
                JobState::StartupFailed,
                None,
                None,
                Some(format!("could not start command {program}: {error}")),
            );
        }
    };
    let deadline = match deadline_after(config.timeout_seconds) {
        Ok(deadline) => deadline,
        Err(error) => {
            return finish_and_exit(
                run_directory,
                JobState::StartupFailed,
                None,
                None,
                Some(error.to_string()),
            );
        }
    };
    loop {
        if let Some(exit) = child.try_wait().context("poll job command")? {
            return finish_exit_status(run_directory, JobState::Completed, exit, None);
        }
        let cancelled = run_directory.join(CANCEL_FILE).exists();
        if cancelled || Instant::now() >= deadline {
            terminate_group(child.id())?;
            let grace_deadline = Instant::now()
                .checked_add(TERM_GRACE)
                .ok_or_else(|| anyhow!("job timeout grace period overflow"))?;
            while Instant::now() < grace_deadline {
                thread::sleep(
                    grace_deadline
                        .saturating_duration_since(Instant::now())
                        .min(POLL_INTERVAL),
                );
            }
            match signal_group(child.id(), libc::SIGKILL) {
                Ok(()) => {}
                Err(error) if error.raw_os_error() == Some(libc::ESRCH) => {}
                Err(error) => return Err(error).context("send SIGKILL to job process group"),
            }
            let exit = child.wait().context("wait after SIGKILL")?;
            let state = if cancelled {
                JobState::Cancelled
            } else {
                JobState::TimedOut
            };
            return finish_exit_status(run_directory, state, exit, None);
        }
        thread::sleep(POLL_INTERVAL);
    }
}

fn terminate_group(pid: u32) -> Result<()> {
    match signal_group(pid, libc::SIGTERM) {
        Ok(()) => Ok(()),
        Err(error) if error.raw_os_error() == Some(libc::ESRCH) => Ok(()),
        Err(error) => Err(error).context("send SIGTERM to job process group"),
    }
}

fn signal_group(pid: u32, signal: i32) -> io::Result<()> {
    // Negative pid is POSIX's process-group target. It originated from the
    // child we spawned and made group leader with `process_group(0)`.
    let result = unsafe { libc::kill(-(pid as libc::pid_t), signal) };
    if result == -1 {
        Err(io::Error::last_os_error())
    } else {
        Ok(())
    }
}

fn finish_exit_status(
    run_directory: &Path,
    state: JobState,
    exit: ExitStatus,
    detail: Option<String>,
) -> Result<i32> {
    let code = exit.code();
    let signal = exit.signal();
    finish(run_directory, state, code, signal, detail)?;
    Ok(match code {
        Some(code) => code,
        None => 128 + signal.unwrap_or(0),
    })
}

fn finish_and_exit(
    run_directory: &Path,
    state: JobState,
    exit_code: Option<i32>,
    signal: Option<i32>,
    detail: Option<String>,
) -> Result<i32> {
    finish(run_directory, state.clone(), exit_code, signal, detail)?;
    Ok(status_exit(&read_status(run_directory)?))
}

fn mark_running(run_directory: &Path, pid: u32, identity: String) -> Result<()> {
    with_status_lock(run_directory, || {
        let mut record = read_status(run_directory)?;
        if record.state == JobState::Starting {
            record.state = JobState::Running;
            record.supervisor_pid = Some(pid);
            record.supervisor_start = Some(identity);
            write_status_unlocked(run_directory, &record)?;
        }
        Ok(())
    })
}

fn claim_worker(run_directory: &Path, pid: u32, identity: String) -> Result<()> {
    with_status_lock(run_directory, || {
        let mut record = read_status(run_directory)?;
        match record.state {
            JobState::Starting => {
                record.state = JobState::Running;
                record.supervisor_pid = Some(pid);
                record.supervisor_start = Some(identity);
                write_status_unlocked(run_directory, &record)
            }
            JobState::Running
                if record.supervisor_pid == Some(pid)
                    && record.supervisor_start.as_deref() == Some(identity.as_str()) =>
            {
                Ok(())
            }
            JobState::Running => bail!("job already has a different live supervisor"),
            JobState::Completed
            | JobState::TimedOut
            | JobState::StartupFailed
            | JobState::Cancelled
            | JobState::Unknown => {
                bail!("job has already reached terminal state {:?}", record.state)
            }
        }
    })
}

fn finish(
    run_directory: &Path,
    state: JobState,
    exit_code: Option<i32>,
    signal: Option<i32>,
    detail: Option<String>,
) -> Result<()> {
    with_status_lock(run_directory, || {
        let mut record = read_status(run_directory)?;
        if is_terminal(&record.state) {
            return Ok(());
        }
        record.state = state;
        record.finished_at_ms = Some(now_ms());
        record.exit_code = exit_code;
        record.signal = signal;
        record.detail = detail;
        write_status_unlocked(run_directory, &record)
    })
}

fn observed_status(run_directory: &Path) -> Result<StatusRecord> {
    let mut record = read_status(run_directory)?;
    if matches!(record.state, JobState::Starting | JobState::Running)
        && !supervisor_is_live(&record)?
    {
        record.state = JobState::Unknown;
        record.detail =
            Some("supervisor is no longer running; no terminal record was published".into());
    }
    Ok(record)
}

fn supervisor_is_live(record: &StatusRecord) -> Result<bool> {
    let (Some(pid), Some(expected_identity)) =
        (record.supervisor_pid, record.supervisor_start.as_deref())
    else {
        return Ok(false);
    };
    Ok(process_identity(pid)?.as_deref() == Some(expected_identity))
}

fn status_exit(record: &StatusRecord) -> i32 {
    match record.state {
        JobState::Completed => record
            .exit_code
            .unwrap_or_else(|| 128 + record.signal.unwrap_or(0)),
        JobState::TimedOut => WAIT_TIMEOUT_EXIT,
        JobState::Cancelled => 130,
        JobState::StartupFailed | JobState::Unknown => UNKNOWN_EXIT,
        JobState::Starting | JobState::Running => 0,
    }
}

fn is_terminal(state: &JobState) -> bool {
    matches!(
        state,
        JobState::Completed
            | JobState::TimedOut
            | JobState::StartupFailed
            | JobState::Cancelled
            | JobState::Unknown
    )
}

fn read_status(run_directory: &Path) -> Result<StatusRecord> {
    read_json(&run_directory.join(STATUS_FILE))
}

fn print_status(record: &StatusRecord) -> Result<()> {
    println!("{}", serde_json::to_string(record)?);
    Ok(())
}

fn read_json<T: for<'de> Deserialize<'de>>(path: &Path) -> Result<T> {
    let bytes = fs::read(path).with_context(|| format!("read {}", path.display()))?;
    serde_json::from_slice(&bytes).with_context(|| format!("parse {}", path.display()))
}

fn write_status(run_directory: &Path, record: &StatusRecord) -> Result<()> {
    with_status_lock(run_directory, || {
        write_status_unlocked(run_directory, record)
    })
}

fn write_status_unlocked(run_directory: &Path, record: &StatusRecord) -> Result<()> {
    write_json_atomic(&run_directory.join(STATUS_FILE), record)
}

fn write_json_atomic<T: Serialize>(path: &Path, value: &T) -> Result<()> {
    let bytes = serde_json::to_vec(value)?;
    let nonce = rand::random::<u64>();
    let temporary = path.with_extension(format!("json.{}.{}.tmp", std::process::id(), nonce));
    let mut file = OpenOptions::new()
        .write(true)
        .create_new(true)
        .open(&temporary)
        .with_context(|| format!("create {}", temporary.display()))?;
    file.write_all(&bytes)
        .with_context(|| format!("write {}", temporary.display()))?;
    file.sync_all()
        .with_context(|| format!("sync {}", temporary.display()))?;
    drop(file);
    fs::rename(&temporary, path).with_context(|| format!("publish {}", path.display()))?;
    Ok(())
}

fn with_status_lock<T>(run_directory: &Path, operation: impl FnOnce() -> Result<T>) -> Result<T> {
    let lock_path = run_directory.join(STATUS_LOCK_FILE);
    let lock = OpenOptions::new()
        .create(true)
        .read(true)
        .write(true)
        .truncate(false)
        .open(&lock_path)
        .with_context(|| format!("open {}", lock_path.display()))?;
    // `flock` is released by the kernel if a supervisor is killed, so a
    // recovery reader cannot be stranded behind a stale lock file.
    if unsafe { libc::flock(lock.as_raw_fd(), libc::LOCK_EX) } == -1 {
        return Err(io::Error::last_os_error()).context("lock job status");
    }
    let result = operation();
    let unlock_result = if unsafe { libc::flock(lock.as_raw_fd(), libc::LOCK_UN) } == -1 {
        Err(io::Error::last_os_error())
    } else {
        Ok(())
    };
    drop(lock);
    let value = result?;
    unlock_result.context("unlock job status")?;
    Ok(value)
}

fn wait_for_identity(pid: u32) -> Result<Option<String>> {
    for _ in 0..10 {
        if let Some(identity) = process_identity(pid)? {
            return Ok(Some(identity));
        }
        thread::sleep(Duration::from_millis(10));
    }
    Ok(None)
}

fn deadline_after(seconds: u64) -> Result<Instant> {
    Instant::now()
        .checked_add(Duration::from_secs(seconds))
        .ok_or_else(|| anyhow!("timeout is too large for this system"))
}

#[cfg(target_os = "macos")]
fn process_identity(pid: u32) -> Result<Option<String>> {
    // `proc_pidinfo` returns the kernel-recorded start time, so a reused PID
    // cannot be mistaken for the detached supervisor that created this record.
    let mut info = unsafe { std::mem::zeroed::<libc::proc_bsdinfo>() };
    let expected_size = std::mem::size_of::<libc::proc_bsdinfo>() as libc::c_int;
    let result = unsafe {
        libc::proc_pidinfo(
            pid as libc::c_int,
            libc::PROC_PIDTBSDINFO,
            0,
            &mut info as *mut libc::proc_bsdinfo as *mut libc::c_void,
            expected_size,
        )
    };
    if result == expected_size && info.pbi_pid == pid {
        if info.pbi_status == libc::SZOMB {
            return Ok(None);
        }
        return Ok(Some(format!(
            "mac:{}:{}",
            info.pbi_start_tvsec, info.pbi_start_tvusec
        )));
    }
    if result == 0 || io::Error::last_os_error().raw_os_error() == Some(libc::ESRCH) {
        Ok(None)
    } else {
        Err(io::Error::last_os_error()).context("read supervisor process identity")
    }
}

#[cfg(target_os = "linux")]
fn process_identity(pid: u32) -> Result<Option<String>> {
    let path = PathBuf::from(format!("/proc/{pid}/stat"));
    let stat = match fs::read_to_string(&path) {
        Ok(stat) => stat,
        Err(error) if error.kind() == io::ErrorKind::NotFound => return Ok(None),
        Err(error) => return Err(error).context("read supervisor process identity"),
    };
    let (_, rest) = stat
        .rsplit_once(") ")
        .ok_or_else(|| anyhow!("malformed {}", path.display()))?;
    let mut fields = rest.split_whitespace();
    if fields.next() == Some("Z") {
        return Ok(None);
    }
    let start_ticks = fields
        .nth(18)
        .ok_or_else(|| anyhow!("missing process start time in {}", path.display()))?;
    Ok(Some(format!("linux:{start_ticks}")))
}

#[cfg(not(any(target_os = "macos", target_os = "linux")))]
fn process_identity(_pid: u32) -> Result<Option<String>> {
    bail!("job supervision requires macOS or Linux process identity support")
}

fn now_ms() -> u64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default()
        .as_millis()
        .try_into()
        .unwrap_or(u64::MAX)
}

#[cfg(test)]
mod tests {
    use super::*;
    use tempfile::TempDir;

    fn test_spec(command: &[&str], timeout_seconds: u64) -> StartSpec {
        StartSpec {
            id: None,
            timeout_seconds,
            cwd: PathBuf::from("/"),
            command: command.iter().map(|part| (*part).to_owned()).collect(),
        }
    }

    fn setup(command: &[&str], timeout_seconds: u64) -> (TempDir, PathBuf) {
        let root = tempfile::tempdir().unwrap();
        let (run_directory, _) =
            create_run_directory(root.path(), &test_spec(command, timeout_seconds)).unwrap();
        (root, run_directory)
    }

    #[test]
    fn captures_zero_and_nonzero_exit_codes() {
        let (_root, success) = setup(&["/bin/sh", "-c", "exit 0"], 5);
        assert_eq!(run_worker(&success).unwrap(), 0);
        assert_eq!(read_status(&success).unwrap().state, JobState::Completed);

        let (_root, failure) = setup(&["/bin/sh", "-c", "exit 7"], 5);
        assert_eq!(run_worker(&failure).unwrap(), 7);
        let record = read_status(&failure).unwrap();
        assert_eq!(record.state, JobState::Completed);
        assert_eq!(record.exit_code, Some(7));
    }

    #[test]
    fn deadline_terminates_the_child_process_group() {
        let root = tempfile::tempdir().unwrap();
        let pid_file = root.path().join("descendant.pid");
        let path = pid_file.to_string_lossy().into_owned();
        let spec = test_spec(
            &[
                "/bin/sh",
                "-c",
                "(trap '' TERM; sleep 30) & child=$!; printf '%s' \"$child\" > \"$1\"; trap 'exit 0' TERM; wait",
                "sh",
                &path,
            ],
            1,
        );
        let (run_directory, _) = create_run_directory(root.path(), &spec).unwrap();
        assert_eq!(run_worker(&run_directory).unwrap(), 0);
        assert_eq!(
            read_status(&run_directory).unwrap().state,
            JobState::TimedOut
        );
        let descendant = fs::read_to_string(pid_file)
            .unwrap()
            .parse::<u32>()
            .unwrap();
        let deadline = Instant::now() + Duration::from_secs(2);
        while process_identity(descendant).unwrap().is_some() && Instant::now() < deadline {
            thread::sleep(POLL_INTERVAL);
        }
        assert!(
            process_identity(descendant).unwrap().is_none(),
            "descendant survived timeout"
        );
    }

    #[test]
    fn wait_has_a_deadline_without_killing_the_job() {
        let (_root, run_directory) = setup(&["/bin/true"], 5);
        let identity = process_identity(std::process::id()).unwrap().unwrap();
        mark_running(&run_directory, std::process::id(), identity).unwrap();
        let start = Instant::now();
        let result = wait_for_test(&run_directory, 1).unwrap();
        assert_eq!(result, WAIT_TIMEOUT_EXIT);
        assert!(start.elapsed() < Duration::from_secs(2));
        assert_eq!(
            read_status(&run_directory).unwrap().state,
            JobState::Running
        );
    }

    #[test]
    fn missing_or_reused_supervisor_is_unknown() {
        let (_root, run_directory) = setup(&["/bin/true"], 5);
        mark_running(&run_directory, u32::MAX, "never-a-live-process".into()).unwrap();
        let record = observed_status(&run_directory).unwrap();
        assert_eq!(record.state, JobState::Unknown);
        assert_eq!(status_exit(&record), UNKNOWN_EXIT);
        assert_eq!(
            read_status(&run_directory).unwrap().state,
            JobState::Running
        );
    }

    #[test]
    fn completed_record_is_not_overwritten() {
        let (_root, run_directory) = setup(&["/bin/true"], 5);
        mark_running(
            &run_directory,
            std::process::id(),
            process_identity(std::process::id()).unwrap().unwrap(),
        )
        .unwrap();
        finish(&run_directory, JobState::Completed, Some(7), None, None).unwrap();
        finish(
            &run_directory,
            JobState::Completed,
            Some(0),
            None,
            Some("late writer".into()),
        )
        .unwrap();
        let record = read_status(&run_directory).unwrap();
        assert_eq!(record.exit_code, Some(7));
        assert_eq!(record.detail, None);
        assert!(run_worker(&run_directory).is_err());
        assert_eq!(read_status(&run_directory).unwrap().exit_code, Some(7));
    }

    #[test]
    fn duplicate_worker_cannot_claim_an_existing_supervisor() {
        let (_root, run_directory) = setup(&["/bin/true"], 5);
        mark_running(&run_directory, u32::MAX, "other-worker".into()).unwrap();
        assert!(run_worker(&run_directory).is_err());
        assert_eq!(
            read_status(&run_directory).unwrap().state,
            JobState::Running
        );
    }

    #[test]
    fn startup_failure_is_published() {
        let (_root, run_directory) = setup(&["/definitely/not/a/command"], 5);
        assert_eq!(run_worker(&run_directory).unwrap(), UNKNOWN_EXIT);
        let record = read_status(&run_directory).unwrap();
        assert_eq!(record.state, JobState::StartupFailed);
        assert!(record.detail.unwrap().contains("could not start command"));
    }

    #[test]
    fn accepts_old_checkout_paths_but_rejects_job_directory_symlink_escapes() {
        let root = tempfile::tempdir().unwrap();
        let (run_directory, _) =
            create_run_directory(root.path(), &test_spec(&["/bin/true"], 5)).unwrap();
        let old_checkout = root.path().join("old-checkout");
        std::os::unix::fs::symlink(root.path(), &old_checkout).unwrap();
        let old_path = old_checkout
            .join("jobs")
            .join(run_directory.file_name().unwrap());
        assert_eq!(
            supplied_run_directory(root.path(), &old_path.into_os_string()).unwrap(),
            run_directory.canonicalize().unwrap()
        );

        let outside = tempfile::tempdir().unwrap();
        let escaped = jobs_directory(root.path()).join("escaped");
        std::os::unix::fs::symlink(outside.path(), &escaped).unwrap();
        assert!(supplied_run_directory(root.path(), &escaped.into_os_string()).is_err());
    }

    #[test]
    fn caller_identity_never_starts_a_second_run() {
        let root = tempfile::tempdir().unwrap();
        let mut spec = test_spec(&["/bin/true"], 5);
        spec.id = Some("result-1".into());
        let (first, created) = create_run_directory(root.path(), &spec).unwrap();
        assert!(created);
        let (second, created_again) = create_run_directory(root.path(), &spec).unwrap();
        assert!(!created_again);
        assert_eq!(first, second);
    }

    #[test]
    fn cancel_request_terminates_group_and_publishes_cancelled() {
        let (_root, run_directory) = setup(&["/bin/sh", "-c", "sleep 30"], 30);
        write_json_atomic(&run_directory.join(CANCEL_FILE), &now_ms()).unwrap();
        let start = Instant::now();
        run_worker(&run_directory).unwrap();
        assert!(start.elapsed() < Duration::from_secs(5));
        assert_eq!(
            read_status(&run_directory).unwrap().state,
            JobState::Cancelled
        );
    }

    fn wait_for_test(run_directory: &Path, timeout_seconds: u64) -> Result<i32> {
        let deadline = Instant::now() + Duration::from_secs(timeout_seconds);
        loop {
            let record = observed_status(run_directory)?;
            if is_terminal(&record.state) || record.state == JobState::Unknown {
                return Ok(status_exit(&record));
            }
            let remaining = deadline.saturating_duration_since(Instant::now());
            if remaining.is_zero() {
                return Ok(WAIT_TIMEOUT_EXIT);
            }
            thread::sleep(remaining.min(POLL_INTERVAL));
        }
    }
}
