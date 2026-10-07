package snapshot

import (
	"io/fs"
	"syscall"
)

type statKey struct {
	dev, ino     uint64
	size         int64
	mtime, ctime int64 // nanoseconds
}

func keyOf(info fs.FileInfo) (statKey, bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return statKey{}, false
	}
	return statKey{dev: uint64(st.Dev), ino: st.Ino, size: st.Size,
		mtime: st.Mtim.Sec*1e9 + st.Mtim.Nsec, ctime: st.Ctim.Sec*1e9 + st.Ctim.Nsec}, true
}
