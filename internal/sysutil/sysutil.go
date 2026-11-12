// Package sysutil wraps the operating system calls the Windows setup and
// the shared desktop need: file permissions, the logged-in user, process
// checks and per-user registry values. Outside Windows most of it is a
// no-op or reports ErrUnsupported.
package sysutil

import "errors"

// ErrUnsupported is returned where the platform has no equivalent.
var ErrUnsupported = errors.New("not supported on this platform")

// SystemSID is the Windows LocalSystem account the service runs as.
const SystemSID = "S-1-5-18"
