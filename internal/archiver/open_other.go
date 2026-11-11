//go:build !linux

package archiver

import "os"

func openNoAtime(path string) (*os.File, error) { return os.Open(path) }
