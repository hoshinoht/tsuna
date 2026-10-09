// Package config resolves gofetch's runtime configuration from flags and
// the environment. Credentials are read here and never logged.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	EnvExaKey     = "EXA_API_KEY"
	EnvExaKeyFile = "GOFETCH_EXA_KEY_FILE"
	// KeyFileNone disables key-file lookup, including the legacy path.
	KeyFileNone = "none"
)

// legacyKeyFile is the OpenCode exa MCP key file earlier versions read
// implicitly. It is still honoured, with a startup notice, when nothing
// explicit is configured.
var legacyKeyFile = filepath.Join(".config", "opencode", ".exa-api-key")

type Config struct {
	ExaAPIKey string
	// ExaKeySource says where the key came from ("env EXA_API_KEY",
	// "file <path>", "legacy file <path>") or is empty when there is none.
	ExaKeySource string
	// Notices are startup messages for stderr; they never contain a key.
	Notices []string
}

// Load resolves configuration. Precedence for the Exa key:
//  1. EXA_API_KEY
//  2. the key file from -exa-key-file, else GOFETCH_EXA_KEY_FILE
//     ("none" disables key files entirely)
//  3. the legacy ~/.config/opencode/.exa-api-key, if it exists
func Load(keyFileFlag string, getenv func(string) string) Config {
	var cfg Config
	if key := strings.TrimSpace(getenv(EnvExaKey)); key != "" {
		cfg.ExaAPIKey, cfg.ExaKeySource = key, "env "+EnvExaKey
		return cfg
	}

	path, origin := strings.TrimSpace(keyFileFlag), "-exa-key-file"
	if path == "" {
		path, origin = strings.TrimSpace(getenv(EnvExaKeyFile)), EnvExaKeyFile
	}
	if path == KeyFileNone {
		return cfg
	}
	if path != "" {
		path = expandHome(path, getenv)
		key, err := readKeyFile(path)
		if err != nil {
			cfg.Notices = append(cfg.Notices, fmt.Sprintf("Exa key file from %s unusable (%v); Exa search disabled", origin, err))
			return cfg
		}
		cfg.ExaAPIKey, cfg.ExaKeySource = key, "file "+path
		return cfg
	}

	if home := homeDir(getenv); home != "" {
		legacy := filepath.Join(home, legacyKeyFile)
		if key, err := readKeyFile(legacy); err == nil {
			cfg.ExaAPIKey, cfg.ExaKeySource = key, "legacy file "+legacy
			cfg.Notices = append(cfg.Notices, fmt.Sprintf(
				"using legacy Exa key file %s; set %s (or -exa-key-file) to make this explicit, or %s=%s to disable it",
				legacy, EnvExaKeyFile, EnvExaKeyFile, KeyFileNone))
		}
	}
	return cfg
}

func readKeyFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("%s does not exist", path)
		}
		return "", fmt.Errorf("cannot read %s", path)
	}
	key := strings.TrimSpace(string(data))
	if key == "" {
		return "", fmt.Errorf("%s is empty", path)
	}
	return key, nil
}

func expandHome(path string, getenv func(string) string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home := homeDir(getenv); home != "" {
			return filepath.Join(home, strings.TrimPrefix(path, "~"))
		}
	}
	return path
}

func homeDir(getenv func(string) string) string {
	if h := getenv("HOME"); h != "" {
		return h
	}
	h, _ := os.UserHomeDir()
	return h
}
