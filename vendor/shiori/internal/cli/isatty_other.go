//go:build !darwin && !linux

package cli

// Unsupported write platforms never prompt: --yes is required.
func isatty(uintptr) bool { return false }
