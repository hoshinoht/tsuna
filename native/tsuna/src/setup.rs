use anyhow::{Context, Result, bail};
use serde_json::{Map, Value, json};
use std::collections::BTreeMap;
use std::ffi::OsString;
use std::fs;
use std::os::unix::fs::PermissionsExt;
use std::path::{Path, PathBuf};
use std::process::Command;

pub(crate) const RUNTIME_AGENT: &str = ".runtime/omp/agent";

#[derive(Debug, serde::Deserialize)]
struct Catalog {
    #[serde(rename = "setupVersion")]
    setup_version: u64,
    models: BTreeMap<String, CatalogModel>,
}

#[derive(Debug, serde::Deserialize)]
struct CatalogModel {
    name: String,
    input: Value,
    cost: Value,
    #[serde(rename = "contextWindow")]
    context_window: u64,
    #[serde(rename = "maxTokens")]
    max_tokens: u64,
}

/// OMP custom-model thinking efforts in the order models.yml requires.
const OMP_EFFORTS: [&str; 6] = ["minimal", "low", "medium", "high", "xhigh", "max"];

#[derive(Debug, serde::Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
struct Kanata {
    enabled: bool,
    base_url: String,
    key_file: PathBuf,
    providers: Vec<KanataProvider>,
    models: Vec<KanataModel>,
    roles: BTreeMap<String, String>,
}

#[derive(Debug, serde::Deserialize)]
#[serde(deny_unknown_fields)]
struct KanataProvider {
    id: String,
    api: KanataApi,
}

#[derive(Debug, Clone, Copy, serde::Deserialize, serde::Serialize)]
enum KanataApi {
    #[serde(rename = "openai-responses")]
    OpenAiResponses,
    #[serde(rename = "openai-completions")]
    OpenAiCompletions,
    #[serde(rename = "anthropic-messages")]
    AnthropicMessages,
}

#[derive(Debug, serde::Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
struct KanataModel {
    id: String,
    source: Option<String>,
    name: Option<String>,
    context_window: Option<u64>,
    max_tokens: Option<u64>,
    efforts: Vec<String>,
}

pub fn run(root: &Path, args: &[OsString]) -> Result<i32> {
    if !args.is_empty() {
        bail!("Usage: tsuna setup");
    }
    prepare(root)?;
    Ok(0)
}

fn prepare(root: &Path) -> Result<()> {
    build_missing_binaries(root)?;
    let runtime = root.join(RUNTIME_AGENT);
    fs::create_dir_all(&runtime)?;
    link_agent_inputs(root, &runtime)?;
    copy_dir_overwrite(&root.join("agent/themes"), &runtime.join("themes"))?;

    let roles = read_json(root.join("config/roles.json"))?;
    let kanata = read_kanata(root)?;
    let sources = source_models(&roles)?;
    let catalog = read_catalog(
        root,
        sources
            .iter()
            .map(String::as_str)
            .chain(kanata.catalog_sources()),
    )?;
    write_runtime_config(root, &runtime, &roles, &kanata, catalog.setup_version)?;
    write_mcp(root, &runtime)?;
    write_models(root, &runtime, &sources, &kanata, &catalog.models)?;
    fs::set_permissions(root.join("bin/tsuna"), fs::Permissions::from_mode(0o755))?;
    let count = roles
        .as_object()
        .context("config/roles.json must be an object")?
        .len();
    println!("Prepared {count} agents, MCP definitions and pinned OMP profile.");
    Ok(())
}

fn build_missing_binaries(root: &Path) -> Result<()> {
    for (directory, output, entry) in [
        ("vendor/shiori", "shiori", "./cmd/shiori"),
        ("mcps/gofetch-mcp", "bin/gofetch", "./cmd/gofetch"),
        (
            "mcps/researcher-mcp",
            "bin/researcher-mcp",
            "./cmd/google-scholar-mcp",
        ),
    ] {
        if root.join(directory).join(output).symlink_metadata().is_ok() {
            continue;
        }
        println!("Building {directory}…");
        let status = Command::new("go")
            .args(["build", "-trimpath", "-o", output, entry])
            .current_dir(root.join(directory))
            .env("CGO_ENABLED", "0")
            .status()
            .with_context(|| format!("Cannot build {directory}"))?;
        if !status.success() {
            bail!("Failed to build {directory}");
        }
    }
    Ok(())
}

fn link_agent_inputs(root: &Path, runtime: &Path) -> Result<()> {
    for entry in ["agents", "skills", "prompts", "AGENTS.md"] {
        let target = runtime.join(entry);
        if target.symlink_metadata().is_err() {
            std::os::unix::fs::symlink(root.join("agent").join(entry), target)?;
        }
    }
    Ok(())
}

pub(crate) fn copy_dir_overwrite(source: &Path, target: &Path) -> Result<()> {
    fs::create_dir_all(target)?;
    for entry in fs::read_dir(source)? {
        let entry = entry?;
        let target_path = target.join(entry.file_name());
        if entry.file_type()?.is_dir() {
            copy_dir_overwrite(&entry.path(), &target_path)?;
        } else {
            fs::copy(entry.path(), target_path)?;
        }
    }
    Ok(())
}

fn read_catalog<'a>(root: &Path, sources: impl IntoIterator<Item = &'a str>) -> Result<Catalog> {
    let script = root.join("scripts/omp-catalog.ts");
    let output = Command::new(crate::launch::bun())
        .arg(script)
        .args(sources)
        .output()
        .context("Cannot read OMP catalog through Bun")?;
    if !output.status.success() {
        bail!(
            "OMP catalog bridge failed: {}",
            String::from_utf8_lossy(&output.stderr).trim()
        );
    }
    serde_json::from_slice(&output.stdout).context("OMP catalog bridge returned invalid JSON")
}

fn read_kanata(root: &Path) -> Result<Kanata> {
    let path = root.join("config/kanata.json");
    let kanata: Kanata = serde_json::from_slice(&fs::read(&path)?)
        .with_context(|| format!("Cannot parse {}", path.display()))?;
    kanata.validate()?;
    Ok(kanata)
}

impl KanataApi {
    fn is_openai(self) -> bool {
        matches!(self, Self::OpenAiResponses | Self::OpenAiCompletions)
    }
}

impl Kanata {
    fn validate(&self) -> Result<()> {
        for (index, provider) in self.providers.iter().enumerate() {
            if self.providers[..index].iter().any(|p| p.id == provider.id) {
                bail!("config/kanata.json lists provider {} twice", provider.id);
            }
        }
        for (index, model) in self.models.iter().enumerate() {
            if self.models[..index].iter().any(|m| m.id == model.id) {
                bail!("config/kanata.json lists model {} twice", model.id);
            }
            let mut previous = None;
            for effort in &model.efforts {
                let position = OMP_EFFORTS
                    .iter()
                    .position(|known| known == effort)
                    .with_context(|| {
                        format!(
                            "config/kanata.json model {} has effort {effort}; OMP efforts are {}",
                            model.id,
                            OMP_EFFORTS.join(", ")
                        )
                    })?;
                if previous.is_some_and(|previous| previous >= position) {
                    bail!(
                        "config/kanata.json model {} must list efforts once, lowest first",
                        model.id
                    );
                }
                previous = Some(position);
            }
            if previous.is_none() {
                bail!("config/kanata.json model {} has no efforts", model.id);
            }
        }
        Ok(())
    }

    /// Catalog sources are only requested when Kanata providers will be generated.
    fn catalog_sources(&self) -> impl Iterator<Item = &str> {
        self.enabled
            .then_some(&self.models)
            .into_iter()
            .flatten()
            .filter_map(|model| model.source.as_deref())
    }

    /// Rewrites a `cliproxy-<p>/<id>:<level>` role selector to the mapped Kanata provider.
    fn role_model(&self, role: &str, model: &str, provider: &str) -> Result<String> {
        if !self.enabled {
            bail!("config/kanata.json maps role {role} to {provider}, but Kanata is disabled");
        }
        if !self.providers.iter().any(|p| p.id == provider) {
            bail!("config/kanata.json maps role {role} to unknown provider {provider}");
        }
        let selector = model
            .split_once('/')
            .filter(|(proxy, _)| proxy.starts_with("cliproxy-"))
            .map(|(_, selector)| selector)
            .with_context(|| format!("Role {role} model {model} is not a CLIProxyAPI selector"))?;
        let (id, level) = match selector.rsplit_once(':') {
            Some((id, level)) => (id, Some(level)),
            None => (selector, None),
        };
        let entry = self
            .models
            .iter()
            .find(|m| m.id == id)
            .with_context(|| format!("config/kanata.json has no model {id} for role {role}"))?;
        if let Some(level) = level
            && !entry.efforts.iter().any(|effort| effort == level)
        {
            bail!("config/kanata.json model {id} does not list effort {level} used by role {role}");
        }
        Ok(format!("{provider}/{selector}"))
    }
}

fn write_runtime_config(
    root: &Path,
    runtime: &Path,
    roles: &Value,
    kanata: &Kanata,
    setup_version: u64,
) -> Result<()> {
    let text = fs::read_to_string(root.join("config/omp.yml"))?;
    let mut config: Value = serde_saphyr::from_str(&text).context("Cannot parse config/omp.yml")?;
    let config = config
        .as_object_mut()
        .context("config/omp.yml must be a mapping")?;
    config.insert("setupVersion".into(), json!(setup_version));
    config.insert(
        "extensions".into(),
        Value::Array(
            [
                "permissions",
                "docs",
                "runtime",
                "tsuna",
                "usage",
                "composer",
                "project-context",
            ]
            .into_iter()
            .map(|name| {
                Value::String(
                    root.join("extensions")
                        .join(format!("{name}.ts"))
                        .display()
                        .to_string(),
                )
            })
            .collect(),
        ),
    );
    config.insert("modelRoles".into(), model_roles(roles, kanata)?);
    fs::write(
        runtime.join("config.yml"),
        serde_saphyr::to_string(&config)?,
    )?;
    Ok(())
}

fn write_mcp(root: &Path, runtime: &Path) -> Result<()> {
    let contents = fs::read_to_string(root.join("config/mcp.json"))?;
    fs::write(
        runtime.join("mcp.json"),
        contents.replace("${TSUNA_ROOT}", &root.display().to_string()),
    )?;
    Ok(())
}

/// The single source of runtime role models: agents use `@role`, and the launcher and
/// `/tsuna-agent` read these generated values, so Kanata role switches apply only here.
fn model_roles(roles: &Value, kanata: &Kanata) -> Result<Value> {
    let mut result = Map::new();
    let roles = roles
        .as_object()
        .context("config/roles.json must be an object")?;
    if let Some(role) = kanata.roles.keys().find(|role| !roles.contains_key(*role)) {
        bail!("config/kanata.json maps unknown role {role}");
    }
    for (name, role) in roles {
        let model = role
            .get("model")
            .and_then(Value::as_str)
            .with_context(|| format!("Role {name} has no model"))?;
        let model = match kanata.roles.get(name) {
            Some(provider) => kanata.role_model(name, model, provider)?,
            None => model.to_owned(),
        };
        result.insert(name.clone(), Value::String(model));
    }
    for (name, alias) in [
        ("default", "orchestrator"),
        ("plan", "plan"),
        ("smol", "explore"),
        ("slow", "oracle"),
    ] {
        result.insert(
            name.into(),
            result
                .get(alias)
                .cloned()
                .with_context(|| format!("Missing {alias} role"))?,
        );
    }
    result.insert(
        "tiny".into(),
        Value::String("cliproxy-openai/gpt-6-luna:low".into()),
    );
    Ok(Value::Object(result))
}

fn source_models(roles: &Value) -> Result<Vec<String>> {
    let mut sources = Vec::new();
    for role in roles
        .as_object()
        .context("config/roles.json must be an object")?
        .values()
    {
        let source = role
            .get("sourceModel")
            .and_then(Value::as_str)
            .context("Role has no sourceModel")?
            .split('#')
            .next()
            .unwrap_or_default()
            .to_owned();
        if !sources.contains(&source) {
            sources.push(source);
        }
    }
    Ok(sources)
}

fn write_models(
    root: &Path,
    runtime: &Path,
    sources: &[String],
    kanata: &Kanata,
    catalog: &BTreeMap<String, CatalogModel>,
) -> Result<()> {
    let mut providers = Map::new();
    for source in sources {
        let (provider, raw_id) = source
            .split_once('/')
            .with_context(|| format!("Invalid source model: {source}"))?;
        if provider != "openai" && provider != "anthropic" {
            continue;
        }
        let model = catalog
            .get(source)
            .with_context(|| format!("OMP catalog bridge omitted {source}"))?;
        let id = raw_id.strip_suffix("-1m").unwrap_or(raw_id);
        let proxy = format!("cliproxy-{provider}");
        let group = providers.entry(proxy).or_insert_with(|| json!({
            "baseUrl": if provider == "openai" { "http://127.0.0.1:18317/v1" } else { "http://127.0.0.1:18317" },
            "api": if provider == "openai" { "openai-responses" } else { "anthropic-messages" },
            "apiKey": gateway_key_setting(root),
            "models": [],
        }));
        let models = group
            .get_mut("models")
            .and_then(Value::as_array_mut)
            .expect("new provider has models array");
        if models
            .iter()
            .any(|existing| existing.get("id").and_then(Value::as_str) == Some(id))
        {
            continue;
        }
        models.push(json!({
            "id": id,
            "name": format!("{} (CLIProxyAPI)", model.name),
            "reasoning": true,
            "input": model.input,
            "cost": model.cost,
            "contextWindow": if raw_id.ends_with("-1m") { 1_000_000 } else { model.context_window },
            "maxTokens": model.max_tokens,
        }));
    }
    for (id, provider) in kanata_providers(root, kanata, catalog)? {
        if providers.contains_key(&id) {
            bail!("config/kanata.json provider {id} collides with a generated provider");
        }
        providers.insert(id, provider);
    }
    fs::write(
        runtime.join("models.yml"),
        serde_saphyr::to_string(&json!({ "providers": providers }))?,
    )?;
    Ok(())
}

fn kanata_providers(
    root: &Path,
    kanata: &Kanata,
    catalog: &BTreeMap<String, CatalogModel>,
) -> Result<Map<String, Value>> {
    let mut providers = Map::new();
    if !kanata.enabled {
        return Ok(providers);
    }
    let mut models = Vec::with_capacity(kanata.models.len());
    for model in &kanata.models {
        let source = model
            .source
            .as_deref()
            .map(|source| {
                catalog
                    .get(source)
                    .with_context(|| format!("OMP catalog bridge omitted {source}"))
            })
            .transpose()?;
        let missing = |field: &str| {
            format!(
                "config/kanata.json model {} needs a source or an explicit {field}",
                model.id
            )
        };
        let name = model
            .name
            .as_deref()
            .or(source.map(|source| source.name.as_str()))
            .with_context(|| missing("name"))?;
        let context_window = model
            .context_window
            .or(source.map(|source| source.context_window))
            .with_context(|| missing("contextWindow"))?;
        let max_tokens = model
            .max_tokens
            .or(source.map(|source| source.max_tokens))
            .with_context(|| missing("maxTokens"))?;
        models.push((model, name, context_window, max_tokens));
    }
    let base_url = kanata.base_url.trim_end_matches('/');
    let api_key = key_command(&root.join(&kanata.key_file));
    for provider in &kanata.providers {
        let mode = if provider.api.is_openai() {
            "effort"
        } else {
            "anthropic-adaptive"
        };
        let entries: Vec<Value> = models
            .iter()
            .map(|(model, name, context_window, max_tokens)| {
                json!({
                    "id": model.id,
                    "name": format!("{name} (Kanata)"),
                    "reasoning": true,
                    // Kanata routes accept text only, whatever the upstream model supports.
                    "input": ["text"],
                    "cost": { "input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0 },
                    "contextWindow": context_window,
                    "maxTokens": max_tokens,
                    "thinking": { "mode": mode, "efforts": model.efforts },
                })
            })
            .collect();
        providers.insert(
            provider.id.clone(),
            json!({
                "baseUrl": if provider.api.is_openai() { format!("{base_url}/v1") } else { base_url.to_owned() },
                "api": provider.api,
                "apiKey": api_key,
                "models": entries,
            }),
        );
    }
    Ok(providers)
}

fn gateway_key_setting(root: &Path) -> String {
    key_command(&root.join(".runtime/proxy/client-key"))
}

/// OMP resolves `!command` keys at request time, so generated files never hold the secret.
fn key_command(key_file: &Path) -> String {
    let key_file = key_file.display().to_string();
    format!("!cat '{}'", key_file.replace('\'', "'\\''"))
}

fn read_json(path: PathBuf) -> Result<Value> {
    serde_json::from_slice(&fs::read(&path)?)
        .with_context(|| format!("Cannot parse {}", path.display()))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn gateway_key_command_quotes_path_without_embedding_the_secret() {
        let root = Path::new("/tmp/it's-a-profile");
        let setting = gateway_key_setting(root);
        assert_eq!(
            setting,
            "!cat '/tmp/it'\\''s-a-profile/.runtime/proxy/client-key'"
        );
        assert!(!setting.contains("secret"));
    }

    #[test]
    fn model_roles_include_catalog_roles_and_aliases() {
        let roles = json!({
            "orchestrator": { "model": "provider/main" }, "plan": { "model": "provider/plan" },
            "explore": { "model": "provider/fast" }, "oracle": { "model": "provider/slow" }
        });
        let roles = model_roles(&roles, &kanata(json!({}))).unwrap();
        assert_eq!(roles["default"], "provider/main");
        assert_eq!(roles["tiny"], "cliproxy-openai/gpt-6-luna:low");
    }

    #[test]
    fn copies_a_fixture_profile_without_reading_a_real_profile() {
        let temp = tempfile::tempdir().unwrap();
        let root = temp.path().join("repo");
        fs::create_dir_all(root.join("agent/themes")).unwrap();
        fs::write(root.join("agent/themes/theme.json"), "fixture").unwrap();
        let target = root.join(RUNTIME_AGENT).join("themes");
        copy_dir_overwrite(&root.join("agent/themes"), &target).unwrap();
        assert_eq!(
            fs::read_to_string(target.join("theme.json")).unwrap(),
            "fixture"
        );
    }

    fn kanata(roles: Value) -> Kanata {
        let kanata: Kanata = serde_json::from_value(json!({
            "enabled": true,
            "baseUrl": "http://127.0.0.1:18090/",
            "keyFile": ".runtime/kanata/client-key",
            "providers": [
                { "id": "kanata", "api": "openai-responses" },
                { "id": "kanata-chat", "api": "openai-completions" },
                { "id": "kanata-anthropic", "api": "anthropic-messages" }
            ],
            "models": [
                { "id": "claude-opus-5-5", "source": "anthropic/claude-opus-5-5", "efforts": ["low", "medium", "high"] },
                { "id": "gpt-6-luna", "source": "openai/gpt-6-luna", "name": "Luna override", "maxTokens": 4096, "efforts": ["low", "xhigh"] },
                { "id": "gpt-oss-20b", "name": "gpt-oss 20B local", "contextWindow": 131072, "maxTokens": 32768, "efforts": ["medium"] }
            ],
            "roles": roles
        }))
        .unwrap();
        kanata.validate().unwrap();
        kanata
    }

    fn catalog() -> BTreeMap<String, CatalogModel> {
        [
            ("anthropic/claude-opus-5-5", "Claude Opus 5.5", 1_000_000),
            ("openai/gpt-6-luna", "GPT-6 Luna", 272_000),
        ]
        .into_iter()
        .map(|(source, name, context_window)| {
            (
                source.to_owned(),
                CatalogModel {
                    name: name.into(),
                    input: json!(["text", "image"]),
                    cost: json!({ "input": 1, "output": 2, "cacheRead": 0, "cacheWrite": 0 }),
                    context_window,
                    max_tokens: 128_000,
                },
            )
        })
        .collect()
    }

    #[test]
    fn kanata_providers_follow_each_api_without_embedding_the_key() {
        let temp = tempfile::tempdir().unwrap();
        let root = temp.path().join("it's-a-profile");
        fs::create_dir_all(&root).unwrap();
        let sources = ["anthropic/claude-opus-5-5".to_owned()];
        write_models(&root, &root, &sources, &kanata(json!({})), &catalog()).unwrap();
        let text = fs::read_to_string(root.join("models.yml")).unwrap();
        let models: Value = serde_saphyr::from_str(&text).unwrap();
        let providers = &models["providers"];
        assert!(providers["cliproxy-anthropic"].is_object());
        let key = format!(
            "!cat '{}'",
            root.join(".runtime/kanata/client-key")
                .display()
                .to_string()
                .replace('\'', "'\\''")
        );
        for (id, api, base_url, mode) in [
            (
                "kanata",
                "openai-responses",
                "http://127.0.0.1:18090/v1",
                "effort",
            ),
            (
                "kanata-chat",
                "openai-completions",
                "http://127.0.0.1:18090/v1",
                "effort",
            ),
            (
                "kanata-anthropic",
                "anthropic-messages",
                "http://127.0.0.1:18090",
                "anthropic-adaptive",
            ),
        ] {
            let provider = &providers[id];
            assert_eq!(provider["api"], api);
            assert_eq!(provider["baseUrl"], base_url);
            assert_eq!(provider["apiKey"], key.as_str());
            let models = provider["models"].as_array().unwrap();
            assert_eq!(models.len(), 3);
            assert_eq!(
                models[0],
                json!({
                    "id": "claude-opus-5-5", "name": "Claude Opus 5.5 (Kanata)", "reasoning": true,
                    "input": ["text"], "cost": { "input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0 },
                    "contextWindow": 1_000_000, "maxTokens": 128_000,
                    "thinking": { "mode": mode, "efforts": ["low", "medium", "high"] }
                })
            );
        }
    }

    #[test]
    fn kanata_explicit_metadata_overrides_the_catalog_and_is_required_without_a_source() {
        let root = Path::new("/tmp/profile");
        let providers = kanata_providers(root, &kanata(json!({})), &catalog()).unwrap();
        let models = providers["kanata"]["models"].as_array().unwrap();
        assert_eq!(models[1]["name"], "Luna override (Kanata)");
        assert_eq!(models[1]["contextWindow"], 272_000);
        assert_eq!(models[1]["maxTokens"], 4096);
        assert_eq!(models[2]["name"], "gpt-oss 20B local (Kanata)");
        assert_eq!(models[2]["contextWindow"], 131_072);

        let mut incomplete = kanata(json!({}));
        incomplete.models[2].context_window = None;
        let error = kanata_providers(root, &incomplete, &catalog()).unwrap_err();
        assert!(
            error
                .to_string()
                .contains("gpt-oss-20b needs a source or an explicit contextWindow")
        );
    }

    #[test]
    fn disabled_kanata_adds_no_providers_or_catalog_sources() {
        let mut disabled = kanata(json!({}));
        disabled.enabled = false;
        assert_eq!(disabled.catalog_sources().count(), 0);
        assert!(
            kanata_providers(Path::new("/tmp/profile"), &disabled, &catalog())
                .unwrap()
                .is_empty()
        );
    }

    #[test]
    fn kanata_rejects_efforts_omp_cannot_express() {
        for efforts in [json!(["none", "low"]), json!(["high", "low"]), json!([])] {
            let kanata: Kanata = serde_json::from_value(json!({
                "enabled": true, "baseUrl": "http://kanata", "keyFile": "key", "providers": [],
                "models": [{ "id": "m", "source": "openai/m", "efforts": efforts }], "roles": {}
            }))
            .unwrap();
            assert!(kanata.validate().is_err());
        }
    }

    #[test]
    fn kanata_role_switch_rewrites_only_mapped_roles() {
        let roles = json!({
            "orchestrator": { "model": "cliproxy-anthropic/claude-opus-5-5:medium" },
            "plan": { "model": "cliproxy-anthropic/claude-opus-5-5:high" },
            "explore": { "model": "cliproxy-openai/gpt-6-luna:low" },
            "oracle": { "model": "cliproxy-anthropic/claude-opus-5-5:medium" }
        });
        let result = model_roles(
            &roles,
            &kanata(json!({ "explore": "kanata", "plan": "kanata-anthropic" })),
        )
        .unwrap();
        assert_eq!(result["explore"], "kanata/gpt-6-luna:low");
        assert_eq!(result["smol"], "kanata/gpt-6-luna:low");
        assert_eq!(result["plan"], "kanata-anthropic/claude-opus-5-5:high");
        assert_eq!(
            result["default"],
            "cliproxy-anthropic/claude-opus-5-5:medium"
        );

        for (mapping, message) in [
            (
                json!({ "explore": "kanata-missing" }),
                "unknown provider kanata-missing",
            ),
            (
                json!({ "orchestrator": "kanata" }),
                "no model claude-opus-5-5",
            ),
            (json!({ "explore": "kanata" }), "does not list effort low"),
            (json!({ "tester": "kanata" }), "unknown role tester"),
        ] {
            let mut config = kanata(mapping);
            // Drop the Opus entry and Luna's low effort so the mapped roles fall outside Kanata.
            config.models.remove(0);
            config.models[0].efforts = vec!["xhigh".into()];
            let error = model_roles(&roles, &config).unwrap_err().to_string();
            assert!(error.contains(message), "{error}");
        }
    }
}
