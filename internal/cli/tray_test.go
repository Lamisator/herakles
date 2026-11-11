package cli

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lamisator/herakles/internal/config"
	"github.com/lamisator/herakles/internal/daemon"
)

// TestTrayMode runs 'herakles tray' where no tray host exists: it must keep
// running as daemon + GUI, share one task runner between them, hand a
// second start over to the running window and clean up on exit.
func TestTrayMode(t *testing.T) {
	e := newEnv(t)
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
	orig := openWindow
	openWindow = func(u string) error { mu.Lock(); opened = append(opened, u); mu.Unlock(); return nil }
	t.Cleanup(func() { openWindow = orig })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errb := &lockedBuffer{}
	done := make(chan error, 1)
	go func() {
		app := &App{out: &bytes.Buffer{}, err: errb}
		root := app.rootCommand()
		root.SetArgs([]string{"--config", e.cfg, "tray", "--open"})
		done <- root.ExecuteContext(ctx)
	}()

	urlFile := filepath.Join(cfg.StateDir, "gui.url")
	var url string
	for end := time.Now().Add(10 * time.Second); url == "" && time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if b, err := os.ReadFile(urlFile); err == nil {
			url = strings.TrimSpace(string(b))
		}
	}
	if url == "" {
		t.Fatal("tray never wrote its GUI URL")
	}
	mu.Lock()
	if len(opened) != 1 || opened[0] != url {
		t.Fatalf("--open should open %s, opened %v", url, opened)
	}
	mu.Unlock()

	// A backup started in the GUI runs on the daemon's runner.
	jar, _ := cookiejar.New(nil)
	hc := &http.Client{Jar: jar}
	resp, err := hc.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	base := strings.SplitN(url, "/?", 2)[0]
	req, _ := http.NewRequest("POST", base+"/api/jobs/docs/run", strings.NewReader(`{"type":"full"}`))
	req.Header.Set("X-Herakles", "1")
	if resp, err = hc.Do(req); err != nil || resp.StatusCode != 202 {
		t.Fatal("start backup from GUI:", resp.Status, err)
	}
	resp.Body.Close()
	dc, err := daemon.NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for end := time.Now().Add(30 * time.Second); !found && time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
		st, err := dc.Status()
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range st.Recent {
			if r.Target == "docs" && r.Trigger == "manual" && r.State == "done" {
				if r.Error != "" {
					t.Fatal(r.Error)
				}
				found = true
			}
		}
	}
	if !found {
		t.Fatal("the daemon never saw the backup started in the GUI")
	}

	// A second start opens the running instance's window.
	out := e.ok("tray")
	if !strings.Contains(out, "already running") {
		t.Fatal(out)
	}
	mu.Lock()
	if len(opened) != 2 || opened[1] != url {
		t.Fatalf("second start should open %s, opened %v", url, opened)
	}
	mu.Unlock()

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("tray did not stop")
	}
	if !strings.Contains(errb.String(), "no tray icon") || !strings.Contains(errb.String(), url) {
		t.Fatalf("expected a no-tray warning with the URL:\n%s", errb.String())
	}
	if _, err := os.Stat(urlFile); !os.IsNotExist(err) {
		t.Fatal("gui.url should be removed on exit")
	}
}

// lockedBuffer is a bytes.Buffer safe for the concurrent log writers.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func TestTrayAutostartCommand(t *testing.T) {
	e := newEnv(t)
	e.setup()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(e.dir, "xdg-config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(e.dir, "xdg-data"))
	entry := filepath.Join(e.dir, "xdg-config", "autostart", "herakles.desktop")
	if out := e.ok("tray", "autostart"); out != "tray autostart is off\n" {
		t.Fatal(out)
	}
	e.ok("tray", "autostart", "on")
	cfg, err := config.Load(e.cfg)
	if err != nil || !cfg.Tray.Autostart {
		t.Fatal("tray.autostart not saved", err)
	}
	if b, err := os.ReadFile(entry); err != nil || !strings.Contains(string(b), " --config "+e.cfg+" tray\n") {
		t.Fatal(string(b), err)
	}
	if out := e.ok("tray", "autostart"); out != "tray autostart is on\n" {
		t.Fatal(out)
	}
	e.ok("tray", "autostart", "off")
	if _, err := os.Stat(entry); !os.IsNotExist(err) {
		t.Fatal("autostart entry not removed")
	}
	if _, _, code := e.run("tray", "autostart", "maybe"); code == 0 {
		t.Fatal("invalid argument accepted")
	}
}
