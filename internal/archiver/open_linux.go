package archiver

import (
	"os"
	"syscall"
)

// openNoAtime opens a file without updating its access time when permitted.
func openNoAtime(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOATIME, 0)
	if err != nil {
		return os.Open(path)
	}
	return f, nil
}
