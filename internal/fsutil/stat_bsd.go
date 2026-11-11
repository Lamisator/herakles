//go:build darwin || freebsd || netbsd || openbsd

package fsutil

import (
	"errors"
	"os"
	"syscall"
	"time"
)

func statCTime(st *syscall.Stat_t) time.Time { return time.Unix(st.Ctimespec.Unix()).UTC() }

func deviceSize(f *os.File) (int64, error) { return 0, errors.New("unsupported") }
