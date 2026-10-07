// Package testutil provides corpus helpers for tests: locating testdata,
// materialising fixtures at a root whose length equals the generation root
// (budget-sensitive vectors measure lengths before $ROOT substitution), and
// read-only fingerprints for the no-write/no-mtime gate.
package testutil

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
)

// RepoRoot returns the repository root.
func RepoRoot() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

// Testdata joins path elements under testdata/.
func Testdata(elem ...string) string {
	return filepath.Join(append([]string{RepoRoot(), "testdata"}, elem...)...)
}

// GenerationPrefix is the harness root prefix recorded in MANIFEST.json.
const GenerationPrefix = "/private/tmp/shiori-fx/run/"

// Root is a materialised fixture.
type Root struct {
	Path    string // canonical absolute root
	GenRoot string // the oracle's generation root for this fixture
}

const alnum = "abcdefghijklmnopqrstuvwxyz0123456789"

// NewRoot copies testdata/fixtures/<fixture> to a fresh directory. On
// Darwin/Linux it uses /private/tmp/sh-XXXXXX/run/<fixture> (or /tmp when
// /private/tmp is absent), which has exactly the generation root's length,
// so byte-length budgets match. Callers must check SameLength before
// comparing budget-sensitive bytes.
func NewRoot(t testing.TB, fixture string) Root {
	t.Helper()
	gen := GenerationPrefix + fixture
	base := "/private/tmp"
	if st, err := os.Stat(base); err != nil || !st.IsDir() {
		base = os.TempDir()
	}
	var dir string
	for attempt := 0; ; attempt++ {
		b := make([]byte, 6)
		if _, err := rand.Read(b); err != nil {
			t.Fatal(err)
		}
		for i := range b {
			b[i] = alnum[int(b[i])%len(alnum)]
		}
		dir = filepath.Join(base, "sh-"+string(b))
		if err := os.Mkdir(dir, 0o755); err == nil {
			break
		} else if attempt > 20 {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	root := filepath.Join(dir, "run", fixture)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := CopyTree(Testdata("fixtures", fixture), root); err != nil {
		t.Fatal(err)
	}
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	return Root{Path: real, GenRoot: gen}
}

// SameLength reports whether the root has the generation root's length.
func (r Root) SameLength() bool { return len(r.Path) == len(r.GenRoot) }

// CopyTree copies regular files and directories, preserving permission
// bits and modification times.
func CopyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		if d.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm()|0o700)
		}
		if name := d.Name(); name == ".DS_Store" {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if err := os.WriteFile(target, data, info.Mode().Perm()); err != nil {
			return err
		}
		return os.Chtimes(target, info.ModTime(), info.ModTime())
	})
}

// Normalize converts engine output produced at r.Path into the corpus
// form: the full root becomes $ROOT and a truncated root prefix
// ("<prefix>…", cut before substitution in the reference) is mapped onto
// the generation root's prefix of the same length.
func (r Root) Normalize(s string) string {
	s = strings.ReplaceAll(s, r.Path, "$ROOT")
	if !strings.Contains(s, "…") {
		return s
	}
	n := len(r.Path)
	if len(r.GenRoot) < n {
		n = len(r.GenRoot)
	}
	for k := n - 1; k >= 1; k-- {
		from := r.Path[:k] + "…"
		if strings.Contains(s, from) {
			s = strings.ReplaceAll(s, from, r.GenRoot[:k]+"…")
		}
	}
	return s
}

// FileState is the byte/mtime fingerprint of one path.
type FileState struct {
	Mode    fs.FileMode
	Size    int64
	ModTime time.Time
	SHA     string
}

// Fingerprint records every file and directory under root (bytes, mode,
// mtime) so a test can prove a read path wrote nothing.
func Fingerprint(t testing.TB, root string) map[string]FileState {
	t.Helper()
	out := map[string]FileState{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		st := FileState{Mode: info.Mode(), Size: info.Size(), ModTime: info.ModTime()}
		if info.Mode().IsRegular() {
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			st.SHA = fmt.Sprintf("%x", sha256.Sum256(data))
		}
		rel, _ := filepath.Rel(root, p)
		out[rel] = st
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// DiffFingerprints describes differences between two fingerprints.
func DiffFingerprints(before, after map[string]FileState) []string {
	var diffs []string
	for k, b := range before {
		a, ok := after[k]
		switch {
		case !ok:
			diffs = append(diffs, "removed "+k)
		case a.SHA != b.SHA || a.Size != b.Size:
			diffs = append(diffs, "content changed "+k)
		case !a.ModTime.Equal(b.ModTime):
			diffs = append(diffs, "mtime changed "+k)
		case a.Mode != b.Mode:
			diffs = append(diffs, "mode changed "+k)
		}
	}
	for k := range after {
		if _, ok := before[k]; !ok {
			diffs = append(diffs, "created "+k)
		}
	}
	sort.Strings(diffs)
	return diffs
}

// ReadJSON decodes a JSON file into v.
func ReadJSON(t testing.TB, path string, v any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(v); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}
