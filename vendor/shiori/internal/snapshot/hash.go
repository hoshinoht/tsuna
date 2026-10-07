// Package snapshot reads a plan's complete artifact set (primary JSON,
// linked Markdown, linked specs and sidecars) as exact bytes and computes
// the frozen manifest hashes. It never writes.
package snapshot

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"

	"github.com/hoshinoht/shiori/internal/ojson"
)

// Hash algorithm version labels. Never reuse these for another algorithm.
const (
	PlanHashVersion  = "workplan-plan-v1"
	StateHashVersion = "workplan-state-v1"
)

// Entry is one manifest entry: exactly one of SHA256 or Missing.
type Entry struct {
	Path    string
	SHA256  string
	Missing bool
}

// EntryFor hashes content, or records a missing entry when data is nil.
func EntryFor(path string, data []byte, exists bool) Entry {
	if !exists {
		return Entry{Path: path, Missing: true}
	}
	return Entry{Path: path, SHA256: SHA256Hex(data)}
}

// SHA256Hex is the lowercase hex SHA-256 of data.
func SHA256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// SortEntries sorts by JavaScript UTF-16 code-unit order (stable).
func SortEntries(entries []Entry) []Entry {
	out := make([]Entry, len(entries))
	copy(out, entries)
	sort.SliceStable(out, func(i, j int) bool { return ojson.CompareUTF16(out[i].Path, out[j].Path) < 0 })
	return out
}

// ManifestJSON is the canonical compact manifest: entries sorted by UTF-16
// order, key order path then sha256|missing, JSON.stringify escaping.
func ManifestJSON(entries []Entry) []byte {
	sorted := SortEntries(entries)
	dst := make([]byte, 0, 16+len(sorted)*120)
	dst = append(dst, '[')
	for i, e := range sorted {
		if i > 0 {
			dst = append(dst, ',')
		}
		dst = append(dst, `{"path":`...)
		dst = ojson.AppendQuoted(dst, e.Path)
		if e.Missing {
			dst = append(dst, `,"missing":true}`...)
		} else {
			dst = append(dst, `,"sha256":`...)
			dst = ojson.AppendQuoted(dst, e.SHA256)
			dst = append(dst, '}')
		}
	}
	return append(dst, ']')
}

// ManifestHash is SHA256(version + "\n" + ManifestJSON(entries) + "\n").
func ManifestHash(version string, entries []Entry) string {
	h := sha256.New()
	h.Write([]byte(version))
	h.Write([]byte{'\n'})
	h.Write(ManifestJSON(entries))
	h.Write([]byte{'\n'})
	return hex.EncodeToString(h.Sum(nil))
}

// EntriesValue renders sorted entries as an ordered JSON array.
func EntriesValue(entries []Entry) ojson.Value {
	sorted := SortEntries(entries)
	out := make([]ojson.Value, len(sorted))
	for i, e := range sorted {
		b := ojson.NewObject(2).Set("path", ojson.StringValue(e.Path))
		if e.Missing {
			b.Set("missing", ojson.BoolValue(true))
		} else {
			b.Set("sha256", ojson.StringValue(e.SHA256))
		}
		out[i] = b.Value()
	}
	return ojson.ArrayValue(out)
}
