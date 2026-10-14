use crate::setup::{RUNTIME_AGENT, copy_dir_overwrite};
use anyhow::{Context, Result, bail};
use serde_json::Value;
use std::ffi::OsString;
use std::fs;
use std::path::Path;

const USAGE: &str = "Usage: tsuna theme <list|current|use NAME>";

pub fn run(root: &Path, args: &[OsString]) -> Result<i32> {
    let args: Vec<&str> = args
        .iter()
        .map(|arg| arg.to_str().context("Theme arguments must be UTF-8"))
        .collect::<Result<_>>()?;
    match args.as_slice() {
        ["list"] => {
            let active = active_theme(root)?;
            for name in tracked_themes(root)? {
                let marker = if active.as_deref() == Some(name.as_str()) {
                    "*"
                } else {
                    " "
                };
                println!("{marker} {name}");
            }
            Ok(0)
        }
        ["current"] => {
            match active_theme(root)? {
                Some(name) => println!("{name}"),
                None => println!("(OMP default)"),
            }
            Ok(0)
        }
        ["use", name] => {
            use_theme(root, name)?;
            println!("Dark theme set to {name}.");
            Ok(0)
        }
        _ => {
            eprintln!("{USAGE}");
            Ok(2)
        }
    }
}

/// Theme names are the file stems under `agent/themes`, which OMP resolves by name.
fn tracked_themes(root: &Path) -> Result<Vec<String>> {
    let mut names = Vec::new();
    for entry in fs::read_dir(root.join("agent/themes")).context("Cannot read agent/themes")? {
        let path = entry?.path();
        if path.extension().is_some_and(|ext| ext == "json")
            && let Some(stem) = path.file_stem().and_then(|stem| stem.to_str())
        {
            names.push(stem.to_owned());
        }
    }
    names.sort();
    Ok(names)
}

fn active_theme(root: &Path) -> Result<Option<String>> {
    let text = fs::read_to_string(root.join("config/omp.yml"))?;
    let config: Value = serde_saphyr::from_str(&text).context("Cannot parse config/omp.yml")?;
    Ok(config
        .pointer("/theme/dark")
        .and_then(Value::as_str)
        .map(str::to_owned))
}

fn use_theme(root: &Path, name: &str) -> Result<()> {
    let themes = tracked_themes(root)?;
    if !themes.iter().any(|theme| theme == name) {
        bail!("Unknown theme {name}; available: {}", themes.join(", "));
    }
    let tracked = root.join("config/omp.yml");
    fs::write(
        &tracked,
        set_dark_theme(&fs::read_to_string(&tracked)?, name),
    )?;

    // An existing profile picks the change up without a full `tsuna setup`.
    let runtime = root.join(RUNTIME_AGENT);
    let config = runtime.join("config.yml");
    if config.is_file() {
        copy_dir_overwrite(&root.join("agent/themes"), &runtime.join("themes"))?;
        fs::write(&config, set_dark_theme(&fs::read_to_string(&config)?, name))?;
    }
    Ok(())
}

/// Sets `theme.dark` by editing lines, so comments and the rest of the file stay untouched.
fn set_dark_theme(text: &str, name: &str) -> String {
    let mut lines: Vec<String> = text.lines().map(str::to_owned).collect();
    let entry = format!("  dark: {name}");
    match lines.iter().position(|line| line.trim_end() == "theme:") {
        Some(start) => {
            let block_end = lines[start + 1..]
                .iter()
                .position(|line| !line.is_empty() && !line.starts_with([' ', '\t']))
                .map_or(lines.len(), |offset| start + 1 + offset);
            match lines[start + 1..block_end]
                .iter()
                .position(|line| line.trim_start().starts_with("dark:"))
            {
                Some(offset) => lines[start + 1 + offset] = entry,
                None => lines.insert(start + 1, entry),
            }
        }
        None => {
            lines.push("theme:".into());
            lines.push(entry);
        }
    }
    let mut result = lines.join("\n");
    result.push('\n');
    result
}

#[cfg(test)]
mod tests {
    use super::*;

    fn fixture() -> tempfile::TempDir {
        let dir = tempfile::tempdir().unwrap();
        let root = dir.path();
        fs::create_dir_all(root.join("agent/themes")).unwrap();
        fs::create_dir_all(root.join("config")).unwrap();
        for name in ["dusk-darker", "siesta"] {
            fs::write(root.join(format!("agent/themes/{name}.json")), "{}").unwrap();
        }
        fs::write(root.join("agent/themes/notes.md"), "not a theme").unwrap();
        fs::write(
            root.join("config/omp.yml"),
            "# Keep this comment.\ntheme:\n  dark: dusk-darker\nsymbolPreset: nerd\n",
        )
        .unwrap();
        dir
    }

    #[test]
    fn lists_json_stems_in_order() {
        let dir = fixture();
        assert_eq!(
            tracked_themes(dir.path()).unwrap(),
            ["dusk-darker", "siesta"]
        );
        assert_eq!(
            active_theme(dir.path()).unwrap().as_deref(),
            Some("dusk-darker")
        );
    }

    #[test]
    fn use_updates_tracked_and_runtime_config() {
        let dir = fixture();
        let root = dir.path();
        let config = root.join(RUNTIME_AGENT).join("config.yml");
        fs::create_dir_all(root.join(RUNTIME_AGENT)).unwrap();
        fs::write(&config, "theme:\n  dark: dusk-darker\nsetupVersion: 3\n").unwrap();

        use_theme(root, "siesta").unwrap();

        assert_eq!(
            fs::read_to_string(root.join("config/omp.yml")).unwrap(),
            "# Keep this comment.\ntheme:\n  dark: siesta\nsymbolPreset: nerd\n"
        );
        assert_eq!(
            fs::read_to_string(&config).unwrap(),
            "theme:\n  dark: siesta\nsetupVersion: 3\n"
        );
        assert!(
            root.join(RUNTIME_AGENT)
                .join("themes/siesta.json")
                .is_file()
        );
    }

    #[test]
    fn use_rejects_unknown_theme_without_writing() {
        let dir = fixture();
        let before = fs::read_to_string(dir.path().join("config/omp.yml")).unwrap();
        let error = use_theme(dir.path(), "nope").unwrap_err().to_string();
        assert!(error.contains("available: dusk-darker, siesta"), "{error}");
        assert_eq!(
            fs::read_to_string(dir.path().join("config/omp.yml")).unwrap(),
            before
        );
    }

    #[test]
    fn set_dark_theme_handles_missing_entries() {
        assert_eq!(
            set_dark_theme("theme:\n  light: dawn\nother: 1\n", "siesta"),
            "theme:\n  dark: siesta\n  light: dawn\nother: 1\n"
        );
        assert_eq!(
            set_dark_theme("other: 1\n", "siesta"),
            "other: 1\ntheme:\n  dark: siesta\n"
        );
    }
}
