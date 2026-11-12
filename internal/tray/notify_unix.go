//go:build (linux || freebsd || openbsd || netbsd) && !android

package tray

import "github.com/godbus/dbus/v5"

// Notify shows a desktop notification (freedesktop notification service).
func Notify(title, body string) error {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return err
	}
	defer conn.Close()
	obj := conn.Object("org.freedesktop.Notifications", "/org/freedesktop/Notifications")
	return obj.Call("org.freedesktop.Notifications.Notify", 0, "Herakles", uint32(0), "herakles", title, body,
		[]string{}, map[string]dbus.Variant{}, int32(-1)).Err
}
