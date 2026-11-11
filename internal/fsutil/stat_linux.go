package fsutil

import (
	"os"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

func statCTime(st *syscall.Stat_t) time.Time {
	return time.Unix(int64(st.Ctim.Sec), int64(st.Ctim.Nsec)).UTC() //nolint:unconvert // int32 on 32-bit
}

func deviceSize(f *os.File) (int64, error) {
	var size uint64
	_, _, e := unix.Syscall(unix.SYS_IOCTL, f.Fd(), unix.BLKGETSIZE64, uintptr(unsafe.Pointer(&size)))
	if e != 0 {
		return 0, e
	}
	return int64(size), nil
}
