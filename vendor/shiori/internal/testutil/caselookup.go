package testutil

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// The corpus was recorded on APFS, where `UPPER` opens `UPPER.json`; a
// case-sensitive filesystem reports it missing (contracts §10).
const (
	caseLookupFixture = "list-mixed"
	caseLookupMissing = "Workplan file not found: $ROOT/.opencode/workplan/upper.json"
)

var (
	caseOnce      sync.Once
	caseSensitive bool
	caseOracleMsg string
)

// CaseSensitive reports whether NewRoot's filesystem is case-sensitive.
func CaseSensitive() bool {
	caseOnce.Do(func() {
		base := "/private/tmp"
		if st, err := os.Stat(base); err != nil || !st.IsDir() {
			base = os.TempDir()
		}
		dir, err := os.MkdirTemp(base, "sh-case-")
		if err != nil {
			return
		}
		defer os.RemoveAll(dir)
		if err := os.WriteFile(filepath.Join(dir, "probe"), nil, 0o644); err != nil {
			return
		}
		_, err = os.Stat(filepath.Join(dir, "PROBE"))
		caseSensitive = os.IsNotExist(err)
		var v struct {
			Expect struct {
				Message string `json:"message"`
			} `json:"expect"`
		}
		data, err := os.ReadFile(Testdata("vectors", "hash", "fixtures", "list-mixed--UPPER.json"))
		if err == nil && json.Unmarshal(data, &v) == nil {
			caseOracleMsg = v.Expect.Message
		}
	})
	return caseSensitive
}

// AdaptCaseLookup rewrites APFS oracle text (plain and JSON-escaped) to the
// case-sensitive "not found", reporting whether anything changed.
func AdaptCaseLookup(fixture, s string) (string, bool) {
	if fixture != caseLookupFixture || !CaseSensitive() || caseOracleMsg == "" {
		return s, false
	}
	out := strings.ReplaceAll(s, caseOracleMsg, caseLookupMissing)
	out = strings.ReplaceAll(out, jsonInner(caseOracleMsg), jsonInner(caseLookupMissing))
	return out, out != s
}

func jsonInner(s string) string {
	b, _ := json.Marshal(s)
	return string(b[1 : len(b)-1])
}

// CaseLookupMissing reports output carrying the case-sensitive "not found",
// to which APFS pins do not apply.
func CaseLookupMissing(fixture, s string) bool {
	if fixture != caseLookupFixture || !CaseSensitive() {
		return false
	}
	return strings.Contains(s, "/.opencode/workplan/upper.json")
}
