package cli

import (
	"bytes"
	"context"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lamisator/herakles/internal/config"
)

// TestDefaultCommand: without a command Herakles prints its help, except
// where the GUI is the default (Windows): there it opens the window.
func TestDefaultCommand(t *testing.T) {
	orig := guiByDefault
	t.Cleanup(func() { guiByDefault = orig })

	guiByDefault = false
	e := newEnv(t)
	if out := e.ok(); !strings.Contains(out, "Usage:") || !strings.Contains(out, "Available Commands:") {
		t.Fatalf("no command should print the help, got:\n%s", out)
	}
	if _, errs, code := e.run("bogus"); code == 0 || !strings.Contains(errs, "unknown command") {
		t.Fatalf("an unknown command must fail, got %d: %s", code, errs)
	}

	guiByDefault = true
	e.setup()
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path="+filepath.Join(e.dir, "no-bus"))
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	control := ln.Addr().String()
	ln.Close()
	cfg, err := config.Load(e.cfg)
	if err != nil {
		t.Fatal(err)
	}
	cfg.StateDir = filepath.Join(e.dir, "state")
	cfg.Daemon.ControlListen = control
	cfg.GUI.Listen = "127.0.0.1:0"
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var opened []string
	origOpen := openWindow
	openWindow = func(u string) error { mu.Lock(); opened = append(opened, u); mu.Unlock(); return nil }
	t.Cleanup(func() { openWindow = origOpen })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		app := &App{out: &bytes.Buffer{}, err: &lockedBuffer{}}
		root := app.rootCommand()
		root.SetArgs([]string{"--config", e.cfg})
		done <- root.ExecuteContext(ctx)
	}()
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		mu.Lock()
		n := len(opened)
		mu.Unlock()
		if n > 0 {
			break
		}
	}
	mu.Lock()
	if len(opened) != 1 || !strings.HasPrefix(opened[0], "http://127.0.0.1:") {
		t.Fatalf("no command should open the GUI window, opened %v", opened)
	}
	mu.Unlock()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("tray mode did not stop")
	}
}
