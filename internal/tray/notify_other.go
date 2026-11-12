//go:build !((linux || freebsd || openbsd || netbsd) && !android) && !windows

package tray

// Notify is not available here; the tray menu and the window still show
// what Notify would.
func Notify(title, body string) error { return ErrUnsupported }
