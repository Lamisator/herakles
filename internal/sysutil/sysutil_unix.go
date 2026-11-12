//go:build !windows

package sysutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
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

// ProcessAlive reports whether pid runs (an exited process that its parent
// has not collected yet, a zombie, does not).
func ProcessAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil || p.Signal(syscall.Signal(0)) != nil {
		return false
	}
	if b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat"); err == nil {
		// pid (comm) STATE ...: comm may contain spaces and parentheses.
		if i := strings.LastIndexByte(string(b), ')'); i >= 0 && i+2 < len(b) && b[i+2] == 'Z' {
			return false
		}
	}
	return true
}

// ProcessIsHerakles reports whether pid runs this program, so a recorded
// PID that the system gave to another process since does not count. When
// that cannot be checked, it is assumed.
func ProcessIsHerakles(pid int) bool {
	if !ProcessAlive(pid) {
		return false
	}
	exe, err1 := os.Readlink("/proc/" + strconv.Itoa(pid) + "/exe")
	self, err2 := os.Executable()
	if err1 != nil || err2 != nil {
		return true
	}
	return filepath.Base(strings.TrimSuffix(exe, " (deleted)")) == filepath.Base(self)
}

// ProgramData is Windows-only; empty elsewhere.
func ProgramData() string { return "" }

// Background lets cmd outlive this process (own session).
func Background(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }
