use std::{
    ffi::{OsStr, OsString},
    fs::{self, File, OpenOptions},
    io::{Read, Write},
    path::{Path, PathBuf},
    process::{Command, Stdio},
    thread,
    time::{Duration, Instant},
};

use anyhow::{Context, Result, bail};
use base64::{Engine as _, engine::general_purpose::URL_SAFE_NO_PAD};
use rand::Rng as _;
use serde_json::Value;

const GATEWAY_PORT: u16 = 18_317;
const REQUEST_TIMEOUT: Duration = Duration::from_secs(5);
const GATEWAY_TIMEOUT: Duration = Duration::from_secs(30);

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ProxyPaths {
    pub directory: PathBuf,
    pub auth_directory: PathBuf,
    pub client_key: PathBuf,
    pub config: PathBuf,
}

pub fn proxy_paths(repository_root: &Path) -> ProxyPaths {
    let directory = repository_root.join(".runtime").join("proxy");
    ProxyPaths {
        auth_directory: directory.join("auth"),
        client_key: directory.join("client-key"),
        config: directory.join("config.yaml"),
        directory,
    }
}

pub fn localhost_port_mapping(port: i32) -> Result<String> {
    if !(1..=65_535).contains(&port) {
        bail!("Invalid localhost port: {port}");
    }
    Ok(format!("127.0.0.1:{port}:{port}"))
}

/// The Compose service that runs CLIProxyAPI, from `config/proxy.json`.
#[derive(Debug, Clone, PartialEq, Eq, serde::Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
pub struct ComposeTarget {
    pub project_directory: PathBuf,
    pub compose_files: Vec<PathBuf>,
    pub service: String,
}

fn expand_home(path: &Path, home: Option<&Path>) -> Result<Option<PathBuf>> {
    let Ok(rest) = path.strip_prefix("~") else {
        return Ok(None);
    };
    let home = home.context("HOME is required to expand ~ in config/proxy.json")?;
    Ok(Some(home.join(rest)))
}

impl ComposeTarget {
    /// Makes every path absolute: `~/` is the home directory; otherwise the project
    /// directory is relative to the checkout and Compose files to the project directory.
    /// Compose resolves `-f` against the working directory, so relative files would break.
    fn resolve(self, repository_root: &Path, home: Option<&Path>) -> Result<Self> {
        if self.compose_files.is_empty() {
            bail!("config/proxy.json composeFiles must list at least one file");
        }
        if self.service.is_empty() || self.service.starts_with('-') {
            bail!(
                "config/proxy.json service must be a Compose service name, not {:?}",
                self.service
            );
        }
        // `components()` drops `.` segments, so the default "." renders as the checkout itself.
        let project_directory: PathBuf = match expand_home(&self.project_directory, home)? {
            Some(path) => path,
            None => repository_root.join(&self.project_directory),
        }
        .components()
        .collect();
        let compose_files = self
            .compose_files
            .iter()
            .map(|file| {
                Ok(expand_home(file, home)?
                    .unwrap_or_else(|| project_directory.join(file))
                    .components()
                    .collect())
            })
            .collect::<Result<_>>()?;
        Ok(Self {
            project_directory,
            compose_files,
            service: self.service,
        })
    }
}

pub fn read_compose_target(repository_root: &Path) -> Result<ComposeTarget> {
    let path = repository_root.join("config/proxy.json");
    let target: ComposeTarget = serde_json::from_slice(
        &fs::read(&path).with_context(|| format!("read {}", path.display()))?,
    )
    .with_context(|| format!("Cannot parse {}", path.display()))?;
    target.resolve(repository_root, crate::launch::home().ok().as_deref())
}

pub fn compose_args(target: &ComposeTarget, args: &[&str]) -> Vec<OsString> {
    let mut result = vec![
        OsString::from("compose"),
        OsString::from("--project-directory"),
        target.project_directory.clone().into_os_string(),
    ];
    for file in &target.compose_files {
        result.push(OsString::from("-f"));
        result.push(file.clone().into_os_string());
    }
    result.extend(args.iter().map(OsString::from));
    result
}

pub fn login_args(target: &ComposeTarget, provider: &str) -> Result<Vec<OsString>> {
    let callback_port = match provider {
        "claude" => 54_545,
        "codex" => 1_455,
        _ => bail!("{}", usage()),
    };
    let mapping = localhost_port_mapping(callback_port)?;
    let provider_flag = format!("--{provider}-login");
    // `run` publishes only this callback (not the service's own ports), and --no-deps
    // keeps the rest of the target project untouched.
    let mut args = compose_args(target, &["run", "--rm", "--no-deps", "-p"]);
    args.extend([
        OsString::from(mapping),
        OsString::from(&target.service),
        OsString::from("./CLIProxyAPI"),
        OsString::from(provider_flag),
        OsString::from("--no-browser"),
    ]);
    Ok(args)
}

pub fn config_yaml(client_key: &str) -> String {
    format!(
        concat!(
            "config-version: 8\n",
            "server:\n",
            "  host: \"0.0.0.0\"\n",
            "  port: 8317\n",
            "management:\n",
            "  allow-remote: false\n",
            "  secret-key: \"\"\n",
            "  disable-control-panel: true\n",
            "access:\n",
            "  api-keys:\n",
            "    - \"{}\"\n",
            "routing:\n",
            "  strategy: \"round-robin\"\n",
            "  session-affinity: true\n",
            "  session-affinity-ttl: \"1h\"\n",
            "oauth:\n",
            "  auth-dir: \"/root/.cli-proxy-api\"\n",
            "observability:\n",
            "  logs:\n",
            "    debug: false\n",
            "    logging-to-file: false\n",
            "    request-log: false\n",
            "  usage:\n",
            "    usage-statistics-enabled: false\n"
        ),
        client_key
    )
}

#[cfg(unix)]
fn private_open_options() -> OpenOptions {
    use std::os::unix::fs::OpenOptionsExt;

    let mut options = OpenOptions::new();
    options.custom_flags(libc::O_NOFOLLOW);
    options
}

#[cfg(not(unix))]
fn private_open_options() -> OpenOptions {
    OpenOptions::new()
}

#[cfg(unix)]
fn set_mode(file: &File, mode: u32) -> Result<()> {
    use std::os::unix::fs::PermissionsExt;

    file.set_permissions(fs::Permissions::from_mode(mode))
        .context("set private permissions")
}

#[cfg(not(unix))]
fn set_mode(_: &File, _: u32) -> Result<()> {
    Ok(())
}

fn ensure_directory(path: &Path, private: bool) -> Result<()> {
    match fs::create_dir(path) {
        Ok(()) => {}
        Err(error) if error.kind() == std::io::ErrorKind::AlreadyExists => {}
        Err(error) => {
            return Err(error).with_context(|| format!("create directory {}", path.display()));
        }
    }

    let metadata = fs::symlink_metadata(path)
        .with_context(|| format!("inspect directory {}", path.display()))?;
    if metadata.file_type().is_symlink() || !metadata.is_dir() {
        bail!("Expected a directory at {}", path.display());
    }

    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        let mut options = private_open_options();
        options
            .read(true)
            .custom_flags(libc::O_NOFOLLOW | libc::O_DIRECTORY);
        let directory = options
            .open(path)
            .with_context(|| format!("open private directory {}", path.display()))?;
        if private {
            set_mode(&directory, 0o700)?;
        }
    }
    #[cfg(not(unix))]
    {
        let directory = private_open_options()
            .read(true)
            .open(path)
            .with_context(|| format!("open private directory {}", path.display()))?;
        if private {
            set_mode(&directory, 0o700)?;
        }
    }
    Ok(())
}

fn create_private_file(path: &Path, content: &[u8]) -> Result<()> {
    #[cfg(unix)]
    use std::os::unix::fs::OpenOptionsExt;

    let mut options = private_open_options();
    options.write(true).create_new(true);
    #[cfg(unix)]
    options.mode(0o600);
    match options.open(path) {
        Ok(mut file) => {
            set_mode(&file, 0o600)?;
            file.write_all(content)
                .with_context(|| format!("write private file {}", path.display()))?;
            file.sync_all()
                .with_context(|| format!("flush private file {}", path.display()))?;
            Ok(())
        }
        Err(error) if error.kind() == std::io::ErrorKind::AlreadyExists => Ok(()),
        Err(error) => Err(error).with_context(|| format!("create private file {}", path.display())),
    }
}

fn open_private_regular_file(path: &Path) -> Result<File> {
    let metadata =
        fs::symlink_metadata(path).with_context(|| format!("inspect file {}", path.display()))?;
    if metadata.file_type().is_symlink() || !metadata.is_file() {
        bail!("Expected a file at {}", path.display());
    }
    let file = private_open_options()
        .read(true)
        .open(path)
        .with_context(|| format!("open private file {}", path.display()))?;
    if !file.metadata()?.is_file() {
        bail!("Expected a file at {}", path.display());
    }
    set_mode(&file, 0o600)?;
    Ok(file)
}

fn private_regular_file(path: &Path) -> Result<()> {
    drop(open_private_regular_file(path)?);
    Ok(())
}

fn read_client_key(paths: &ProxyPaths) -> Result<String> {
    let mut file = open_private_regular_file(&paths.client_key)?;
    let mut key = String::new();
    file.read_to_string(&mut key)
        .with_context(|| format!("read client key {}", paths.client_key.display()))?;
    let key = key.trim().to_owned();
    if key.is_empty() {
        bail!(
            "Client key at {} is empty; remove it and run 'proxy init'.",
            paths.client_key.display()
        );
    }
    Ok(key)
}

pub fn initialize_proxy(repository_root: &Path) -> Result<ProxyPaths> {
    let runtime = repository_root.join(".runtime");
    ensure_directory(&runtime, false)?;
    let paths = proxy_paths(repository_root);
    ensure_directory(&paths.directory, true)?;
    ensure_directory(&paths.auth_directory, true)?;

    let mut bytes = [0_u8; 32];
    rand::rng().fill(&mut bytes);
    let client_key = format!("{}\n", URL_SAFE_NO_PAD.encode(bytes));
    create_private_file(&paths.client_key, client_key.as_bytes())?;
    private_regular_file(&paths.client_key)?;
    let client_key = read_client_key(&paths)?;
    create_private_file(&paths.config, config_yaml(&client_key).as_bytes())?;
    private_regular_file(&paths.config)?;
    Ok(paths)
}

fn fetch_models_at(paths: &ProxyPaths, endpoint: &str, timeout: Duration) -> Result<Value> {
    let client_key = read_client_key(paths)?;
    let client = reqwest::blocking::Client::builder()
        .no_proxy()
        .timeout(timeout)
        .build()
        .context("build gateway HTTP client")?;
    let response = client
        .get(format!("{endpoint}/v1/models"))
        .bearer_auth(client_key)
        .send()
        .context("request gateway model listing")?;
    if !response.status().is_success() {
        bail!("Gateway model listing failed: HTTP {}", response.status());
    }
    response.json().context("decode gateway model listing")
}

pub fn fetch_models(repository_root: &Path) -> Result<Value> {
    fetch_models_at(
        &proxy_paths(repository_root),
        &format!("http://127.0.0.1:{GATEWAY_PORT}"),
        REQUEST_TIMEOUT,
    )
}

pub fn wait_for_gateway(repository_root: &Path, timeout: Duration) -> Result<Value> {
    let deadline = Instant::now() + timeout;
    let mut last_error = None;
    while Instant::now() < deadline {
        let remaining = deadline.saturating_duration_since(Instant::now());
        match fetch_models_at(
            &proxy_paths(repository_root),
            &format!("http://127.0.0.1:{GATEWAY_PORT}"),
            REQUEST_TIMEOUT.min(remaining.max(Duration::from_millis(1))),
        ) {
            Ok(models) => return Ok(models),
            Err(error) => last_error = Some(error),
        }
        thread::sleep(
            Duration::from_millis(250).min(deadline.saturating_duration_since(Instant::now())),
        );
    }
    let detail = last_error
        .map(|error| format!(": {error}"))
        .unwrap_or_default();
    bail!(
        "CLIProxyAPI gateway did not become healthy within {}ms{detail}",
        timeout.as_millis()
    )
}

fn run_docker(docker: &OsStr, args: &[OsString]) -> Result<()> {
    let status = Command::new(docker)
        .args(args)
        .stdin(Stdio::inherit())
        .stdout(Stdio::inherit())
        .stderr(Stdio::inherit())
        .status()
        .with_context(|| format!("run {}", Path::new(docker).display()))?;
    if status.success() {
        return Ok(());
    }
    match status.code() {
        Some(code) => bail!("docker exited with status {code}"),
        None => bail!("docker terminated by signal"),
    }
}

fn usage() -> &'static str {
    "Usage: tsuna proxy <init|start|stop|status|login claude|codex|models>"
}

/// Executes a proxy subcommand and returns its process exit code on success.
pub fn run(root: &Path, args: &[OsString]) -> Result<i32> {
    let command = args.first().and_then(|value| value.to_str());
    let provider = args.get(1).and_then(|value| value.to_str());
    match command {
        Some("init") => {
            initialize_proxy(root)?;
            println!("CLIProxyAPI gateway initialized.");
        }
        Some("start") => {
            initialize_proxy(root)?;
            let target = read_compose_target(root)?;
            run_docker(
                OsStr::new("docker"),
                &compose_args(&target, &["up", "-d", &target.service]),
            )?;
            wait_for_gateway(root, GATEWAY_TIMEOUT)?;
            println!("CLIProxyAPI gateway is reachable.");
        }
        Some("stop") => {
            let target = read_compose_target(root)?;
            run_docker(
                OsStr::new("docker"),
                &compose_args(&target, &["stop", &target.service]),
            )?;
        }
        Some("status") => {
            let models = fetch_models(root)?;
            let count = models
                .get("data")
                .and_then(Value::as_array)
                .map_or(0, Vec::len);
            println!("CLIProxyAPI gateway is reachable ({count} models).");
        }
        Some("models") => println!("{}", serde_json::to_string_pretty(&fetch_models(root)?)?),
        Some("login") => {
            let provider = provider
                .filter(|value| matches!(*value, "claude" | "codex"))
                .ok_or_else(|| anyhow::anyhow!(usage()))?;
            initialize_proxy(root)?;
            run_docker(
                OsStr::new("docker"),
                &login_args(&read_compose_target(root)?, provider)?,
            )?;
        }
        _ => bail!("{}", usage()),
    }
    Ok(0)
}

#[cfg(test)]
mod tests {
    use std::{
        fs,
        io::{Read, Write},
        net::TcpListener,
        os::unix::fs::{PermissionsExt, symlink},
        path::Path,
        thread,
        time::{Duration, Instant},
    };

    use tempfile::tempdir;

    use super::*;

    fn mode(path: &Path) -> u32 {
        fs::metadata(path).unwrap().permissions().mode() & 0o777
    }

    #[test]
    fn initializes_private_persistent_credentials_without_replacing_them() {
        let directory = tempdir().unwrap();
        let paths = initialize_proxy(directory.path()).unwrap();
        let original_key = fs::read_to_string(&paths.client_key).unwrap();
        let original_config = fs::read_to_string(&paths.config).unwrap();

        assert!(paths.auth_directory.is_dir());
        assert_eq!(mode(&paths.directory), 0o700);
        assert_eq!(mode(&paths.auth_directory), 0o700);
        assert_eq!(mode(&paths.client_key), 0o600);
        assert_eq!(mode(&paths.config), 0o600);
        assert!(original_key.trim().len() >= 40);
        assert!(original_config.contains(&format!("    - \"{}\"", original_key.trim())));
        assert!(original_config.contains("  host: \"0.0.0.0\""));
        assert!(original_config.contains("  session-affinity: true"));
        assert!(original_config.contains("  disable-control-panel: true"));
        assert!(original_config.contains("    request-log: false"));

        initialize_proxy(directory.path()).unwrap();
        assert_eq!(fs::read_to_string(&paths.client_key).unwrap(), original_key);
        assert_eq!(fs::read_to_string(&paths.config).unwrap(), original_config);
    }

    #[test]
    fn rejects_symlinked_credentials_before_reading_or_chmodding_them() {
        let directory = tempdir().unwrap();
        let paths = initialize_proxy(directory.path()).unwrap();
        let target = directory.path().join("outside-key");
        fs::write(&target, "do not read").unwrap();
        fs::remove_file(&paths.client_key).unwrap();
        symlink(&target, &paths.client_key).unwrap();

        let error = read_client_key(&paths).unwrap_err().to_string();
        assert!(error.contains("Expected a file"));
        assert_eq!(fs::read_to_string(target).unwrap(), "do not read");
    }

    #[test]
    fn constructs_local_only_docker_arguments_and_validates_ports() {
        assert_eq!(
            localhost_port_mapping(18_317).unwrap(),
            "127.0.0.1:18317:18317"
        );
        assert_eq!(
            localhost_port_mapping(54_545).unwrap(),
            "127.0.0.1:54545:54545"
        );
        assert!(
            localhost_port_mapping(0)
                .unwrap_err()
                .to_string()
                .contains("Invalid localhost port")
        );
        let tsuna = tsuna_target();
        assert_eq!(
            compose_args(&tsuna, &["up", "-d", "gateway"]),
            [
                "compose",
                "--project-directory",
                "/repo",
                "-f",
                "/repo/compose.yml",
                "up",
                "-d",
                "gateway"
            ]
            .map(OsString::from)
        );
        assert_eq!(
            login_args(&tsuna, "claude").unwrap(),
            [
                "compose",
                "--project-directory",
                "/repo",
                "-f",
                "/repo/compose.yml",
                "run",
                "--rm",
                "--no-deps",
                "-p",
                "127.0.0.1:54545:54545",
                "gateway",
                "./CLIProxyAPI",
                "--claude-login",
                "--no-browser"
            ]
            .map(OsString::from)
        );
        assert!(
            login_args(&tsuna, "codex")
                .unwrap()
                .contains(&OsString::from("127.0.0.1:1455:1455"))
        );
        assert!(config_yaml("test-key").contains("  auth-dir: \"/root/.cli-proxy-api\""));
    }

    /// tsuna's own compose.yml and its `gateway` service.
    fn tsuna_target() -> ComposeTarget {
        serde_json::from_str::<ComposeTarget>(
            r#"{ "projectDirectory": ".", "composeFiles": ["compose.yml"], "service": "gateway" }"#,
        )
        .unwrap()
        .resolve(Path::new("/repo"), None)
        .unwrap()
    }

    const KANATA_SETTING: &str = r#"{
        "projectDirectory": "~/projects/homelab/kanata",
        "composeFiles": ["compose.kanata.yml", "compose.kanata.cliproxy.yml"],
        "service": "cliproxy"
    }"#;

    #[test]
    fn targets_a_kanata_managed_service_from_the_setting() {
        let directory = tempdir().unwrap();
        fs::create_dir(directory.path().join("config")).unwrap();
        let target = serde_json::from_str::<ComposeTarget>(KANATA_SETTING)
            .unwrap()
            .resolve(directory.path(), Some(Path::new("/home/me")))
            .unwrap();
        let args = |tail: &[&str]| {
            [
                "compose",
                "--project-directory",
                "/home/me/projects/homelab/kanata",
                "-f",
                "/home/me/projects/homelab/kanata/compose.kanata.yml",
                "-f",
                "/home/me/projects/homelab/kanata/compose.kanata.cliproxy.yml",
            ]
            .iter()
            .chain(tail)
            .map(OsString::from)
            .collect::<Vec<_>>()
        };
        assert_eq!(
            compose_args(&target, &["stop", &target.service]),
            args(&["stop", "cliproxy"])
        );
        assert_eq!(
            login_args(&target, "codex").unwrap(),
            args(&[
                "run",
                "--rm",
                "--no-deps",
                "-p",
                "127.0.0.1:1455:1455",
                "cliproxy",
                "./CLIProxyAPI",
                "--codex-login",
                "--no-browser"
            ])
        );

        // Absolute paths pass through unchanged.
        let absolute = r#"{"projectDirectory": "/srv/kanata", "composeFiles": ["/srv/kanata/a.yml"], "service": "cliproxy"}"#;
        fs::write(directory.path().join("config/proxy.json"), absolute).unwrap();
        let target = read_compose_target(directory.path()).unwrap();
        assert_eq!(target.project_directory, Path::new("/srv/kanata"));
        assert_eq!(target.compose_files, [Path::new("/srv/kanata/a.yml")]);
    }

    #[test]
    fn rejects_an_unusable_compose_setting() {
        let parse = |json: &str| {
            serde_json::from_str::<ComposeTarget>(json)
                .map_err(anyhow::Error::from)
                .and_then(|target| target.resolve(Path::new("/repo"), None))
                .unwrap_err()
                .to_string()
        };
        assert!(
            parse(r#"{"projectDirectory": ".", "composeFiles": [], "service": "gateway"}"#)
                .contains("at least one file")
        );
        assert!(
            parse(r#"{"projectDirectory": ".", "composeFiles": ["c.yml"], "service": "--rm"}"#)
                .contains("Compose service name")
        );
        assert!(
            parse(r#"{"projectDirectory": "~/k", "composeFiles": ["c.yml"], "service": "s"}"#)
                .contains("HOME is required")
        );
        assert!(
            parse(r#"{"projectDirectory": ".", "composeFiles": ["c.yml"], "service": "s", "file": "x"}"#)
                .contains("unknown field")
        );
        assert!(
            read_compose_target(tempdir().unwrap().path())
                .unwrap_err()
                .to_string()
                .contains("config/proxy.json")
        );
        assert!(config_yaml("test-key").contains("  auth-dir: \"/root/.cli-proxy-api\""));
    }

    #[test]
    fn runs_fake_docker_with_inherited_process_arguments() {
        let directory = tempdir().unwrap();
        let docker = directory.path().join("docker");
        let output = PathBuf::from(format!("{}.args", docker.display()));
        fs::write(&docker, "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$0.args\"\n").unwrap();
        fs::set_permissions(&docker, fs::Permissions::from_mode(0o700)).unwrap();
        let args = compose_args(&tsuna_target(), &["stop", "gateway"]);
        run_docker(docker.as_os_str(), &args).unwrap();
        assert_eq!(
            fs::read_to_string(output).unwrap(),
            "compose\n--project-directory\n/repo\n-f\n/repo/compose.yml\nstop\ngateway\n"
        );
    }

    #[test]
    fn fetches_models_from_local_server_with_bearer_auth_and_a_deadline() {
        let directory = tempdir().unwrap();
        let paths = initialize_proxy(directory.path()).unwrap();
        fs::write(&paths.client_key, "test-key\n").unwrap();
        private_regular_file(&paths.client_key).unwrap();
        let listener = TcpListener::bind("127.0.0.1:0").expect("bind local HTTP test server");
        let endpoint = format!("http://{}", listener.local_addr().unwrap());
        let server = thread::spawn(move || {
            let (mut stream, _) = listener.accept().unwrap();
            let mut bytes = [0; 1_024];
            let read = stream.read(&mut bytes).unwrap();
            let request = String::from_utf8_lossy(&bytes[..read]);
            assert!(request.starts_with("GET /v1/models HTTP/1.1"));
            assert!(
                request.contains("authorization: Bearer test-key")
                    || request.contains("Authorization: Bearer test-key")
            );
            stream.write_all(b"HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 16\r\nConnection: close\r\n\r\n{\"data\":[{},{}]}").unwrap();
        });
        let models = fetch_models_at(&paths, &endpoint, Duration::from_secs(1)).unwrap();
        assert_eq!(models["data"].as_array().unwrap().len(), 2);
        server.join().unwrap();

        let listener = TcpListener::bind("127.0.0.1:0").unwrap();
        let endpoint = format!("http://{}", listener.local_addr().unwrap());
        let server = thread::spawn(move || {
            let (_stream, _) = listener.accept().unwrap();
            thread::sleep(Duration::from_millis(250));
        });
        let started = Instant::now();
        assert!(fetch_models_at(&paths, &endpoint, Duration::from_millis(50)).is_err());
        assert!(started.elapsed() < Duration::from_millis(200));
        server.join().unwrap();
    }
}
