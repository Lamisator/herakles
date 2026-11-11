package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

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

// onUI runs fn on the UI goroutine and waits for it.
func (h *harness) onUI(fn func()) {
	done := make(chan struct{})
	h.ui.app.QueueUpdateDraw(func() { fn(); close(done) })
	<-done
}

// form returns the form of the current wizard step.
func (h *harness) wizForm() *tview.Form {
	var f *tview.Form
	h.onUI(func() {
		item := h.ui.wiz.flex.GetItem(1)
		if fl, ok := item.(*tview.Flex); ok {
			item = fl.GetItem(0)
		}
		f, _ = item.(*tview.Form)
	})
	return f
}

func (h *harness) wizClick(label string) {
	h.t.Helper()
	f := h.wizForm()
	h.onUI(func() { h.ui.app.SetFocus(f.GetButton(f.GetButtonIndex(label))) })
	h.key(tcell.KeyEnter, 0)
}

func (h *harness) wizType(label, text string) {
	h.t.Helper()
	f := h.wizForm()
	field := f.GetFormItemByLabel(label).(*tview.InputField)
	h.onUI(func() { h.ui.app.SetFocus(field) })
	for _, r := range text {
		h.rune(r)
	}
	// Key events are queued separately from UI updates: wait until all
	// arrived before moving the focus.
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		var got string
		h.onUI(func() { got = field.GetText() })
		if got == text {
			return
		}
	}
	h.t.Fatalf("%s: typed text did not arrive", label)
}

func TestTUIWizard(t *testing.T) {
	h := start(t)
	h.rune('w')
	h.waitFor("What do you want to back up?")
	h.key(tcell.KeyEnter, 0) // Files & folders
	h.waitFor("Which files and folders?")
	src := t.TempDir()
	os.WriteFile(filepath.Join(src, "note.txt"), []byte("hello"), 0o644)
	h.wizType("Folders (; separated)", src)
	h.wizClick("Next")
	h.waitFor("Where should the backups go?")
	wf := h.wizForm()
	h.onUI(func() {
		dd := wf.GetFormItemByLabel("Repository").(*tview.DropDown)
		dd.SetCurrentOption(dd.GetOptionCount() - 1) // New location…
	})
	h.waitFor("Folder for the backups")
	repo := filepath.Join(t.TempDir(), "repo")
	h.wizType("Folder for the backups", repo)
	h.wizClick("Next")
	h.waitFor("save the password first") // refused until confirmed
	h.key(tcell.KeyEnter, 0)             // close the message
	f := h.wizForm()
	h.onUI(func() { h.ui.app.SetFocus(f.GetFormItemByLabel("I have saved the password")) })
	h.key(tcell.KeyEnter, 0)
	pw := h.ui.wiz.plan.Password
	h.wizClick("Next")
	h.waitFor("When should it run?")
	h.wizClick("Next")
	h.waitFor("How long should backups be kept?")
	h.wizClick("Next")
	h.waitFor("Check and create")
	h.wizClick("Create backup")
	h.waitFor("is ready")
	if !strings.Contains(h.text(), pw[:6]) {
		t.Fatal("the generated password must be shown at the end")
	}
	cfg := h.eng.Config()
	j := cfg.Job("files")
	rc := cfg.Repo("local")
	if j == nil || rc == nil || j.Repository != "local" || rc.Location != repo || j.Retention.KeepMonthly != 12 || len(j.Schedules) != 1 {
		t.Fatalf("job %+v repo %+v", j, rc)
	}
	if b, err := os.ReadFile(rc.PasswordFile); err != nil || strings.TrimSpace(string(b)) != pw {
		t.Fatal("password file:", err)
	}
	// The first backup runs right away.
	for i := 0; i < 200; i++ {
		if r := h.eng.History().Last("files"); r != nil {
			if r.Status != "success" {
				t.Fatalf("first backup: %+v", r)
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("first backup did not run")
}
