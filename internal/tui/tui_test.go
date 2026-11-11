package tui

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"

	"github.com/lamisator/herakles/internal/config"
	"github.com/lamisator/herakles/internal/engine"
	tu "github.com/lamisator/herakles/internal/testutil"
)

type harness struct {
	t   *testing.T
	ui  *UI
	sim tcell.SimulationScreen
	eng *engine.Engine
}

func start(t *testing.T) *harness {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	tu.MakeTree(t, src)
	cfg := config.New(filepath.Join(dir, "h.yaml"))
	cfg.StateDir = filepath.Join(dir, "state")
	cfg.Daemon.ControlListen = "127.0.0.1:1" // no daemon
	cfg.Repositories = []*config.Repository{{Name: "vault", Location: filepath.Join(dir, "repo"), Password: "pw"}}
	cfg.Jobs = []*config.Job{{Name: "documents", Repository: "vault", Paths: []string{src}, Schedules: []config.Schedule{{Cron: "@daily"}}}}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	eng := engine.New(cfg, nil)
	if _, err := eng.Init(context.Background(), cfg.Repositories[0], tu.Password); err != nil {
		t.Fatal(err)
	}
	cfg.Repositories[0].Password = tu.Password.Password
	ctx, cancel := context.WithCancel(context.Background())
	sim := tcell.NewSimulationScreen("UTF-8")
	if err := sim.Init(); err != nil {
		t.Fatal(err)
	}
	u := New(ctx, eng, "test")
	u.app.SetScreen(sim)
	sim.SetSize(140, 40)
	done := make(chan struct{})
	go func() { _ = u.app.Run(); close(done) }()
	t.Cleanup(func() { u.app.Stop(); cancel(); <-done })
	h := &harness{t: t, ui: u, sim: sim, eng: eng}
	h.waitFor("documents")
	return h
}

// text returns the screen contents. tcell hands out its internal cell
// buffer, so it is read on the UI goroutine, which is the one drawing.
func (h *harness) text() string {
	ch := make(chan string, 1)
	go h.ui.app.QueueUpdate(func() { ch <- h.contents() })
	select {
	case s := <-ch:
		return s
	case <-time.After(5 * time.Second):
		return "(UI not responding)"
	}
}

func (h *harness) contents() string {
	cells, w, _ := h.sim.GetContents()
	var b strings.Builder
	for i, c := range cells {
		if len(c.Runes) > 0 {
			b.WriteRune(c.Runes[0])
		} else {
			b.WriteByte(' ')
		}
		if (i+1)%w == 0 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func (h *harness) waitFor(s string) {
	h.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		h.ui.app.QueueUpdateDraw(func() {})
		if strings.Contains(h.text(), s) {
			return
		}
		time.Sleep(30 * time.Millisecond)
	}
	h.t.Fatalf("screen never showed %q:\n%s", s, h.text())
}

func (h *harness) key(k tcell.Key, r rune) {
	h.sim.InjectKey(k, r, tcell.ModNone)
	time.Sleep(20 * time.Millisecond)
}

func (h *harness) rune(r rune) { h.key(tcell.KeyRune, r) }

func TestTUINavigation(t *testing.T) {
	h := start(t)
	h.waitFor("Backup jobs")
	h.rune('2')
	h.waitFor("vault")
	h.waitFor("inline password")
	h.rune('3')
	h.waitFor("no snapshots")
	h.rune('5')
	h.waitFor(" Log ")
	h.rune('4')
	h.waitFor("repositories:")
	h.key(tcell.KeyEscape, 0)
	h.waitFor("Backup jobs")
}

func TestTUIRunBackupAndBrowse(t *testing.T) {
	h := start(t)
	h.rune('f') // full backup of the selected job
	h.waitFor("success")
	last := h.eng.History().Last("documents")
	if last == nil || last.Type != "full" || last.Status != "success" {
		t.Fatalf("history: %+v", last)
	}
	h.rune('3')
	h.waitFor("full")
	h.key(tcell.KeyEnter, 0) // browse snapshot
	h.waitFor("Browse")
	h.waitFor("└──") // first path component (tmp/ or C/) is listed
}

func TestTUIJobEditor(t *testing.T) {
	h := start(t)
	h.rune('n')
	h.waitFor("New job")
	// Type a name into the focused first field, then Esc closes the form.
	for _, r := range "photos" {
		h.rune(r)
	}
	h.waitFor("photos")
	h.key(tcell.KeyEscape, 0)
	h.waitFor("Backup jobs")
	if h.eng.Config().Job("photos") != nil {
		t.Fatal("cancelled form saved a job")
	}
	// Quit confirmation.
	h.rune('q')
	h.waitFor("Quit Herakles?")
}
