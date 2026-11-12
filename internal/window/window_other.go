//go:build !linux && !windows

package window

// Open shows url in a native window and returns once it is up.
func Open(url string) error { return ErrUnavailable }

// Show shows url in a native window and returns when it is closed.
func Show(url string) error { return ErrUnavailable }
