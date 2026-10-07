package snapshot

import (
	"encoding/json"
	"sort"
	"testing"
	"unicode/utf16"
	"unicode/utf8"
)

// FuzzManifestJSON: the canonical manifest is valid JSON, entries come
// back in UTF-16 code-unit order with the frozen key order, and the hash
// is independent of input order.
func FuzzManifestJSON(f *testing.F) {
	f.Add("docs/𝒜.md", "docs/Ａ.md", "a\"b\\c <>&.md", true)
	f.Add(".opencode/workplan/a.json", "docs/\x01.md", "docs/é.md", false)
	f.Fuzz(func(t *testing.T, a, b, c string, missing bool) {
		for _, s := range []string{a, b, c} {
			if !utf8.ValidString(s) {
				return
			}
		}
		if a == b || b == c || a == c {
			return // manifests never contain duplicate paths (Load dedups)
		}
		entries := []Entry{
			{Path: a, SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
			{Path: b, Missing: missing, SHA256: map[bool]string{false: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}[missing]},
			{Path: c, Missing: true},
		}
		out := ManifestJSON(entries)
		var decoded []map[string]any
		if err := json.Unmarshal(out, &decoded); err != nil {
			t.Fatalf("invalid JSON %q: %v", out, err)
		}
		paths := make([]string, len(decoded))
		for i, d := range decoded {
			paths[i] = d["path"].(string)
		}
		units := func(s string) []uint16 { return utf16.Encode([]rune(s)) }
		if !sort.SliceIsSorted(paths, func(i, j int) bool {
			x, y := units(paths[i]), units(paths[j])
			for k := 0; k < len(x) && k < len(y); k++ {
				if x[k] != y[k] {
					return x[k] < y[k]
				}
			}
			return len(x) < len(y)
		}) {
			t.Fatalf("not UTF-16 sorted: %q", paths)
		}
		rev := []Entry{entries[2], entries[1], entries[0]}
		if ManifestHash(StateHashVersion, rev) != ManifestHash(StateHashVersion, entries) {
			t.Fatal("hash depends on input order")
		}
	})
}
