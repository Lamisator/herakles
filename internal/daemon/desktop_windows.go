package daemon

import (
	"os/user"
	"path/filepath"

	"github.com/lamisator/herakles/internal/sysutil"
)

func defaultDesktopDir() string { return filepath.Join(sysutil.ProgramData(), "Herakles", "desktop") }

// grantShare lets only the user (and administrators) read the share file;
// u.Uid is the user's SID on Windows.
func grantShare(path string, u *user.User) error { return sysutil.SecureFile(path, u.Uid) }
