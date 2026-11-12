//go:build (linux || freebsd || openbsd || netbsd) && !android

package tray

import (
	"context"
	"fmt"
	"time"

	"github.com/godbus/dbus/v5"
)

func iconBytes(s State) []byte { return PNG(64, s) }

// available checks for a D-Bus session bus, without which no tray can work.
func available() error {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return fmt.Errorf("no D-Bus session bus (%w); a desktop session is required for the tray", err)
	}
	return conn.Close()
}

// hostReady waits up to timeout for a StatusNotifierWatcher (the desktop's
// tray host). At login the panel may start after Herakles.
func hostReady(ctx context.Context, timeout time.Duration) bool {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return false
	}
	defer conn.Close()
	for end := time.Now().Add(timeout); ; time.Sleep(250 * time.Millisecond) {
		var has bool
		if conn.BusObject().Call("org.freedesktop.DBus.NameHasOwner", 0, "org.kde.StatusNotifierWatcher").Store(&has) == nil && has {
			return true
		}
		if time.Now().After(end) || ctx.Err() != nil {
			return false
		}
	}
}

const noHostWarning = "the desktop has no system tray (StatusNotifierItem host) yet; the icon appears once one starts. On GNOME install the \"AppIndicator and KStatusNotifierItem Support\" extension"

// promoteIcon is Windows only.
func promoteIcon(context.Context) {}
