// Package tui implements the curses-style terminal interface.
package tui

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"strings"
	"sync"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/lamisator/herakles/internal/config"
	"github.com/lamisator/herakles/internal/crypto"
	"github.com/lamisator/herakles/internal/daemon"
	"github.com/lamisator/herakles/internal/engine"
	"github.com/lamisator/herakles/internal/repository"
	"github.com/lamisator/herakles/internal/retention"
	"github.com/lamisator/herakles/internal/tray"
	"github.com/lamisator/herakles/internal/ui"
)

// UI is the terminal application.
type UI struct {
	app     *tview.Application
	eng     *engine.Engine
	runner  *daemon.Runner
	logs    *daemon.LogBuffer
	version string
	ctx     context.Context

	pages  *tview.Pages
	header *tview.TextView
	footer *tview.TextView

	jobs     *tview.Table
	repos    *tview.Table
	runsView *tview.TextView
	logView  *tview.TextView

	snapRepo  *tview.DropDown
	snapTable *tview.Table
	snapList  []*repository.Snapshot
	tree      *tview.TreeView
	yaml      *tview.TextArea

	current string
	wiz     *wiz // the open backup wizard (tests)
	mu      sync.Mutex
	open    map[string]*repository.Repository
}

var tabs = []struct{ key, name, title string }{
	{"1", "dashboard", "Dashboard"},
	{"2", "repos", "Repositories"},
	{"3", "snapshots", "Snapshots"},
	{"4", "yaml", "Config"},
	{"5", "logs", "Logs"},
}

// Run starts the TUI on the terminal.
func Run(ctx context.Context, eng *engine.Engine, version string, wizard bool) error {
	u := New(ctx, eng, version)
	if wizard {
		u.startWizard()
	}
	return u.app.Run()
}

// New builds the UI (exposed for tests, which set a simulation screen).
func New(ctx context.Context, eng *engine.Engine, version string) *UI {
	u := &UI{eng: eng, version: version, ctx: ctx, open: map[string]*repository.Repository{}, logs: daemon.NewLogBuffer(500)}
	eng.Log = slog.New(daemon.NewTeeHandler(slog.NewTextHandler(io.Discard, nil), u.logs))
	eng.Prompt = u.promptPassword
	u.runner = daemon.NewRunner(ctx, eng, 2)
	u.app = tview.NewApplication()
	u.build()
	go u.ticker()
	return u
}

// App returns the tview application (tests).
func (u *UI) App() *tview.Application { return u.app }

func (u *UI) build() {
	u.header = tview.NewTextView().SetDynamicColors(true).SetRegions(true)
	u.footer = tview.NewTextView().SetDynamicColors(true)
	u.pages = tview.NewPages()

	u.pages.AddPage("dashboard", u.buildDashboard(), true, false)
	u.pages.AddPage("repos", u.buildRepos(), true, false)
	u.pages.AddPage("snapshots", u.buildSnapshots(), true, false)
	u.pages.AddPage("yaml", u.buildYAML(), true, false)
	u.pages.AddPage("logs", u.buildLogs(), true, false)

	root := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(u.header, 1, 0, false).
		AddItem(u.pages, 0, 1, true).
		AddItem(u.footer, 1, 0, false)
	u.app.SetRoot(root, true)
	u.app.SetInputCapture(u.globalKeys)
	u.show("dashboard")
}

func (u *UI) globalKeys(ev *tcell.EventKey) *tcell.EventKey {
	// Do not steal keys from text inputs and modal dialogs.
	if u.modalOpen() || u.inTextInput() {
		if ev.Key() == tcell.KeyCtrlC {
			u.app.Stop()
			return nil
		}
		return ev
	}
	switch ev.Key() {
	case tcell.KeyF1, tcell.KeyF2, tcell.KeyF3, tcell.KeyF4, tcell.KeyF5:
		u.show(tabs[int(ev.Key()-tcell.KeyF1)].name)
		return nil
	case tcell.KeyCtrlC:
		u.app.Stop()
		return nil
	case tcell.KeyRune:
		r := ev.Rune()
		for _, t := range tabs {
			if string(r) == t.key {
				u.show(t.name)
				return nil
			}
		}
		if r == 'q' {
			u.confirm("Quit Herakles?", func() { u.app.Stop() })
			return nil
		}
	}
	return ev
}

func (u *UI) modalOpen() bool {
	name, _ := u.pages.GetFrontPage()
	return strings.HasPrefix(name, "modal")
}

func (u *UI) inTextInput() bool {
	switch u.app.GetFocus().(type) {
	case *tview.InputField, *tview.TextArea:
		return true
	}
	return false
}

func (u *UI) show(name string) {
	u.current = name
	u.pages.SwitchToPage(name)
	var parts []string
	for _, t := range tabs {
		if t.name == name {
			parts = append(parts, fmt.Sprintf("[black:aqua] %s %s [-:-]", t.key, t.title))
		} else {
			parts = append(parts, fmt.Sprintf(" [aqua]%s[-] %s ", t.key, t.title))
		}
	}
	u.header.SetText("[::b]HERAKLES[::-] " + strings.Join(parts, ""))
	hints := map[string]string{
		"dashboard": "[yellow]w[-] backup wizard  [yellow]Enter/r[-] run  [yellow]f[-] full  [yellow]i[-] incremental  [yellow]e[-] edit  [yellow]n[-] new job  [yellow]Del[-] delete  [yellow]c[-] cancel  [yellow]q[-] quit",
		"repos":     "[yellow]n[-] new  [yellow]e[-] edit  [yellow]I[-] init  [yellow]k[-] check  [yellow]p[-] prune  [yellow]s[-] stats  [yellow]Del[-] delete  [yellow]q[-] quit",
		"snapshots": "[yellow]Tab[-] focus  [yellow]Enter[-] browse  [yellow]r[-] restore  [yellow]d[-] forget snapshot  [yellow]R[-] reload  [yellow]Esc[-] back",
		"yaml":      "[yellow]Ctrl-S[-] validate & save  [yellow]Ctrl-R[-] revert  [yellow]Esc[-] leave editor",
		"logs":      "[yellow]q[-] quit",
	}
	u.footer.SetText(hints[name])
	switch name {
	case "dashboard":
		u.refreshJobs()
		u.app.SetFocus(u.jobs)
	case "repos":
		u.refreshRepos()
		u.app.SetFocus(u.repos)
	case "snapshots":
		u.refreshSnapRepos()
		u.app.SetFocus(u.snapTable)
	case "yaml":
		u.loadYAML()
	case "logs":
		u.refreshLogs()
	}
}

func (u *UI) ticker() {
	t := time.NewTicker(500 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-u.ctx.Done():
			return
		case <-t.C:
			u.app.QueueUpdateDraw(func() {
				switch u.current {
				case "dashboard":
					u.refreshRuns()
					u.refreshJobStatus()
				case "logs":
					u.refreshLogs()
				}
			})
		}
	}
}

// ---------- helpers: modals ----------

func (u *UI) modal(name string, p tview.Primitive, w, h int) {
	grid := tview.NewGrid().SetColumns(0, w, 0).SetRows(0, h, 0).AddItem(p, 1, 1, 1, 1, 0, 0, true)
	u.pages.AddPage("modal-"+name, grid, true, true)
	u.app.SetFocus(p)
}

func (u *UI) closeModal(name string) {
	u.pages.RemovePage("modal-" + name)
	// Return to a dialog underneath (e.g. a message over the wizard).
	if front, p := u.pages.GetFrontPage(); strings.HasPrefix(front, "modal") {
		u.app.SetFocus(p)
		return
	}
	u.show(u.current)
}

func (u *UI) message(title, text string) {
	m := tview.NewModal().SetText(text).AddButtons([]string{"OK"}).SetDoneFunc(func(int, string) { u.closeModal("msg") })
	m.SetTitle(title)
	u.pages.AddPage("modal-msg", m, true, true)
	u.app.SetFocus(m)
}

func (u *UI) errorf(format string, a ...any) {
	u.message("Error", "[red]"+fmt.Sprintf(format, a...))
}

func (u *UI) confirm(text string, ok func()) {
	m := tview.NewModal().SetText(text).AddButtons([]string{"Yes", "No"}).SetDoneFunc(func(i int, l string) {
		u.closeModal("confirm")
		if l == "Yes" {
			ok()
		}
	})
	u.pages.AddPage("modal-confirm", m, true, true)
	u.app.SetFocus(m)
}

// background runs fn off the UI goroutine and reports errors in a modal.
func (u *UI) background(title string, fn func() (string, error)) {
	go func() {
		msg, err := fn()
		u.app.QueueUpdateDraw(func() {
			if err != nil {
				u.errorf("%s failed:\n%v", title, err)
			} else if msg != "" {
				u.message(title, msg)
			}
		})
	}()
}

// promptPassword is called from background goroutines when a repository has
// no configured password source.
func (u *UI) promptPassword(repo string) (crypto.Credentials, error) {
	type answer struct {
		pw string
		ok bool
	}
	ch := make(chan answer, 1)
	u.app.QueueUpdateDraw(func() {
		form := tview.NewForm()
		form.AddPasswordField("Password", "", 40, '*', nil)
		done := func(ok bool) {
			pw := form.GetFormItem(0).(*tview.InputField).GetText()
			u.closeModal("password")
			ch <- answer{pw, ok}
		}
		form.AddButton("Unlock", func() { done(true) })
		form.AddButton("Cancel", func() { done(false) })
		form.SetCancelFunc(func() { done(false) })
		form.SetBorder(true).SetTitle(" Repository " + repo + " ")
		u.modal("password", form, 60, 7)
	})
	a := <-ch
	if !a.ok || a.pw == "" {
		return crypto.Credentials{}, fmt.Errorf("no password entered")
	}
	return crypto.Credentials{Password: a.pw}, nil
}

func (u *UI) openRepo(name string) (*repository.Repository, error) {
	u.mu.Lock()
	r, ok := u.open[name]
	u.mu.Unlock()
	if ok {
		// Pick up new snapshots/index entries written by other operations.
		if err := r.LoadIndex(u.ctx); err == nil {
			return r, nil
		}
	}
	r, _, err := u.eng.OpenByName(u.ctx, name)
	if err != nil {
		return nil, err
	}
	u.mu.Lock()
	u.open[name] = r
	u.mu.Unlock()
	return r, nil
}

func (u *UI) saveConfig(c *config.Config) error {
	if err := c.Save(); err != nil {
		return err
	}
	u.eng.SetConfig(c)
	tray.ApplyLogged(c, u.eng.Log)
	u.mu.Lock()
	u.open = map[string]*repository.Repository{}
	u.mu.Unlock()
	if cl, err := daemon.NewClient(c); err == nil {
		_ = cl.Reload() // keep a running daemon in sync
	}
	return nil
}

// ---------- dashboard ----------

func (u *UI) buildDashboard() tview.Primitive {
	u.jobs = tview.NewTable().SetSelectable(true, false).SetFixed(1, 0)
	u.jobs.SetBorder(true).SetTitle(" Backup jobs ")
	u.runsView = tview.NewTextView().SetDynamicColors(true)
	u.runsView.SetBorder(true).SetTitle(" Activity ")
	u.jobs.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		job := u.selectedJob()
		switch {
		case ev.Key() == tcell.KeyEnter || ev.Rune() == 'r':
			if job != nil {
				u.chooseType(job.Name)
			}
		case ev.Rune() == 'f' && job != nil:
			u.startJob(job.Name, "full")
		case ev.Rune() == 'i' && job != nil:
			u.startJob(job.Name, "incremental")
		case ev.Rune() == 'e' && job != nil:
			u.editJob(job)
		case ev.Rune() == 'n':
			u.editJob(nil)
		case ev.Rune() == 'w':
			u.startWizard()
		case ev.Rune() == 'c' && job != nil:
			if !u.runner.Cancel(daemon.KindBackup + ":" + job.Name) {
				u.message("Cancel", "No active run for "+job.Name)
			}
		case (ev.Key() == tcell.KeyDelete || ev.Rune() == 'x') && job != nil:
			name := job.Name
			u.confirm("Delete job "+name+" from the configuration?\n(Snapshots are kept.)", func() {
				c := u.eng.Config().Clone()
				for i, j := range c.Jobs {
					if j.Name == name {
						c.Jobs = append(c.Jobs[:i], c.Jobs[i+1:]...)
						break
					}
				}
				if err := u.saveConfig(c); err != nil {
					u.errorf("%v", err)
				}
				u.refreshJobs()
			})
		default:
			return ev
		}
		return nil
	})
	return tview.NewFlex().SetDirection(tview.FlexRow).AddItem(u.jobs, 0, 3, true).AddItem(u.runsView, 0, 2, false)
}

func (u *UI) selectedJob() *config.Job {
	row, _ := u.jobs.GetSelection()
	cell := u.jobs.GetCell(row, 0)
	if row == 0 || cell == nil {
		return nil
	}
	return u.eng.Config().Job(cell.Text)
}

func cellsHeader(t *tview.Table, cols ...string) {
	for i, c := range cols {
		t.SetCell(0, i, tview.NewTableCell(c).SetTextColor(tcell.ColorYellow).SetSelectable(false).SetExpansion(1))
	}
}

func (u *UI) refreshJobs() {
	cfg := u.eng.Config()
	row, _ := u.jobs.GetSelection()
	u.jobs.Clear()
	cellsHeader(u.jobs, "JOB", "REPOSITORY", "SOURCES", "SCHEDULE", "LAST RUN", "STATUS")
	for i, j := range cfg.Jobs {
		var sched []string
		for _, s := range j.Schedules {
			t := s.Type
			if t == "" {
				t = "auto"
			}
			sched = append(sched, s.Cron+" "+t)
		}
		if j.Disabled {
			sched = []string{"[gray]disabled"}
		}
		srcs := append(append([]string(nil), j.Paths...), j.Devices...)
		if j.Docker != nil {
			srcs = append([]string{"Docker projects"}, srcs...)
		}
		src := strings.Join(srcs, ", ")
		u.jobs.SetCell(i+1, 0, tview.NewTableCell(j.Name).SetExpansion(1))
		u.jobs.SetCell(i+1, 1, tview.NewTableCell(j.Repository).SetExpansion(1))
		u.jobs.SetCell(i+1, 2, tview.NewTableCell(src).SetExpansion(2).SetMaxWidth(40))
		u.jobs.SetCell(i+1, 3, tview.NewTableCell(strings.Join(sched, "; ")).SetExpansion(2).SetMaxWidth(40))
	}
	if len(cfg.Jobs) == 0 {
		u.jobs.SetCell(1, 0, tview.NewTableCell("[gray]no jobs yet — press w for the backup wizard (or n for the job form)").SetSelectable(false))
	}
	u.refreshJobStatus()
	if row < 1 {
		row = 1
	}
	u.jobs.Select(row, 0)
}

func (u *UI) refreshJobStatus() {
	cfg := u.eng.Config()
	h := u.eng.History()
	hist, _ := h.Load()
	last := map[string]*engine.JobResult{}
	for _, r := range hist {
		if _, ok := last[r.Job]; !ok {
			last[r.Job] = r
		}
	}
	for i, j := range cfg.Jobs {
		when, status := "never", "-"
		color := "[gray]"
		if r := last[j.Name]; r != nil {
			when, status = ui.Ago(r.Start), r.Status
			color = map[string]string{"success": "[green]", "warning": "[yellow]", "failure": "[red]"}[r.Status]
		}
		if u.runner.IsActive(daemon.KindBackup, j.Name) {
			status, color = "running", "[aqua]"
		}
		u.jobs.SetCell(i+1, 4, tview.NewTableCell(when).SetExpansion(1))
		u.jobs.SetCell(i+1, 5, tview.NewTableCell(color+status).SetExpansion(1))
	}
}

func (u *UI) refreshRuns() {
	var b strings.Builder
	for _, r := range u.runner.Active() {
		fmt.Fprintf(&b, "[aqua]%s %s[-] (%s, %s) ", r.Kind, r.Target, r.State, dashed(r.Type))
		if r.ProgressStats != nil {
			s := r.ProgressStats
			pct := s.Percent()
			fmt.Fprintf(&b, "\n  %s %s  %d files  %s read  %s new  %s\n  [gray]%s[-]\n", bar(pct, 30), pctText(pct), s.FilesDone, ui.Bytes(s.BytesDone), ui.Bytes(s.BytesNew), ui.Duration(s.Elapsed), tview.Escape(s.Current))
		} else {
			fmt.Fprintf(&b, "%s\n", tview.Escape(r.Message))
		}
	}
	for _, r := range u.runner.Recent() {
		color := "[green]"
		res := "ok"
		if r.Error != "" {
			color, res = "[red]", r.Error
		} else if r.Result != nil {
			res = r.Result.Status
			if r.Result.Summary != nil {
				res += fmt.Sprintf(" — snapshot %s, %s new", r.Result.Snapshot[:8], ui.Bytes(r.Result.Summary.BytesAdded))
			}
			if r.Result.Status == "warning" {
				color = "[yellow]"
			}
		}
		fmt.Fprintf(&b, "%s%s %s %s[-] %s\n", color, ui.Time(r.Started), r.Kind, r.Target, tview.Escape(res))
	}
	if cl, err := daemon.NewClient(u.eng.Config()); err == nil {
		if st, err := cl.Status(); err == nil && len(st.Schedule) > 0 {
			e := st.Schedule[0]
			fmt.Fprintf(&b, "\n[gray]daemon running — next: %s %s at %s (in %s)[-]\n", e.Kind, e.Target, ui.Time(e.Next), ui.Until(e.Next))
		}
	}
	if b.Len() == 0 {
		b.WriteString("[gray]No activity. Select a job and press Enter to run a backup.")
	}
	u.runsView.SetText(b.String())
}

func dashed(s string) string {
	if s == "" {
		return "auto"
	}
	return s
}

func pctText(p float64) string {
	if p < 0 {
		return "  ?%"
	}
	return fmt.Sprintf("%3.0f%%", p)
}

func bar(p float64, w int) string {
	if p < 0 {
		return "[" + strings.Repeat("·", w) + "]"
	}
	n := int(p / 100 * float64(w))
	return "[green]" + strings.Repeat("█", n) + "[gray]" + strings.Repeat("░", w-n) + "[-]"
}

func (u *UI) chooseType(job string) {
	m := tview.NewModal().SetText("Run backup job " + job + " as:").
		AddButtons([]string{"Auto", "Full", "Incremental", "Differential", "Cancel"}).
		SetDoneFunc(func(i int, label string) {
			u.closeModal("type")
			if label != "Cancel" && label != "" {
				u.startJob(job, strings.ToLower(label))
			}
		})
	u.pages.AddPage("modal-type", m, true, true)
	u.app.SetFocus(m)
}

func (u *UI) startJob(job, typ string) {
	if _, err := u.runner.Enqueue(daemon.KindBackup, job, typ, "manual"); err != nil {
		u.errorf("%v", err)
		return
	}
	u.refreshRuns()
}

// ---------- job editor ----------

func joinList(l []string) string { return strings.Join(l, "; ") }

func splitList(s string) []string {
	var out []string
	for _, p := range strings.FieldsFunc(s, func(r rune) bool { return r == ';' || r == '\n' }) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func itoa(n int) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprint(n)
}

func atoi(s string) int {
	var n int
	fmt.Sscan(strings.TrimSpace(s), &n)
	return n
}

func (u *UI) editJob(orig *config.Job) {
	cfg := u.eng.Config()
	j := &config.Job{}
	title := " New job "
	if orig != nil {
		j = cfg.Clone().Job(orig.Name)
		title = " Edit job " + orig.Name + " "
	} else if len(cfg.Repositories) > 0 {
		j.Repository = cfg.Repositories[0].Name
	}
	var repoNames []string
	sel := 0
	for i, r := range cfg.Repositories {
		repoNames = append(repoNames, r.Name)
		if r.Name == j.Repository {
			sel = i
		}
	}
	if len(repoNames) == 0 {
		u.errorf("Create a repository first (tab 2).")
		return
	}
	var sched []string
	for _, s := range j.Schedules {
		sched = append(sched, s.Cron+"="+dashed(s.Type))
	}
	rp := j.Retention
	form := tview.NewForm()
	form.AddInputField("Name", j.Name, 30, nil, nil)
	form.AddInputField("Description", j.Description, 50, nil, nil)
	form.AddDropDown("Repository", repoNames, sel, nil)
	form.AddInputField("Paths (; separated)", joinList(j.Paths), 60, nil, nil)
	form.AddInputField("Devices (; separated)", joinList(j.Devices), 60, nil, nil)
	form.AddInputField("Exclude patterns", joinList(j.Exclude), 60, nil, nil)
	form.AddInputField("Exclude if present", joinList(j.ExcludeIfPresent), 40, nil, nil)
	form.AddInputField("Exclude larger than", j.ExcludeLargerThan, 10, nil, nil)
	form.AddCheckbox("Exclude caches", j.ExcludeCaches, nil)
	form.AddCheckbox("One file system", j.OneFileSystem, nil)
	form.AddInputField("Tags (; separated)", joinList(j.Tags), 40, nil, nil)
	form.AddInputField("Schedules (cron=type; …)", joinList(sched), 60, nil, nil)
	form.AddInputField("Full interval (e.g. 7d)", j.FullInterval, 10, nil, nil)
	form.AddInputField("Keep last", itoa(rp.KeepLast), 6, nil, nil)
	form.AddInputField("Keep hourly", itoa(rp.KeepHourly), 6, nil, nil)
	form.AddInputField("Keep daily", itoa(rp.KeepDaily), 6, nil, nil)
	form.AddInputField("Keep weekly", itoa(rp.KeepWeekly), 6, nil, nil)
	form.AddInputField("Keep monthly", itoa(rp.KeepMonthly), 6, nil, nil)
	form.AddInputField("Keep yearly", itoa(rp.KeepYearly), 6, nil, nil)
	form.AddInputField("Keep full backups", itoa(rp.KeepFull), 6, nil, nil)
	within := ""
	if !rp.KeepWithin.IsZero() {
		within = rp.KeepWithin.String()
	}
	form.AddInputField("Keep within (e.g. 14d)", within, 10, nil, nil)
	form.AddCheckbox("Prune after forget", j.Prune, nil)
	form.AddCheckbox("Catch up missed runs", j.CatchUp, nil)
	form.AddCheckbox("Disabled", j.Disabled, nil)
	text := func(label string) string { return form.GetFormItemByLabel(label).(*tview.InputField).GetText() }
	check := func(label string) bool { return form.GetFormItemByLabel(label).(*tview.Checkbox).IsChecked() }
	form.AddButton("Save", func() {
		nj := *j
		nj.Name = strings.TrimSpace(text("Name"))
		nj.Description = text("Description")
		_, nj.Repository = form.GetFormItemByLabel("Repository").(*tview.DropDown).GetCurrentOption()
		nj.Paths, nj.Devices, nj.Exclude = splitList(text("Paths (; separated)")), splitList(text("Devices (; separated)")), splitList(text("Exclude patterns"))
		nj.ExcludeIfPresent, nj.Tags = splitList(text("Exclude if present")), splitList(text("Tags (; separated)"))
		nj.ExcludeLargerThan = strings.TrimSpace(text("Exclude larger than"))
		nj.ExcludeCaches, nj.OneFileSystem = check("Exclude caches"), check("One file system")
		nj.Schedules = nil
		for _, s := range splitList(text("Schedules (cron=type; …)")) {
			expr, typ, _ := strings.Cut(s, "=")
			typ = strings.TrimSpace(typ)
			if typ == "auto" {
				typ = ""
			}
			nj.Schedules = append(nj.Schedules, config.Schedule{Cron: strings.TrimSpace(expr), Type: typ})
		}
		nj.FullInterval = strings.TrimSpace(text("Full interval (e.g. 7d)"))
		nj.Retention.KeepLast, nj.Retention.KeepHourly = atoi(text("Keep last")), atoi(text("Keep hourly"))
		nj.Retention.KeepDaily, nj.Retention.KeepWeekly = atoi(text("Keep daily")), atoi(text("Keep weekly"))
		nj.Retention.KeepMonthly, nj.Retention.KeepYearly = atoi(text("Keep monthly")), atoi(text("Keep yearly"))
		nj.Retention.KeepFull = atoi(text("Keep full backups"))
		if w := strings.TrimSpace(text("Keep within (e.g. 14d)")); w != "" {
			if err := nj.Retention.KeepWithin.UnmarshalText([]byte(w)); err != nil {
				u.errorf("%v", err)
				return
			}
		} else {
			nj.Retention.KeepWithin = retention.Duration{}
		}
		nj.Prune, nj.CatchUp, nj.Disabled = check("Prune after forget"), check("Catch up missed runs"), check("Disabled")
		c := u.eng.Config().Clone()
		replaced := false
		for i, x := range c.Jobs {
			if orig != nil && x.Name == orig.Name {
				c.Jobs[i] = &nj
				replaced = true
			}
		}
		if !replaced {
			c.Jobs = append(c.Jobs, &nj)
		}
		if err := u.saveConfig(c); err != nil {
			u.errorf("Configuration invalid:\n%v", err)
			return
		}
		u.closeModal("job")
		u.refreshJobs()
	})
	form.AddButton("Cancel", func() { u.closeModal("job") })
	form.SetCancelFunc(func() { u.closeModal("job") })
	form.SetBorder(true).SetTitle(title)
	u.modal("job", form, 100, 40)
}
