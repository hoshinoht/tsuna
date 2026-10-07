package snapshot

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hoshinoht/shiori/internal/model"
	"github.com/hoshinoht/shiori/internal/testutil"
)

type fixtureVector struct {
	ID      string `json:"id"`
	Fixture string `json:"fixture"`
	PlanID  string `json:"planId"`
	Expect  struct {
		Kind                 string           `json:"kind"`
		Message              string           `json:"message"`
		PlanManifest         []map[string]any `json:"planManifest"`
		StateManifest        []map[string]any `json:"stateManifest"`
		PlanHash             string           `json:"planHash"`
		StateHash            string           `json:"stateHash"`
		MissingPlanArtifacts []string         `json:"missingPlanArtifacts"`
		NormalizedPlanFile   string           `json:"normalizedPlanFile"`
		NormalizedSpecFiles  []string         `json:"normalizedSpecFiles"`
	} `json:"expect"`
}

func entriesEqual(t *testing.T, got []Entry, want []Entry) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("manifest length %d want %d: %+v", len(got), len(want), got)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("entry %d = %+v want %+v", i, got[i], want[i])
		}
	}
}

// TestFixtureSnapshots checks every per-fixture snapshot vector: manifest
// membership and order, hashes, missing artifacts, path normalization and
// the error cases (engine-specific JSON parse text is compared by
// prefix).
func TestFixtureSnapshots(t *testing.T) {
	files, _ := filepath.Glob(testutil.Testdata("vectors", "hash", "fixtures", "*.json"))
	if len(files) != 29 {
		t.Fatalf("expected 29 snapshot vectors, found %d", len(files))
	}
	for _, f := range files {
		var v fixtureVector
		data, _ := os.ReadFile(f)
		if err := json.Unmarshal(data, &v); err != nil {
			t.Fatal(err)
		}
		t.Run(v.ID, func(t *testing.T) {
			root := testutil.NewRoot(t, v.Fixture)
			before := testutil.Fingerprint(t, root.Path)
			id, nerr := model.NormalizeID(v.PlanID)
			if nerr != nil {
				t.Fatal(nerr)
			}
			s, err := Load(root.Path, id, DefaultLimits)
			if d := testutil.DiffFingerprints(before, testutil.Fingerprint(t, root.Path)); len(d) > 0 {
				t.Fatalf("read path modified the workspace: %v", d)
			}
			if v.Expect.Kind == "error" {
				if err == nil {
					t.Fatalf("expected error %q", v.Expect.Message)
				}
				got := root.Normalize(err.Error())
				want, _ := testutil.AdaptCaseLookup(v.Fixture, v.Expect.Message)
				if i := strings.Index(want, ": JSON Parse error:"); i >= 0 {
					if !strings.HasPrefix(got, want[:i+2]) {
						t.Fatalf("error prefix\n got %q\nwant %q", got, want[:i+2])
					}
					return
				}
				if got != want {
					t.Fatalf("error\n got %q\nwant %q", got, want)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			conv := func(raw []map[string]any) []Entry {
				out := []Entry{}
				for _, m := range raw {
					e := Entry{Path: m["path"].(string)}
					if h, ok := m["sha256"].(string); ok {
						e.SHA256 = h
					} else {
						e.Missing = true
					}
					out = append(out, e)
				}
				return out
			}
			entriesEqual(t, s.PlanManifest, conv(v.Expect.PlanManifest))
			entriesEqual(t, s.StateManifest, conv(v.Expect.StateManifest))
			if s.PlanHash != v.Expect.PlanHash || s.StateHash != v.Expect.StateHash {
				t.Fatalf("hashes %s/%s want %s/%s", s.PlanHash, s.StateHash, v.Expect.PlanHash, v.Expect.StateHash)
			}
			if s.Plan.PlanFile != v.Expect.NormalizedPlanFile {
				t.Fatalf("planFile %q want %q", s.Plan.PlanFile, v.Expect.NormalizedPlanFile)
			}
			if strings.Join(s.Plan.SpecFiles, "\n") != strings.Join(v.Expect.NormalizedSpecFiles, "\n") {
				t.Fatalf("specFiles %q want %q", s.Plan.SpecFiles, v.Expect.NormalizedSpecFiles)
			}
			missing := s.MissingPlanArtifacts
			if missing == nil {
				missing = []string{}
			}
			if strings.Join(missing, "\n") != strings.Join(v.Expect.MissingPlanArtifacts, "\n") {
				t.Fatalf("missing %q want %q", missing, v.Expect.MissingPlanArtifacts)
			}
		})
	}
}

// TestInvalidationTable reproduces hash/invalidation (acceptance C04): which
// edits change planHash/stateHash. Rows whose exact edit bytes are not
// recorded are checked by their changed/unchanged flags; unchanged rows are
// checked against the recorded hashes exactly.
func TestInvalidationTable(t *testing.T) {
	var table struct {
		Rows []struct {
			Edit             string `json:"edit"`
			PlanHash         string `json:"planHash"`
			StateHash        string `json:"stateHash"`
			PlanHashChanged  bool   `json:"planHashChanged"`
			StateHashChanged bool   `json:"stateHashChanged"`
		} `json:"rows"`
	}
	data, _ := os.ReadFile(testutil.Testdata("vectors", "hash", "invalidation.json"))
	if err := json.Unmarshal(data, &table); err != nil {
		t.Fatal(err)
	}
	wp := func(root string, name string) string { return filepath.Join(root, ".opencode", "workplan", name) }
	edits := map[string]func(root string) error{
		"baseline":             func(string) error { return nil },
		"json-byte-change":     func(r string) error { return appendByte(wp(r, "full-plan.json")) },
		"markdown-byte-change": func(r string) error { return appendByte(wp(r, "full-plan.md")) },
		"spec-byte-change":     func(r string) error { return appendByte(filepath.Join(r, "docs", "spec-a.md")) },
		"spec-deleted":         func(r string) error { return os.Remove(filepath.Join(r, "docs", "spec-a.md")) },
		"checkpoint-change":    func(r string) error { return appendByte(wp(r, "full-plan.checkpoint.json")) },
		"dependencies-deleted": func(r string) error { return os.Remove(wp(r, "full-plan.dependencies.json")) },
		"journal-added": func(r string) error {
			return os.WriteFile(wp(r, "full-plan.transaction.json"), []byte("{}\n"), 0o600)
		},
		"lock-and-stage-added": func(r string) error {
			if err := os.WriteFile(wp(r, ".full-plan.lock"), []byte("{}"), 0o600); err != nil {
				return err
			}
			return os.WriteFile(wp(r, ".full-plan.json.x.0.stage"), []byte("x"), 0o600)
		},
		"unrelated-file-added": func(r string) error { return os.WriteFile(wp(r, "notes.txt"), []byte("x"), 0o600) },
		"mtime-only-touch": func(r string) error {
			future := time.Now().Add(time.Hour)
			return os.Chtimes(wp(r, "full-plan.json"), future, future)
		},
	}
	var base *Snapshot
	for _, row := range table.Rows {
		t.Run(row.Edit, func(t *testing.T) {
			edit, ok := edits[row.Edit]
			if !ok {
				t.Fatalf("no edit implementation for %q", row.Edit)
			}
			root := testutil.NewRoot(t, "full-valid")
			if err := edit(root.Path); err != nil {
				t.Fatal(err)
			}
			s, err := Load(root.Path, "full-plan", DefaultLimits)
			if err != nil {
				t.Fatal(err)
			}
			if row.Edit == "baseline" {
				base = s
			}
			if base == nil {
				t.Fatal("baseline row must come first")
			}
			if (s.PlanHash != base.PlanHash) != row.PlanHashChanged || (s.StateHash != base.StateHash) != row.StateHashChanged {
				t.Fatalf("changed flags plan=%v state=%v want %v/%v", s.PlanHash != base.PlanHash, s.StateHash != base.StateHash, row.PlanHashChanged, row.StateHashChanged)
			}
			if !row.PlanHashChanged && s.PlanHash != row.PlanHash {
				t.Fatalf("planHash %s want %s", s.PlanHash, row.PlanHash)
			}
			if !row.StateHashChanged && s.StateHash != row.StateHash {
				t.Fatalf("stateHash %s want %s", s.StateHash, row.StateHash)
			}
		})
	}
}

func appendByte(path string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write([]byte(" "))
	return err
}
