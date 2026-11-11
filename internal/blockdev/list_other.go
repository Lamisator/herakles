//go:build !linux && !windows

package blockdev

func list() ([]Device, error) { return nil, ErrUnsupported }
