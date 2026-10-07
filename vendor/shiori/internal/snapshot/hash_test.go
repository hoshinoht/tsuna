package snapshot

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type manifestCase struct {
	Name                  string `json:"name"`
	Manifest              []map[string]any
	SortedPaths           []string `json:"sortedPaths"`
	CanonicalManifestJSON string   `json:"canonicalManifestJson"`
	CanonicalUTF8Hex      string   `json:"canonicalManifestJsonUtf8Hex"`
	PlanHash              string   `json:"planHash"`
	StateHash             string   `json:"stateHash"`
}

func toEntries(t *testing.T, raw []map[string]any) []Entry {
	t.Helper()
	out := make([]Entry, 0, len(raw))
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

// TestManifestHashVectors checks every synthetic manifest vector: UTF-16
// ordering, JSON.stringify escaping, key order and both hash labels.
func TestManifestHashVectors(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "vectors", "hash", "manifest-hash.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct{ Cases []manifestCase }
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Cases) == 0 {
		t.Fatal("no cases")
	}
	for _, c := range doc.Cases {
		t.Run(c.Name, func(t *testing.T) {
			entries := toEntries(t, c.Manifest)
			sorted := SortEntries(entries)
			for i, e := range sorted {
				if e.Path != c.SortedPaths[i] {
					t.Fatalf("sorted[%d] = %q, want %q", i, e.Path, c.SortedPaths[i])
				}
			}
			got := ManifestJSON(entries)
			if string(got) != c.CanonicalManifestJSON {
				t.Fatalf("manifest JSON\n got %s\nwant %s", got, c.CanonicalManifestJSON)
			}
			if hex.EncodeToString(got) != c.CanonicalUTF8Hex {
				t.Fatalf("utf8 hex mismatch")
			}
			if h := ManifestHash(PlanHashVersion, entries); h != c.PlanHash {
				t.Errorf("planHash %s want %s", h, c.PlanHash)
			}
			if h := ManifestHash(StateHashVersion, entries); h != c.StateHash {
				t.Errorf("stateHash %s want %s", h, c.StateHash)
			}
		})
	}
}
