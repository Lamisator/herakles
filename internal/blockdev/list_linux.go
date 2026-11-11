package blockdev

func list() ([]Device, error) { return listLinux(defaultRoots) }
