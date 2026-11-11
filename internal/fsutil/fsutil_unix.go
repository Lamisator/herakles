//go:build !windows

package fsutil

import (
	"errors"
	"os"
	"syscall"

	"github.com/lamisator/herakles/internal/repository"
	"golang.org/x/sys/unix"
)

func fillPlatform(path string, fi os.FileInfo, n *repository.Node) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return
	}
	n.UID, n.GID = st.Uid, st.Gid
	n.User, n.Group = lookupUser(st.Uid), lookupGroup(st.Gid)
	n.Inode = uint64(st.Ino)
	n.DeviceID = uint64(st.Dev) //nolint:unconvert // differs per platform
	n.Links = uint64(st.Nlink)  //nolint:unconvert
	n.Rdev = uint64(st.Rdev)    //nolint:unconvert
	n.ChangeTime = statCTime(st)
	n.Xattrs = readXattrs(path)
}

func readXattrs(path string) map[string][]byte {
	sz, err := unix.Llistxattr(path, nil)
	if err != nil || sz <= 0 {
		return nil
	}
	buf := make([]byte, sz)
	sz, err = unix.Llistxattr(path, buf)
	if err != nil {
		return nil
	}
	out := map[string][]byte{}
	start := 0
	for i := 0; i < sz; i++ {
		if buf[i] != 0 {
			continue
		}
		name := string(buf[start:i])
		start = i + 1
		if name == "" {
			continue
		}
		vsz, err := unix.Lgetxattr(path, name, nil)
		if err != nil || vsz < 0 {
			continue
		}
		val := make([]byte, vsz)
		vsz, err = unix.Lgetxattr(path, name, val)
		if err != nil {
			continue
		}
		out[name] = val[:vsz]
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// IsRoot reports whether the process can restore ownership.
func IsRoot() bool { return os.Geteuid() == 0 }

// RestoreMetadata applies ownership, permissions, xattrs and timestamps.
func RestoreMetadata(path string, n *repository.Node, owner bool) error {
	var errs []error
	if owner && IsRoot() {
		if err := os.Lchown(path, int(n.UID), int(n.GID)); err != nil {
			errs = append(errs, err)
		}
	}
	if n.Type != repository.NodeSymlink {
		if err := os.Chmod(path, n.Mode&(os.ModePerm|os.ModeSetuid|os.ModeSetgid|os.ModeSticky)); err != nil {
			errs = append(errs, err)
		}
	}
	for k, v := range n.Xattrs {
		_ = unix.Lsetxattr(path, k, v, 0) // best effort (namespaces may be restricted)
	}
	atime := n.AccessTime
	if atime.IsZero() {
		atime = n.ModTime
	}
	ts := []unix.Timespec{unix.NsecToTimespec(atime.UnixNano()), unix.NsecToTimespec(n.ModTime.UnixNano())}
	if err := unix.UtimesNanoAt(unix.AT_FDCWD, path, ts, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// CreateSpecial creates fifos and device nodes.
func CreateSpecial(path string, n *repository.Node) error {
	mode := uint32(n.Mode.Perm())
	switch n.Type {
	case repository.NodeFIFO:
		mode |= unix.S_IFIFO
	case repository.NodeCharDev:
		mode |= unix.S_IFCHR
	case repository.NodeBlkDev:
		mode |= unix.S_IFBLK
	default:
		return nil // sockets are not restored
	}
	return unix.Mknod(path, mode, int(n.Rdev))
}

func openDevice(path string) (*os.File, error) { return os.Open(path) }

func openDeviceWrite(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE, 0o600)
}

// DeviceOf returns the device ID of a file (used for one_file_system).
func DeviceOf(fi os.FileInfo) uint64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Dev) //nolint:unconvert
	}
	return 0
}
