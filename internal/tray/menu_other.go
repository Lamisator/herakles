//go:build !windows

package tray

// EnsureMenuEntry is for Windows; Linux packages and 'herakles tray
// autostart on' add the applications menu entry.
func EnsureMenuEntry(string) (string, error) { return "", nil }
