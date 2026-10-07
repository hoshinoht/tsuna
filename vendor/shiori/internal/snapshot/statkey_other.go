//go:build !linux && !darwin

package snapshot

import "io/fs"

type statKey struct{}

// keyOf: no trusted stat identity on this platform, so no stat hits.
func keyOf(fs.FileInfo) (statKey, bool) { return statKey{}, false }
