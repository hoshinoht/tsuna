package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func writeKey(t *testing.T, path, key string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(key+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadPrecedence(t *testing.T) {
	home := t.TempDir()
	legacy := filepath.Join(home, ".config", "opencode", ".exa-api-key")
	explicit := filepath.Join(home, "keys", "exa")
	writeKey(t, legacy, "legacy-key")
	writeKey(t, explicit, "explicit-key")

	cases := []struct {
		name, flag string
		env        map[string]string
		key, src   string
	}{
		{"env wins", "~/keys/exa", map[string]string{"HOME": home, EnvExaKey: "env-key"}, "env-key", "env EXA_API_KEY"},
		{"flag with ~", "~/keys/exa", map[string]string{"HOME": home}, "explicit-key", "file " + explicit},
		{"env file", "", map[string]string{"HOME": home, EnvExaKeyFile: explicit}, "explicit-key", "file " + explicit},
		{"legacy fallback", "", map[string]string{"HOME": home}, "legacy-key", "legacy file " + legacy},
		{"none disables", "", map[string]string{"HOME": home, EnvExaKeyFile: "none"}, "", ""},
		{"missing explicit file does not fall back", "/nonexistent/key", map[string]string{"HOME": home}, "", ""},
	}
	for _, c := range cases {
		cfg := Load(c.flag, env(c.env))
		if cfg.ExaAPIKey != c.key || cfg.ExaKeySource != c.src {
			t.Errorf("%s: key=%q src=%q, want %q %q", c.name, cfg.ExaAPIKey, cfg.ExaKeySource, c.key, c.src)
		}
		for _, n := range cfg.Notices {
			if strings.Contains(n, "-key") && (strings.Contains(n, "legacy-key") || strings.Contains(n, "explicit-key") || strings.Contains(n, "env-key")) {
				t.Errorf("%s: notice leaks the key: %q", c.name, n)
			}
		}
	}

	if cfg := Load("", env(map[string]string{"HOME": home})); len(cfg.Notices) != 1 || !strings.Contains(cfg.Notices[0], "legacy") {
		t.Errorf("legacy use must be announced: %v", cfg.Notices)
	}
	if cfg := Load("/nonexistent/key", env(map[string]string{"HOME": home})); len(cfg.Notices) != 1 {
		t.Errorf("unusable explicit key file must be announced: %v", cfg.Notices)
	}
}
