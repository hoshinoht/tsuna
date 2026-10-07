package testutil

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/hoshinoht/shiori/internal/ojson"
)

// SHA256Hex is the hex SHA-256 of s.
func SHA256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// FirstDiff describes where got and want first differ, with context.
func FirstDiff(got, want string) string {
	i := 0
	for i < len(got) && i < len(want) && got[i] == want[i] {
		i++
	}
	lo := i - 200
	if lo < 0 {
		lo = 0
	}
	hiG, hiW := i+200, i+200
	if hiG > len(got) {
		hiG = len(got)
	}
	if hiW > len(want) {
		hiW = len(want)
	}
	return fmt.Sprintf("at byte %d (got len %d, want len %d)\n got …%s…\nwant …%s…", i, len(got), len(want), got[lo:hiG], want[lo:hiW])
}

// Without returns object v without the named members.
func Without(v ojson.Value, keys ...string) ojson.Value {
	drop := map[string]bool{}
	for _, k := range keys {
		drop[k] = true
	}
	var out []ojson.Member
	for _, m := range v.Members() {
		if !drop[m.Key] {
			out = append(out, m)
		}
	}
	return ojson.ObjectValue(out)
}

// Replace returns object v with member key set to nv (in place).
func Replace(v ojson.Value, key string, nv ojson.Value) ojson.Value {
	out := append([]ojson.Member(nil), v.Members()...)
	for i := range out {
		if out[i].Key == key {
			out[i].Value = nv
		}
	}
	return ojson.ObjectValue(out)
}

// Has reports whether object v has member key.
func Has(v ojson.Value, key string) bool { _, ok := v.Get(key); return ok }
