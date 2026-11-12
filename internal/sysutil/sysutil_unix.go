//go:build !windows

package sysutil

import (
	"os"
	"strconv"
	"syscall"
)

// SecureDir does nothing: Unix directories get their mode on creation.
func SecureDir(string) error { return nil }

// SecureFile limits path to its owner (the user is set with chown).
func SecureFile(path, _ string) error { return os.Chmod(path, 0o600) }

// Elevated reports whether the process runs as root.
func Elevated() bool { return os.Geteuid() == 0 }

// SelfID is the current user's ID (uid); root has none.
func SelfID() (string, bool) {
	uid := os.Getuid()
	if uid <= 0 {
		return "", false
	}
	return strconv.Itoa(uid), true
}

// ConsoleUser is the user logged in at the screen; unknown on Unix.
func ConsoleUser() (string, error) { return "", ErrUnsupported }

// ProcessAlive reports whether pid runs.
func ProcessAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	return err == nil && p.Signal(syscall.Signal(0)) == nil
}

// ProgramData is Windows-only; empty elsewhere.
func ProgramData() string { return "" }
