package gui

import (
	"context"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/lamisator/herakles/internal/config"
	"github.com/lamisator/herakles/internal/daemon"
	"github.com/lamisator/herakles/internal/engine"
)

// The system service shares its GUI through a per-user file; the tray reads
// it, shows the daemon status and opens the window with a one-time ticket.
func TestServiceShare(t *testing.T) {
	dir := t.TempDir()
	cfg := config.New(filepath.Join(dir, "h.yaml"))
	cfg.StateDir = filepath.Join(dir, "state")
	cfg.Daemon.ControlListen = "127.0.0.1:0"
	cfg.GUI.Users = []string{"alice"}
	eng := engine.New(cfg, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d, err := daemon.New(ctx, eng, daemon.Options{Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	defer func() { cancel(); <-done }()
	for i := 0; ; i++ {
		if _, err := os.Stat(daemon.TokenPath(cfg)); err == nil {
			break
		}
		if i > 200 {
			t.Fatal("daemon did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	inst, err := Start(ctx, eng, Options{Listen: "127.0.0.1:0", Version: "test", Runner: d.Runner, Logs: d.Logs, Service: true})
	if err != nil {
		t.Fatal(err)
	}
	defer inst.Shutdown()

	me, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	old := daemon.DesktopDir
	daemon.DesktopDir = filepath.Join(dir, "desktop")
	defer func() { daemon.DesktopDir = old }()
	if err := daemon.WriteDesktopShare(me, daemon.DesktopShare{GUI: inst.Base, Token: inst.Token}); err != nil {
		t.Fatal(err)
	}
	// On Windows an ACL limits the file to the user (no file modes).
	if fi, err := os.Stat(daemon.DesktopPath(me.Uid)); err != nil || (runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600) {
		t.Fatalf("share file: %v %v", fi, err)
	}
	sh, err := daemon.SharedDesktop()
	if os.Getuid() == 0 {
		if err == nil {
			t.Fatal("root must not use a share")
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if st, err := sh.Status(); err != nil || st.Version != "test" {
		t.Fatalf("status: %+v %v", st, err)
	}
	u, err := sh.OpenURL()
	if err != nil || strings.Contains(u, inst.Token) {
		t.Fatalf("open URL %q: %v", u, err)
	}
	cl := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := cl.Get(u)
	if err != nil || resp.StatusCode != http.StatusFound || len(resp.Cookies()) == 0 {
		t.Fatalf("ticket login: %v %v", resp, err)
	}
	// A ticket works once.
	resp, _ = cl.Get(u)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("reused ticket: %d", resp.StatusCode)
	}
	// The window knows it belongs to the service.
	c := &client{t: t, url: inst.Base, tok: inst.Token}
	var info struct {
		Service *struct{ Users []string }
	}
	c.json("GET", "/api/info", nil, &info)
	if info.Service == nil || len(info.Service.Users) != 1 || info.Service.Users[0] != "alice" {
		t.Fatalf("info: %+v", info.Service)
	}
	daemon.RemoveDesktopShares(nil)
	if _, err := daemon.SharedDesktop(); err == nil {
		t.Fatal("share still found after removal")
	}
}
