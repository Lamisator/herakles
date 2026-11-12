//go:build !windows

package dbdump

import (
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"syscall"
)

type account struct {
	name, home string
	uid, gid   int
}

func lookupAccount(name string) (*account, error) {
	u, err := user.Lookup(name)
	if err != nil {
		return nil, err
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	return &account{name: u.Username, home: u.HomeDir, uid: uid, gid: gid}, nil
}

// setCredential runs the command as the account (Herakles runs as root).
func setCredential(cmd *exec.Cmd, a *account) {
	if os.Geteuid() == a.uid {
		return
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uint32(a.uid), Gid: uint32(a.gid)}}
}

// chownLike gives p the owner of fi (a restored SQLite file keeps the
// owner of the one it replaces).
func chownLike(p string, fi os.FileInfo) {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		_ = os.Chown(p, int(st.Uid), int(st.Gid))
	}
}
