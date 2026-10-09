use anyhow::{Context, Result, bail};
use jsonc_parser::parse_to_serde_value;
use serde_json::{Map, Value, json};
use std::ffi::OsString;
use std::fs;
use std::path::Path;

const OMP_INTEGRATION: &str = r#"

## OMP integration

- Use OMP's task tool with complete briefs; it replaces OpenCode subagent calls. Agent Hub and agent:// paths provide worker inspection and steering.
- MCP tools have OMP names (mcp__server_tool); inspect the available catalog for the exact spelling.
- The permission extension preserves the imported action/resource rules and Shiori role checks. A denied tool remains unavailable.
- Durable plans stay in .opencode/workplan so existing Shiori artifacts remain compatible.
- User-selected roles are managed by /tsuna-agent; only the user may change the primary role.
"#;

#[derive(Debug, PartialEq)]
pub struct Frontmatter {
    pub meta: Value,
    pub body: String,
}

pub fn run(_root: &Path, args: &[OsString]) -> Result<i32> {
    if args.len() != 2 {
        bail!("Usage: tsuna import-opencode SOURCE TARGET");
    }
    import_config(Path::new(&args[0]), Path::new(&args[1]))?;
    Ok(0)
}

pub fn import_config(source: &Path, target: &Path) -> Result<()> {
    let source_config: Value = parse_jsonc(&source.join("opencode.json"))?;
    let root = std::path::absolute(target)?;
    fs::create_dir_all(&root)?;
    let agent_names = markdown_names(&source.join("agents"))?;
    let mut permissions = Map::new();
    let mut roles = Map::new();

    for name in &agent_names {
        let input = fs::read_to_string(source.join("agents").join(format!("{name}.md")))?;
        let parsed = frontmatter(&input)?;
        let meta = object(&parsed.meta, "agent frontmatter")?;
        let permission_rules = meta.get("permissions").cloned().unwrap_or(Value::Null);
        permissions.insert(name.clone(), permission_rules.clone());
        let source_model = string_field(meta, "model", "agent frontmatter")?;
        roles.insert(
            name.clone(),
            json!({
                "description": string_field(meta, "description", "agent frontmatter")?,
                "mode": string_field(meta, "mode", "agent frontmatter")?,
                "model": model_selector(source_model, true),
                "directModel": model_selector(source_model, false),
                "sourceModel": source_model,
                "hidden": meta.get("hidden").and_then(Value::as_bool).unwrap_or(false),
            }),
        );
        let spawns: Vec<Value> = agent_names
            .iter()
            .filter(|child| allows_spawn(&permission_rules, child))
            .cloned()
            .map(Value::String)
            .collect();
        let generated = json!({
            "name": name,
            "description": string_field(meta, "description", "agent frontmatter")?,
            "model": format!("@{name}"),
            "spawns": if spawns.is_empty() { Value::Bool(false) } else { Value::Array(spawns) },
        });
        save(
            &root,
            &format!("agent/agents/{name}.md"),
            &format!(
                "---\n{}---\n\n{}",
                serde_saphyr::to_string(&generated)?,
                adapt_prose(&parsed.body)
            ),
        )?;
        save(
            &root,
            &format!("agent/prompts/{name}.md"),
            &adapt_prose(&parsed.body),
        )?;
    }

    save_json(&root, "config/roles.json", &Value::Object(roles))?;
    save_json(
        &root,
        "config/permissions.json",
        &Value::Object(permissions),
    )?;
    write_plugins(&root, &source_config)?;
    write_mcp(&root, &source_config)?;
    let commands = write_commands(source, &root)?;
    save_json(
        &root,
        "config/commands.json",
        &Value::Object(commands.clone()),
    )?;
    save(
        &root,
        "agent/AGENTS.md",
        &format!(
            "{}{}",
            adapt_prose(&fs::read_to_string(source.join("AGENTS.md"))?),
            OMP_INTEGRATION
        ),
    )?;
    copy_skills(source, &root)?;
    for path in [
        "LICENSE",
        "NOTICE",
        "scripts/check-workplan.ts",
        "scripts/agent-permissions.yaml",
        "dcp.jsonc",
        "cli.json",
    ] {
        let destination = if matches!(path, "LICENSE" | "NOTICE") {
            path.to_owned()
        } else {
            format!("source-config/{path}")
        };
        save(&root, &destination, &fs::read_to_string(source.join(path))?)?;
    }
    let skills = markdown_skill_count(&root.join("agent/skills"))?;
    let mcp = object(
        source_config
            .get("mcp")
            .context("opencode.json has no mcp")?,
        "mcp",
    )?
    .get("servers")
    .and_then(Value::as_object)
    .context("opencode.json mcp has no servers")?;
    let plugins = source_config
        .get("plugins")
        .cloned()
        .unwrap_or(Value::Array(vec![]));
    save_json(
        &root,
        "config/import-manifest.json",
        &json!({
            "importedAt": "2026-10-07", "sourceCommit": "f1586302b214c1ae84b81c7ca508cd1f20004428",
            "includesWorkingTree": true, "agents": agent_names.len(), "commands": commands.len(), "skills": skills,
            "mcp": mcp.keys().collect::<Vec<_>>(), "plugins": plugins,
            "permissionsSource": "Actual agent frontmatter (active configuration); original generator YAML retained under source-config."
        }),
    )?;
    Ok(())
}

pub fn frontmatter(text: &str) -> Result<Frontmatter> {
    let normalized = text.replace("\r\n", "\n");
    let Some(rest) = normalized.strip_prefix("---\n") else {
        bail!("Missing Markdown frontmatter");
    };
    let Some((raw_meta, body)) = rest.split_once("\n---\n") else {
        bail!("Missing Markdown frontmatter");
    };
    let meta = serde_saphyr::from_str(raw_meta).context("Cannot parse Markdown frontmatter")?;
    Ok(Frontmatter {
        meta,
        body: body.to_owned(),
    })
}

pub fn model_selector(value: &str, gateway: bool) -> String {
    let (base, effort) = value.split_once('#').unwrap_or((value, ""));
    let (provider, raw_id) = base.split_once('/').unwrap_or((base, ""));
    let id = raw_id.strip_suffix("-1m").unwrap_or(raw_id);
    let provider = if gateway {
        match provider {
            "openai" => "cliproxy-openai",
            "anthropic" => "cliproxy-anthropic",
            other => other,
        }
    } else if provider == "openai" {
        "openai-codex"
    } else {
        provider
    };
    format!(
        "{provider}/{id}{}",
        if effort.is_empty() {
            String::new()
        } else {
            format!(":{effort}")
        }
    )
}

pub fn translate_mcp(servers: &Map<String, Value>) -> Result<Value> {
    let mut output = Map::new();
    for (name, entry) in servers {
        let entry = object(entry, "MCP server")?;
        let command = entry
            .get("command")
            .and_then(Value::as_array)
            .cloned()
            .unwrap_or_default();
        let mut mapped = Map::new();
        mapped.insert(
            "type".into(),
            Value::String(
                if entry.get("type").and_then(Value::as_str) == Some("local") {
                    "stdio"
                } else {
                    "http"
                }
                .into(),
            ),
        );
        mapped.insert(
            "enabled".into(),
            Value::Bool(
                !entry
                    .get("disabled")
                    .and_then(Value::as_bool)
                    .unwrap_or(false),
            ),
        );
        if let Some(first) = command.first().and_then(Value::as_str) {
            mapped.insert("command".into(), Value::String(expand_env(first)));
            mapped.insert(
                "args".into(),
                Value::Array(
                    command
                        .iter()
                        .skip(1)
                        .map(|value| Value::String(expand_env(value.as_str().unwrap_or_default())))
                        .collect(),
                ),
            );
        } else if let Some(url) = entry.get("url").and_then(Value::as_str) {
            mapped.insert("url".into(), Value::String(url.to_owned()));
        }
        if let Some(headers) = entry.get("headers").and_then(Value::as_object) {
            mapped.insert("headers".into(), expand_map(headers));
        }
        if let Some(environment) = entry.get("environment").and_then(Value::as_object) {
            mapped.insert("env".into(), expand_map(environment));
        }
        if let Some(timeout) = entry.get("timeout") {
            let timeout = timeout
                .as_u64()
                .or_else(|| timeout.get("execution").and_then(Value::as_u64));
            if let Some(timeout) = timeout {
                mapped.insert("timeout".into(), json!(timeout));
            }
        }
        output.insert(name.clone(), Value::Object(mapped));
    }
    Ok(Value::Object(output))
}

fn write_plugins(root: &Path, config: &Value) -> Result<()> {
    let plugins = config
        .get("plugins")
        .and_then(Value::as_array)
        .context("opencode.json has no plugins")?;
    let mut options = Map::new();
    for plugin in plugins.iter().filter_map(Value::as_object) {
        let package = plugin
            .get("package")
            .and_then(Value::as_str)
            .context("Plugin has no package")?;
        let name = Path::new(package)
            .file_name()
            .and_then(|part| part.to_str())
            .context("Plugin package has no basename")?;
        options.insert(
            name.to_owned(),
            plugin.get("options").cloned().unwrap_or_else(|| json!({})),
        );
    }
    save_json(root, "config/plugin-options.json", &Value::Object(options))
}

fn write_mcp(root: &Path, config: &Value) -> Result<()> {
    let mcp = config
        .get("mcp")
        .and_then(Value::as_object)
        .context("opencode.json has no mcp")?;
    let servers = mcp
        .get("servers")
        .and_then(Value::as_object)
        .context("opencode.json mcp has no servers")?;
    save_json(
        root,
        "config/mcp.json",
        &json!({ "mcpServers": translate_mcp(servers)? }),
    )
}

fn write_commands(source: &Path, root: &Path) -> Result<Map<String, Value>> {
    let mut commands = Map::new();
    for name in markdown_names(&source.join("commands"))? {
        let parsed = frontmatter(&fs::read_to_string(
            source.join("commands").join(format!("{name}.md")),
        )?)?;
        let meta = object(&parsed.meta, "command frontmatter")?;
        let mut item = meta.clone();
        item.insert("body".into(), Value::String(adapt_prose(&parsed.body)));
        commands.insert(name.clone(), Value::Object(item));
        save(
            root,
            &format!("commands/{name}.md"),
            &format!(
                "---\n{}---\n{}",
                serde_saphyr::to_string(&parsed.meta)?,
                adapt_prose(&parsed.body)
            ),
        )?;
    }
    Ok(commands)
}

fn copy_skills(source: &Path, root: &Path) -> Result<()> {
    let destination = root.join("agent/skills");
    copy_filtered(&source.join("skills"), &destination)?;
    rewrite_markdown(&destination)
}

fn copy_filtered(source: &Path, destination: &Path) -> Result<()> {
    fs::create_dir_all(destination)?;
    for entry in fs::read_dir(source)? {
        let entry = entry?;
        let name = entry.file_name();
        let name = name.to_string_lossy();
        if name == "__pycache__" || name.ends_with(".pyc") {
            continue;
        }
        let target = destination.join(&*name);
        if entry.file_type()?.is_dir() {
            copy_filtered(&entry.path(), &target)?;
        } else {
            fs::copy(entry.path(), target)?;
        }
    }
    Ok(())
}

fn rewrite_markdown(directory: &Path) -> Result<()> {
    for entry in fs::read_dir(directory)? {
        let entry = entry?;
        if entry.file_type()?.is_dir() {
            rewrite_markdown(&entry.path())?;
        } else if entry.file_name().to_string_lossy().ends_with(".md") {
            let path = entry.path();
            let contents = fs::read_to_string(&path)?;
            fs::write(&path, adapt_prose(&contents))?;
        }
    }
    Ok(())
}

fn markdown_names(directory: &Path) -> Result<Vec<String>> {
    let mut names: Vec<_> = fs::read_dir(directory)?
        .filter_map(|entry| entry.ok())
        .filter_map(|entry| {
            let path = entry.path();
            (entry.file_type().ok()?.is_file()
                && path.extension().is_some_and(|extension| extension == "md"))
            .then(|| path.file_stem()?.to_str().map(str::to_owned))
            .flatten()
        })
        .collect();
    names.sort();
    Ok(names)
}

fn markdown_skill_count(directory: &Path) -> Result<usize> {
    let mut total = 0;
    for entry in fs::read_dir(directory)? {
        let entry = entry?;
        if entry.file_type()?.is_dir() && entry.path().join("SKILL.md").is_file() {
            total += 1;
        }
    }
    Ok(total)
}

fn parse_jsonc(path: &Path) -> Result<Value> {
    let text = fs::read_to_string(path)?;
    parse_to_serde_value(&text, &Default::default())
        .with_context(|| format!("Cannot parse {}", path.display()))
}

fn save(root: &Path, path: &str, contents: &str) -> Result<()> {
    let output = root.join(path);
    let parent = output.parent().context("Output path has no parent")?;
    fs::create_dir_all(parent)?;
    fs::write(output, contents)?;
    Ok(())
}

fn save_json(root: &Path, path: &str, value: &Value) -> Result<()> {
    save(
        root,
        path,
        &format!("{}\n", serde_json::to_string_pretty(value)?),
    )
}

fn adapt_prose(text: &str) -> String {
    text.replace("~/.config/opencode", "~/.config/tsuna")
        .replace("OpenCode 2", "OMP (tsuna)")
        .replace("OpenCode loads", "OMP loads")
        .replace("native subagent tool", "native task tool")
        .replace("`subagent`", "`task`")
        .replace("`shell`", "`bash`")
        .replace("`question`", "`ask`")
        .replace("webfetch", "read")
        .replace("websearch", "web_search")
}

fn expand_env(value: &str) -> String {
    let mut value = value.replace("{env:HOME}/.config/opencode", "${TSUNA_ROOT}");
    value = value.replace("$HOME/.config/opencode", "${TSUNA_ROOT}");
    let mut output = String::new();
    while let Some(start) = value.find("{env:") {
        output.push_str(&value[..start]);
        let remaining = &value[start + 5..];
        let Some(end) = remaining.find('}') else {
            output.push_str(&value[start..]);
            return output;
        };
        output.push_str("${");
        output.push_str(&remaining[..end]);
        output.push('}');
        value = remaining[end + 1..].to_owned();
    }
    output.push_str(&value);
    output
}

fn expand_map(source: &Map<String, Value>) -> Value {
    Value::Object(
        source
            .iter()
            .map(|(key, value)| {
                (
                    key.clone(),
                    Value::String(expand_env(value.as_str().unwrap_or_default())),
                )
            })
            .collect(),
    )
}

fn allows_spawn(rules: &Value, child: &str) -> bool {
    let mut effect = "ask";
    for rule in rules
        .as_array()
        .into_iter()
        .flatten()
        .filter_map(Value::as_object)
    {
        if glob_matches(
            rule.get("action")
                .and_then(Value::as_str)
                .unwrap_or_default(),
            "subagent",
        ) && glob_matches(
            rule.get("resource")
                .and_then(Value::as_str)
                .unwrap_or_default(),
            child,
        ) {
            effect = rule.get("effect").and_then(Value::as_str).unwrap_or(effect);
        }
    }
    effect == "allow"
}

fn glob_matches(pattern: &str, value: &str) -> bool {
    let (pattern, value) = (pattern.as_bytes(), value.as_bytes());
    let (mut p, mut v, mut star, mut retry) = (0, 0, None, 0);
    while v < value.len() {
        if p < pattern.len() && (pattern[p] == b'?' || pattern[p] == value[v]) {
            p += 1;
            v += 1;
        } else if p < pattern.len() && pattern[p] == b'*' {
            star = Some(p);
            p += 1;
            retry = v;
        } else if let Some(star) = star {
            p = star + 1;
            retry += 1;
            v = retry;
        } else {
            return false;
        }
    }
    pattern[p..].iter().all(|byte| *byte == b'*')
}

fn object<'a>(value: &'a Value, description: &str) -> Result<&'a Map<String, Value>> {
    value
        .as_object()
        .with_context(|| format!("{description} must be an object"))
}

fn string_field<'a>(
    object: &'a Map<String, Value>,
    name: &str,
    description: &str,
) -> Result<&'a str> {
    object
        .get(name)
        .and_then(Value::as_str)
        .with_context(|| format!("{description} has no {name}"))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn reads_lf_and_crlf_frontmatter() {
        for text in [
            "---\nname: demo\n---\nbody",
            "---\r\nname: demo\r\n---\r\nbody",
        ] {
            let parsed = frontmatter(text).unwrap();
            assert_eq!(parsed.meta["name"], "demo");
            assert_eq!(parsed.body, "body");
        }
        assert!(frontmatter("body only").is_err());
    }

    #[test]
    fn translates_models_and_mcp_semantics() {
        assert_eq!(
            model_selector("openai/gpt-6.1-sol-1m#high", true),
            "cliproxy-openai/gpt-6.1-sol:high"
        );
        assert_eq!(
            model_selector("openai/gpt-6.1-sol-1m#high", false),
            "openai-codex/gpt-6.1-sol:high"
        );
        let servers = json!({ "test": { "type": "local", "command": ["{env:HOME}/.config/opencode/bin/test", "arg"], "disabled": true, "environment": { "TOKEN": "{env:TOKEN}" }, "timeout": { "execution": 300000 } } });
        assert_eq!(
            translate_mcp(servers.as_object().unwrap()).unwrap(),
            json!({ "test": { "type": "stdio", "enabled": false, "command": "${TSUNA_ROOT}/bin/test", "args": ["arg"], "env": { "TOKEN": "${TOKEN}" }, "timeout": 300000 } })
        );
    }

    #[test]
    fn imports_a_jsonc_fixture_tree() {
        let temp = tempfile::tempdir().unwrap();
        let source = temp.path().join("source");
        let target = temp.path().join("target");
        for path in ["agents", "commands", "skills/demo", "scripts"] {
            fs::create_dir_all(source.join(path)).unwrap();
        }
        fs::write(source.join("opencode.json"), "{ // JSONC\n \"plugins\": [{\"package\": \"@scope/example\"}], \"mcp\": {\"servers\": {}} }").unwrap();
        fs::write(source.join("agents/root.md"), "---\ndescription: Root\nmode: primary\nmodel: openai/gpt-6-luna#low\npermissions:\n  - action: subagent\n    resource: child\n    effect: allow\n---\nOpenCode 2 uses `shell`.").unwrap();
        fs::write(source.join("agents/child.md"), "---\ndescription: Child\nmode: subagent\nmodel: anthropic/claude-opus-5-5#medium\npermissions: []\n---\nbody").unwrap();
        fs::write(
            source.join("commands/demo.md"),
            "---\nagent: root\ndescription: Demo\n---\nwebsearch",
        )
        .unwrap();
        fs::write(source.join("skills/demo/SKILL.md"), "Use OpenCode loads.").unwrap();
        fs::write(source.join("AGENTS.md"), "Use native subagent tool.").unwrap();
        for path in [
            "LICENSE",
            "NOTICE",
            "scripts/check-workplan.ts",
            "scripts/agent-permissions.yaml",
            "dcp.jsonc",
            "cli.json",
        ] {
            fs::write(source.join(path), "fixture").unwrap();
        }
        import_config(&source, &target).unwrap();
        let roles: Value =
            serde_json::from_slice(&fs::read(target.join("config/roles.json")).unwrap()).unwrap();
        assert_eq!(roles["root"]["model"], "cliproxy-openai/gpt-6-luna:low");
        let agent = fs::read_to_string(target.join("agent/agents/root.md")).unwrap();
        assert!(agent.contains("spawns:\n- child"));
        assert!(
            fs::read_to_string(target.join("agent/prompts/root.md"))
                .unwrap()
                .contains("OMP (tsuna) uses `bash`")
        );
        assert_eq!(
            fs::read_to_string(target.join("source-config/cli.json")).unwrap(),
            "fixture"
        );
    }
}
