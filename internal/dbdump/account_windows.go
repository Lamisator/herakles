package dbdump

import (
	"errors"
	"os"
	"os/exec"
)

type account struct {
	name, home string
	uid, gid   int
}

func lookupAccount(name string) (*account, error) {
	return nil, errors.New("run_as is not supported on Windows")
}

func setCredential(cmd *exec.Cmd, a *account) {}

func chownLike(p string, fi os.FileInfo) {}
