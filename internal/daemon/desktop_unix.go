//go:build !windows

package daemon

import (
	"os"
	"os/user"
	"strconv"
)

func defaultDesktopDir() string { return "/run/herakles/desktop" }

// grantShare hands the share file to the user.
func grantShare(path string, u *user.User) error {
	uid, err := strconv.Atoi(u.Uid)
	if err != nil {
		return err
	}
	gid, _ := strconv.Atoi(u.Gid)
	return os.Chown(path, uid, gid)
}
