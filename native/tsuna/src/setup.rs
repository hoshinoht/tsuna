use anyhow::{Context, Result, bail};
use serde_json::{Map, Value, json};
use std::collections::BTreeMap;
use std::ffi::OsString;
use std::fs;
use std::os::unix::fs::PermissionsExt;
use std::path::{Path, PathBuf};
use std::process::Command;

const RUNTIME_AGENT: &str = ".runtime/omp/agent";

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
    let sources = source_models(&roles)?;
    let catalog = read_catalog(root, &sources)?;
    write_runtime_config(root, &runtime, &roles, catalog.setup_version)?;
    write_mcp(root, &runtime)?;
    write_models(root, &runtime, &sources, &catalog.models)?;
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

fn copy_dir_overwrite(source: &Path, target: &Path) -> Result<()> {
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

fn read_catalog(root: &Path, sources: &[String]) -> Result<Catalog> {
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

fn write_runtime_config(
    root: &Path,
    runtime: &Path,
    roles: &Value,
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
    config.insert("modelRoles".into(), model_roles(roles)?);
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

fn model_roles(roles: &Value) -> Result<Value> {
    let mut result = Map::new();
    let roles = roles
        .as_object()
        .context("config/roles.json must be an object")?;
    for (name, role) in roles {
        result.insert(
            name.clone(),
            Value::String(
                role.get("model")
                    .and_then(Value::as_str)
                    .with_context(|| format!("Role {name} has no model"))?
                    .to_owned(),
            ),
        );
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
    fs::write(
        runtime.join("models.yml"),
        serde_saphyr::to_string(&json!({ "providers": providers }))?,
    )?;
    Ok(())
}

fn gateway_key_setting(root: &Path) -> String {
    let key_file = root.join(".runtime/proxy/client-key").display().to_string();
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
        let roles = model_roles(&roles).unwrap();
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
}
