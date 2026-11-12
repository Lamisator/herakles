package backend

import "golang.org/x/sys/windows"

func diskSpace(p string) (Space, error) {
	name, err := windows.UTF16PtrFromString(p)
	if err != nil {
		return Space{}, err
	}
	var avail, total, free uint64
	if err := windows.GetDiskFreeSpaceEx(name, &avail, &total, &free); err != nil {
		return Space{}, err
	}
	return Space{Total: total, Free: avail}, nil
}
