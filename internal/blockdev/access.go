package blockdev

import (
	"errors"
	"io/fs"
	"os"
	"syscall"
)

// unavailable says why a device cannot be read here, or "" if it can (or
// only needs root).
func unavailable(path, container string) string {
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		if container != "" {
			return "not passed into the container (" + container + ")"
		}
		return "no device node " + path
	}
	f, err := os.Open(path)
	if err == nil {
		f.Close()
		return ""
	}
	if errors.Is(err, syscall.EPERM) {
		if container != "" {
			return "access blocked by the container (" + container + ")"
		}
		return "access blocked (device rules)"
	}
	return ""
}
