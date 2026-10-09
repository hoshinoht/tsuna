use anyhow::{Context, Result, bail};
use std::fs;
use std::os::unix::fs::{PermissionsExt, symlink};
use std::path::{Path, PathBuf};
use std::time::{SystemTime, UNIX_EPOCH};

pub fn install_path(root: &Path, home: &Path) -> Result<()> {
    let directory = home.join(".local/bin");
    fs::create_dir_all(&directory)?;
    for name in ["tsuna", "hoshi-omp"] {
        install_command(&directory, &root.join("bin").join(name), name)?;
    }
    Ok(())
}

fn install_command(directory: &Path, target: &Path, name: &str) -> Result<()> {
    fs::set_permissions(target, fs::Permissions::from_mode(0o755))?;
    let link = directory.join(name);
    match fs::symlink_metadata(&link) {
        Ok(info) => {
            if info.file_type().is_symlink()
                && fs::canonicalize(&link).ok() == Some(fs::canonicalize(target)?)
            {
                return Ok(());
            }
            bail!("Refusing to replace existing command: {}", link.display());
        }
        Err(err) if err.kind() == std::io::ErrorKind::NotFound => {}
        Err(err) => return Err(err.into()),
    }
    symlink(target, &link)?;
    println!("Installed {} -> {}", link.display(), target.display());
    Ok(())
}

fn copy_missing(source: &Path, target: &Path) -> Result<()> {
    let metadata = fs::symlink_metadata(source)?;
    if metadata.is_dir() {
        fs::create_dir_all(target)?;
        for entry in fs::read_dir(source)? {
            let entry = entry?;
            copy_missing(&entry.path(), &target.join(entry.file_name()))?;
        }
    } else {
        if fs::symlink_metadata(target).is_ok() {
            return Ok(());
        }
        if metadata.file_type().is_symlink() {
            symlink(fs::read_link(source)?, target)?;
        } else {
            // create_new preserves a marker created concurrently by the existing profile.
            let mut out = match fs::OpenOptions::new()
                .write(true)
                .create_new(true)
                .open(target)
            {
                Ok(file) => file,
                Err(err) if err.kind() == std::io::ErrorKind::AlreadyExists => return Ok(()),
                Err(err) => return Err(err.into()),
            };
            std::io::copy(&mut fs::File::open(source)?, &mut out)?;
            fs::set_permissions(target, metadata.permissions())?;
        }
    }
    Ok(())
}

pub fn connect(root: &Path, home: &Path) -> Result<Option<PathBuf>> {
    let target = root.join(".runtime/omp/agent");
    if !fs::symlink_metadata(&target)
        .map(|m| m.is_dir())
        .unwrap_or(false)
    {
        bail!("Prepare the Tsuna agent profile before connecting the official CLI");
    }
    let base = home.join(".omp");
    fs::create_dir_all(&base)?;
    let agent_dir = base.join("agent");
    let existing = match fs::symlink_metadata(&agent_dir) {
        Ok(info) => Some(info),
        Err(err) if err.kind() == std::io::ErrorKind::NotFound => None,
        Err(err) => return Err(err.into()),
    };
    if existing
        .as_ref()
        .is_some_and(|m| m.file_type().is_symlink())
        && fs::canonicalize(&agent_dir).ok() == Some(fs::canonicalize(&target)?)
    {
        return Ok(None);
    }
    let backup = if existing.is_some() {
        let timestamp = SystemTime::now().duration_since(UNIX_EPOCH)?.as_nanos();
        let backup = base.join(format!(
            "agent-before-tsuna-{timestamp}-{}",
            std::process::id()
        ));
        if fs::symlink_metadata(&backup).is_ok() {
            bail!("Backup already exists: {}", backup.display());
        }
        fs::rename(&agent_dir, &backup)?;
        Some(backup)
    } else {
        None
    };
    let result: Result<()> = (|| {
        if let Some(backup) = &backup {
            let markers = backup.join("custom-session-files");
            if fs::symlink_metadata(&markers).is_ok() {
                copy_missing(&markers, &target.join("custom-session-files"))?;
            }
        }
        symlink(&target, &agent_dir)?;
        Ok(())
    })();
    if let Err(error) = result {
        if let Some(backup) = &backup {
            fs::rename(backup, &agent_dir).with_context(|| {
                format!(
                    "Profile connection failed ({error}); restore {} manually",
                    backup.display()
                )
            })?;
        }
        return Err(error);
    }
    Ok(backup)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn connection_preserves_old_state_and_existing_markers() {
        let temp = tempfile::tempdir().unwrap();
        let home = temp.path();
        let root = home.join("repo");
        let target = root.join(".runtime/omp/agent");
        fs::create_dir_all(target.join("custom-session-files")).unwrap();
        fs::create_dir_all(home.join(".omp/agent/custom-session-files")).unwrap();
        fs::write(home.join(".omp/agent/keep.txt"), "old state").unwrap();
        fs::write(
            home.join(".omp/agent/custom-session-files/old"),
            "old marker",
        )
        .unwrap();
        fs::write(target.join("custom-session-files/old"), "existing marker").unwrap();
        fs::write(
            home.join(".omp/agent/custom-session-files/other"),
            "other marker",
        )
        .unwrap();
        let backup = connect(&root, home).unwrap().unwrap();
        assert_eq!(
            fs::read_to_string(backup.join("keep.txt")).unwrap(),
            "old state"
        );
        assert_eq!(
            fs::read_to_string(target.join("custom-session-files/old")).unwrap(),
            "existing marker"
        );
        assert_eq!(
            fs::read_to_string(target.join("custom-session-files/other")).unwrap(),
            "other marker"
        );
        assert_eq!(fs::read_link(home.join(".omp/agent")).unwrap(), target);
        assert!(connect(&root, home).unwrap().is_none());
    }

    #[test]
    fn install_refuses_existing_command_and_is_idempotent() {
        let temp = tempfile::tempdir().unwrap();
        let root = temp.path().join("repo");
        fs::create_dir_all(root.join("bin")).unwrap();
        fs::write(root.join("bin/tsuna"), "fixture").unwrap();
        fs::write(root.join("bin/hoshi-omp"), "fixture").unwrap();
        install_path(&root, temp.path()).unwrap();
        install_path(&root, temp.path()).unwrap();
        assert_eq!(
            fs::read_link(temp.path().join(".local/bin/hoshi-omp")).unwrap(),
            root.join("bin/hoshi-omp")
        );
        let link = temp.path().join(".local/bin/tsuna");
        fs::remove_file(&link).unwrap();
        fs::write(&link, "existing").unwrap();
        assert!(install_path(&root, temp.path()).is_err());
        assert_eq!(fs::read_to_string(&link).unwrap(), "existing");
    }
}
