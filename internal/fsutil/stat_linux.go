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

// pseudoFS are file systems without data worth keeping: kernel interfaces
// (/proc, /sys, cgroups, …) and memory-backed ones (/dev, /run and /tmp
// as tmpfs).
var pseudoFS = map[int64]bool{
	unix.PROC_SUPER_MAGIC: true, unix.SYSFS_MAGIC: true, unix.CGROUP_SUPER_MAGIC: true, unix.CGROUP2_SUPER_MAGIC: true,
	unix.DEVPTS_SUPER_MAGIC: true, unix.TMPFS_MAGIC: true, unix.RAMFS_MAGIC: true, unix.DEBUGFS_MAGIC: true,
	unix.TRACEFS_MAGIC: true, unix.SECURITYFS_MAGIC: true, unix.PSTOREFS_MAGIC: true, unix.BPF_FS_MAGIC: true,
	0x19800202: true, unix.HUGETLBFS_MAGIC: true, 0x62656570: true, // mqueue, configfs
	unix.BINFMTFS_MAGIC:     true,
	unix.AUTOFS_SUPER_MAGIC: true, unix.EFIVARFS_MAGIC: true, unix.NSFS_MAGIC: true, 0x65735543: true, // fusectl
}

// PseudoFS reports whether path is on such a file system.
func PseudoFS(path string) bool {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return false
	}
	return pseudoFS[int64(st.Type)] //nolint:unconvert // int32 on some platforms
}
