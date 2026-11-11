package blockdev

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

const ioctlDiskGetDriveGeometryEx = 0x000700A0 // FILE_ANY_ACCESS: works without administrator rights

// list returns the physical drives (\\.\PhysicalDriveN) and the volumes
// with a drive letter (\\.\C:).
func list() ([]Device, error) {
	var out []Device
	for i := 0; i < 32; i++ {
		p := fmt.Sprintf(`\\.\PhysicalDrive%d`, i)
		size, ok := diskSize(p)
		if !ok {
			continue
		}
		out = append(out, Device{Path: p, Name: fmt.Sprintf("PhysicalDrive%d", i), Type: "disk", Size: size})
	}
	mask, err := windows.GetLogicalDrives()
	if err != nil {
		return out, nil
	}
	for i := 0; i < 26; i++ {
		if mask&(1<<i) == 0 {
			continue
		}
		letter := string(rune('A' + i))
		root := letter + `:\`
		rootp, _ := windows.UTF16PtrFromString(root)
		t := windows.GetDriveType(rootp)
		if t != windows.DRIVE_FIXED && t != windows.DRIVE_REMOVABLE {
			continue // network, optical and RAM drives
		}
		d := Device{Path: `\\.\` + letter + ":", Name: letter + ":", Type: "volume", Mounts: []string{root}, Removable: t == windows.DRIVE_REMOVABLE}
		var label, fs [windows.MAX_PATH + 1]uint16
		var serial, maxLen, flags uint32
		if windows.GetVolumeInformation(rootp, &label[0], uint32(len(label)), &serial, &maxLen, &flags, &fs[0], uint32(len(fs))) == nil {
			d.Label, d.FSType = windows.UTF16ToString(label[:]), windows.UTF16ToString(fs[:])
			d.UUID = fmt.Sprintf("%04X-%04X", serial>>16, serial&0xffff)
			d.ReadOnly = flags&windows.FILE_READ_ONLY_VOLUME != 0
		}
		if size, ok := diskSize(d.Path); ok {
			d.Size = size
		} else {
			var free, total, totalFree uint64
			if windows.GetDiskFreeSpaceEx(rootp, &free, &total, &totalFree) == nil {
				d.Size = total
			}
		}
		if sys, err := windows.GetSystemDirectory(); err == nil && len(sys) > 0 && sys[0] == letter[0] {
			d.System = true
		}
		out = append(out, d)
	}
	return out, nil
}

func diskSize(path string) (uint64, bool) {
	p, _ := windows.UTF16PtrFromString(path)
	h, err := windows.CreateFile(p, 0, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return 0, false
	}
	defer windows.CloseHandle(h)
	var buf [256]byte
	var n uint32
	if err := windows.DeviceIoControl(h, ioctlDiskGetDriveGeometryEx, nil, 0, &buf[0], uint32(len(buf)), &n, nil); err != nil || n < 32 {
		return 0, false
	}
	// DISK_GEOMETRY (24 bytes) followed by DiskSize (LARGE_INTEGER).
	return *(*uint64)(unsafe.Pointer(&buf[24])), true
}
