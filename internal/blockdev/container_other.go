//go:build !linux

package blockdev

// Container returns the kind of container Herakles runs in; only detected
// on Linux.
func Container() string { return "" }
