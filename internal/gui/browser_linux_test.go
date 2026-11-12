package gui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A failing xdg-open (or a browser stub that exits) must not stop the
// search: the next browser gets the address.
func TestOpenLinuxBrowserFallsThrough(t *testing.T) {
	bin := t.TempDir()
	out := filepath.Join(t.TempDir(), "opened")
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write("xdg-open", "exit 3")
	write("chromium-browser", `echo "requires the chromium snap" >&2; exit 1`)
	write("firefox", `echo "$1" > `+out)
	t.Setenv("PATH", bin)
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path="+filepath.Join(bin, "no-bus"))
	if err := openLinuxBrowser("http://127.0.0.1:1/?token=x"); err != nil {
		t.Fatal(err)
	}
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if b, err := os.ReadFile(out); err == nil && strings.TrimSpace(string(b)) == "http://127.0.0.1:1/?token=x" {
			return
		}
	}
	t.Fatal("firefox was not opened")
}

func TestOpenLinuxBrowserNone(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path=/nonexistent")
	if err := openLinuxBrowser("http://x/"); err == nil {
		t.Fatal("no browser should be an error")
	}
}
