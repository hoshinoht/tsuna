//go:build unix

package engine

import "syscall"

// processUmask is read once at package initialization, before any
// goroutine creates files: syscall.Umask can only be read by setting it,
// so it is set back immediately.
var processUmask = func() uint32 {
	old := syscall.Umask(0o022)
	syscall.Umask(old)
	return uint32(old)
}()
