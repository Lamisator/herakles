//go:build windows

package fsutil

import (
	"os"
	"syscall"
	"time"
	"unsafe"

	"github.com/lamisator/herakles/internal/repository"
	"golang.org/x/sys/windows"
)

func fillPlatform(path string, fi os.FileInfo, n *repository.Node) {
	if d, ok := fi.Sys().(*syscall.Win32FileAttributeData); ok {
		n.ChangeTime = time.Unix(0, d.CreationTime.Nanoseconds()).UTC()
	}
}

// IsRoot reports whether ownership can be restored (never on Windows).
func IsRoot() bool { return false }

// RestoreMetadata applies permissions (read-only bit) and timestamps.
func RestoreMetadata(path string, n *repository.Node, owner bool) error {
	if n.Type == repository.NodeSymlink {
		return nil
	}
	if n.Type != repository.NodeDir {
		_ = os.Chmod(path, n.Mode.Perm())
	}
	atime := n.AccessTime
	if atime.IsZero() {
		atime = n.ModTime
	}
	return os.Chtimes(path, atime, n.ModTime)
}

// CreateSpecial is a no-op on Windows.
func CreateSpecial(path string, n *repository.Node) error { return nil }

func openDevice(path string) (*os.File, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_SEQUENTIAL_SCAN, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(h), path), nil
}

func openDeviceWrite(path string) (*os.File, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(p, windows.GENERIC_WRITE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_ALWAYS, 0, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(h), path), nil
}

const ioctlDiskGetLengthInfo = 0x7405c

func deviceSize(f *os.File) (int64, error) {
	var length int64
	var ret uint32
	err := windows.DeviceIoControl(windows.Handle(f.Fd()), ioctlDiskGetLengthInfo, nil, 0, (*byte)(unsafe.Pointer(&length)), 8, &ret, nil)
	if err != nil {
		return 0, err
	}
	return length, nil
}

// DeviceOf returns 0 on Windows (one_file_system uses volume names instead).
func DeviceOf(fi os.FileInfo) uint64 { return 0 }
