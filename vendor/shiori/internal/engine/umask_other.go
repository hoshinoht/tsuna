//go:build !unix

package engine

// processUmask is the conventional default where the platform has none.
var processUmask uint32 = 0o022
