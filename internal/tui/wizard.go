package tui

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/lamisator/herakles/internal/blockdev"
	"github.com/lamisator/herakles/internal/config"
	"github.com/lamisator/herakles/internal/crypto"
	"github.com/lamisator/herakles/internal/docker"
	"github.com/lamisator/herakles/internal/ui"
	"github.com/lamisator/herakles/internal/wizard"
)

// wiz is the backup wizard: a modal with the steps on the left and the
// current step on the right.
type wiz struct {
	u        *UI
	plan     wizard.Plan
	step     int
	flex     *tview.Flex
	steps    *tview.TextView
	newRepo  bool
	saved    bool
	pw2      string
	projects []docker.Project
	dockErr  string
	modeAll  bool
	folders  string
	chosen   map[string]bool
	stop     map[string]bool
}

var wizSteps = []string{"What", "Sources", "Where", "When", "How long", "Finish"}

func (u *UI) startWizard() {
	cfg := u.eng.Config()
	w := &wiz{u: u, modeAll: true, chosen: map[string]bool{}, stop: map[string]bool{},
		plan: wizard.Plan{Schedule: "nightly", Time: "02:00", Keep: "standard", Maintenance: true,
			PasswordMode: wizard.Generate, Password: wizard.GeneratePassword(), SkipCaches: true,
			New: &wizard.NewRepo{Type: wizard.Local, Dir: "herakles"}}}
	if len(cfg.Repositories) > 0 {
		w.plan.Repo = cfg.Repositories[0].Name
	} else {
		w.newRepo = true
	}
	u.wiz = w
	w.steps = tview.NewTextView().SetDynamicColors(true)
	w.steps.SetBorder(true).SetTitle(" Steps ")
	w.flex = tview.NewFlex()
	w.flex.SetBorder(true).SetTitle(" Backup wizard ")
	u.modal("wizard", w.flex, 110, 32)
	w.render()
}

func (w *wiz) cancel() {
	w.u.confirm("Leave the backup wizard? Nothing has been saved.", func() { w.u.closeModal("wizard") })
}

func (w *wiz) render() {
	var b strings.Builder
	for i, s := range wizSteps {
		switch {
		case i < w.step:
			fmt.Fprintf(&b, " [green]✓ %d %s[-]\n\n", i+1, s)
		case i == w.step:
			fmt.Fprintf(&b, " [black:aqua] %d %s [-:-]\n\n", i+1, s)
		default:
			fmt.Fprintf(&b, "   %d %s\n\n", i+1, s)
		}
	}
	w.steps.SetText(b.String())
	content := []func() tview.Primitive{w.what, w.sources, w.where, w.when, w.keep, w.finish}[w.step]()
	w.flex.Clear().AddItem(w.steps, 18, 0, false).AddItem(content, 0, 1, true)
	w.u.app.SetFocus(content)
}

func (w *wiz) go_(step int) {
	w.step = step
	w.render()
}

// form makes a step form with a title line and Back/Next/Cancel buttons.
func (w *wiz) form(title string, next func() error) *tview.Form {
	f := tview.NewForm()
	f.SetBorder(true).SetTitle(" " + title + " ")
	f.SetCancelFunc(w.cancel)
	if w.step > 0 {
		f.AddButton("Back", func() { w.go_(w.step - 1) })
	}
	label := "Next"
	if w.step == len(wizSteps)-1 {
		label = "Create backup"
	}
	f.AddButton(label, func() {
		if err := next(); err != nil {
			w.u.errorf("%v", err)
			return
		}
		if w.step < len(wizSteps)-1 {
			w.go_(w.step + 1)
		}
	})
	f.AddButton("Cancel", w.cancel)
	return f
}

func inputText(f *tview.Form, label string) string {
	return strings.TrimSpace(f.GetFormItemByLabel(label).(*tview.InputField).GetText())
}

func checked(f *tview.Form, label string) bool {
	return f.GetFormItemByLabel(label).(*tview.Checkbox).IsChecked()
}

// ---------- 1 what ----------

func (w *wiz) what() tview.Primitive {
	l := tview.NewList().
		AddItem("Files & folders", "Documents, home directories, server data", '1', nil).
		AddItem("Docker containers", "Each Compose project on its own, with volumes and database dumps", '2', nil).
		AddItem("A whole disk or partition", "An image for disaster recovery", '3', nil)
	l.SetBorder(true).SetTitle(" What do you want to back up? (Enter selects, Esc leaves) ")
	l.SetCurrentItem(map[string]int{wizard.Files: 0, wizard.Docker: 1, wizard.Disk: 2}[w.plan.Kind])
	l.SetSelectedFunc(func(i int, _, _ string, _ rune) {
		kind := []string{wizard.Files, wizard.Docker, wizard.Disk}[i]
		if kind != w.plan.Kind {
			w.plan.Kind, w.plan.Name = kind, ""
		}
		w.go_(1)
	})
	l.SetDoneFunc(w.cancel)
	return l
}

// ---------- 2 sources ----------

// listDevices is replaced in tests.
var listDevices = blockdev.List

// diskSources offers the discovered disks and partitions; the field below
// takes any other device and shows what will be saved in the job.
func (w *wiz) diskSources() tview.Primitive {
	f := w.form("Which disk or partition?", func() error {
		if strings.TrimSpace(w.plan.Device) == "" {
			return fmt.Errorf("choose a disk or partition")
		}
		return nil
	})
	devs, err := listDevices()
	field := tview.NewInputField().SetLabel("Device").SetText(w.plan.Device).SetFieldWidth(60)
	field.SetChangedFunc(func(t string) { w.plan.Device = strings.TrimSpace(t) })
	if len(devs) > 0 {
		depth := map[string]int{}
		opts, cur := make([]string, 0, len(devs)+1), -1
		if w.plan.Device != "" {
			cur = len(devs) // typed by hand
		}
		for i, d := range devs {
			if d.Parent != "" {
				depth[d.Name] = depth[d.Parent] + 1
			}
			opts = append(opts, fmt.Sprintf("%s%s  %s  %s", strings.Repeat("  ", depth[d.Name]), d.Path, ui.Bytes(int64(d.Size)), d.Describe()))
			if w.plan.Device != "" && (w.plan.Device == d.Path || w.plan.Device == d.Stable) {
				cur = i
			}
		}
		opts = append(opts, "Other device (type it below)")
		dd := tview.NewDropDown().SetLabel("Disk / partition").SetOptions(opts, nil)
		dd.SetCurrentOption(cur)
		dd.SetSelectedFunc(func(_ string, i int) {
			if i < len(devs) {
				field.SetText(devs[i].Best())
			}
		})
		f.AddFormItem(dd)
	}
	f.AddFormItem(field)
	hint := "A whole disk includes the partition table, boot loader and all partitions. " +
		"The job stores the stable name (/dev/disk/by-id/…), which survives reordered disks. " +
		"Reading devices needs root (administrator on Windows). Devices marked \"system\" change while they are read."
	if err != nil {
		hint = err.Error() + "\ne.g. /dev/sda1, /dev/nvme0n1 or \\\\.\\C: on Windows. Needs root/administrator rights."
	}
	f.AddTextView("", hint, 90, 5, true, false)
	return f
}

func (w *wiz) sources() tview.Primitive {
	switch w.plan.Kind {
	case wizard.Docker:
		return w.dockerSources()
	case wizard.Disk:
		return w.diskSources()
	}
	f := w.form("Which files and folders?", func() error {
		if len(splitList(w.joinedPaths())) == 0 {
			return fmt.Errorf("enter at least one folder")
		}
		return nil
	})
	f.AddInputField("Folders (; separated)", strings.Join(w.plan.Paths, "; "), 70, nil, func(t string) { w.plan.Paths = splitList(t) })
	f.AddCheckbox("Leave out caches and temporary files", w.plan.SkipCaches, func(c bool) { w.plan.SkipCaches = c })
	f.AddCheckbox("Leave out build and dependency folders", w.plan.SkipBuild, func(c bool) { w.plan.SkipBuild = c })
	f.AddCheckbox("Leave out Downloads and trash", w.plan.SkipDownloads, func(c bool) { w.plan.SkipDownloads = c })
	f.AddCheckbox("Stay on one file system", w.plan.OneFS, func(c bool) { w.plan.OneFS = c })
	f.AddInputField("More patterns to leave out (; separated)", strings.Join(w.plan.Extra, "; "), 50, nil, func(t string) { w.plan.Extra = splitList(t) })
	return f
}

func (w *wiz) joinedPaths() string { return strings.Join(w.plan.Paths, ";") }

func (w *wiz) dockerJob() *config.DockerJob {
	d := &config.DockerJob{}
	if !w.modeAll {
		d.Folders = splitList(w.folders)
	}
	for _, p := range w.projects {
		inFolder := !w.modeAll && d.InFolders(p.Dirs()...) != ""
		on := w.included(p)
		switch {
		case w.modeAll && !on, !w.modeAll && inFolder && !on:
			d.ExcludeProjects = append(d.ExcludeProjects, p.Name)
		case !w.modeAll && on && !inFolder:
			d.Projects = append(d.Projects, p.Name)
		}
		if w.stop[p.Name] {
			d.Stop = append(d.Stop, p.Name)
		}
	}
	return d
}

func (w *wiz) included(p docker.Project) bool {
	if v, ok := w.chosen[p.Name]; ok {
		return v
	}
	if w.modeAll {
		return true
	}
	return (&config.DockerJob{Folders: splitList(w.folders)}).InFolders(p.Dirs()...) != ""
}

func (w *wiz) loadProjects() {
	dc := &docker.Client{}
	for _, j := range w.u.eng.Config().Jobs {
		if j.Docker != nil && j.Docker.Binary != "" {
			dc = &docker.Client{Binary: j.Docker.Binary}
			break
		}
	}
	ps, err := dc.Projects(w.u.ctx, docker.Search{Trees: splitList(w.folders)})
	w.projects, w.dockErr = ps, ""
	if err != nil {
		w.dockErr = err.Error()
	}
	sort.Slice(w.projects, func(i, j int) bool { return w.projects[i].Name < w.projects[j].Name })
}

func (w *wiz) dockerSources() tview.Primitive {
	if w.projects == nil && w.dockErr == "" {
		w.loadProjects()
	}
	list := tview.NewList().ShowSecondaryText(true)
	list.SetBorder(true).SetTitle(" Projects: Enter = back up yes/no, s = stop during backup ")
	fill := func() {
		cur := list.GetCurrentItem()
		list.Clear()
		if w.dockErr != "" {
			list.AddItem("Docker is not available here", w.dockErr, 0, nil)
		}
		for _, p := range w.projects {
			mark := "[ ]"
			if w.included(p) {
				mark = "[x]"
			}
			info := fmt.Sprintf("%d/%d running", p.Running(), len(p.Containers))
			if len(p.Containers) == 0 {
				info = "directory only"
			}
			if f := (&config.DockerJob{Folders: splitList(w.folders)}).InFolders(p.Dirs()...); !w.modeAll && f != "" {
				info += " · in " + f
			}
			if w.stop[p.Name] {
				info += " · stopped during backup"
			}
			list.AddItem(mark+" "+p.Name, "    "+info+" · "+strings.Join(p.Dirs(), ", "), 0, nil)
		}
		if cur < list.GetItemCount() {
			list.SetCurrentItem(cur)
		}
	}
	list.SetSelectedFunc(func(i int, _, _ string, _ rune) {
		if i < len(w.projects) {
			p := w.projects[i]
			w.chosen[p.Name] = !w.included(p)
			fill()
		}
	})
	list.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		if ev.Rune() == 's' {
			if i := list.GetCurrentItem(); i < len(w.projects) {
				w.stop[w.projects[i].Name] = !w.stop[w.projects[i].Name]
				fill()
			}
			return nil
		}
		return ev
	})
	fill()
	f := w.form("Which containers?", func() error {
		w.plan.Docker = w.dockerJob()
		if !w.modeAll && len(w.plan.Docker.Projects) == 0 && len(w.plan.Docker.Folders) == 0 {
			return fmt.Errorf("choose at least one project or folder, or \"All projects\"")
		}
		return nil
	})
	mode := 0
	if !w.modeAll {
		mode = 1
	}
	f.AddDropDown("Back up", []string{"All projects (new ones too)", "Only chosen projects and folders"}, mode, func(_ string, i int) {
		if (i == 0) != w.modeAll {
			w.modeAll = i == 0
			w.chosen = map[string]bool{}
			fill()
		}
	})
	f.AddInputField("Folders included automatically", w.folders, 40, nil, func(t string) { w.folders = t })
	f.GetFormItemByLabel("Folders included automatically").(*tview.InputField).SetDoneFunc(func(tcell.Key) {
		w.loadProjects()
		fill()
	})
	f.AddTextView("", "Folders (e.g. /srv; ; separated) include every project in them, also new ones. Press Enter after typing to refresh the list. Tab moves to the list.", 0, 3, true, false)
	return tview.NewFlex().SetDirection(tview.FlexRow).AddItem(f, 11, 0, true).AddItem(list, 0, 1, false)
}

// ---------- 3 where ----------

func (w *wiz) where() tview.Primitive {
	cfg := w.u.eng.Config()
	f := w.form("Where should the backups go?", w.checkWhere)
	var opts []string
	sel := len(cfg.Repositories)
	for i, r := range cfg.Repositories {
		opts = append(opts, r.Name+" ("+r.Location+")")
		if !w.newRepo && r.Name == w.plan.Repo {
			sel = i
		}
	}
	opts = append(opts, "New location…")
	f.AddDropDown("Repository", opts, sel, func(_ string, i int) {
		isNew := i == len(cfg.Repositories)
		if !isNew {
			w.plan.Repo = cfg.Repositories[i].Name
		}
		if isNew != w.newRepo {
			w.newRepo = isNew
			w.render()
		}
	})
	if !w.newRepo {
		return f
	}
	n := w.plan.New
	types := []string{wizard.Local, wizard.SMB, wizard.SFTP, wizard.Server}
	ti := 0
	for i, t := range types {
		if t == n.Type {
			ti = i
		}
	}
	f.AddDropDown("Storage", []string{"Local or USB disk (also mounted shares)", "Network share (SMB)", "SFTP server", "Herakles server"}, ti, func(_ string, i int) {
		if types[i] != n.Type {
			n.Type, n.Name = types[i], ""
			w.render()
		}
	})
	if n.Name == "" {
		n.Name = wizard.DefaultRepoName(n.Type, cfg)
	}
	in := func(label, value string, width int, set func(string)) {
		f.AddInputField(label, value, width, nil, set)
	}
	in("Name", n.Name, 20, func(t string) { n.Name = strings.TrimSpace(t) })
	switch n.Type {
	case wizard.Local:
		in("Folder for the backups", n.Path, 50, func(t string) { n.Path = t })
	case wizard.SMB:
		in("NAS / server", n.Host, 30, func(t string) { n.Host = t })
		in("Share", n.Share, 20, func(t string) { n.Share = t })
		in("Folder in the share", n.Dir, 30, func(t string) { n.Dir = t })
		in("User name", n.User, 20, func(t string) { n.User = t })
		f.AddPasswordField("Share password", n.Pass, 30, '*', func(t string) { n.Pass = t })
	case wizard.SFTP:
		in("Server", n.Host, 30, func(t string) { n.Host = t })
		in("User name", n.User, 20, func(t string) { n.User = t })
		in("Folder on the server", n.Dir, 40, func(t string) { n.Dir = t })
		in("SSH key (optional)", n.Identity, 40, func(t string) { n.Identity = t })
	case wizard.Server:
		in("Server (host:port)", n.URL, 40, func(t string) { n.URL = t })
		in("Repository on the server", n.ServerRepo, 20, func(t string) { n.ServerRepo = t })
		in("Account", n.User, 20, func(t string) { n.User = t })
		f.AddPasswordField("Account password", n.Pass, 30, '*', func(t string) { n.Pass = t })
		in("Server certificate (optional)", n.CA, 40, func(t string) { n.CA = t })
	}
	modes := []string{wizard.Generate, wizard.Choose, wizard.Ask}
	mi := 0
	for i, m := range modes {
		if m == w.plan.PasswordMode {
			mi = i
		}
	}
	f.AddDropDown("Encryption password", []string{"Generate one and store it here", "Choose one and store it here", "Ask every time (not stored)"}, mi, func(_ string, i int) {
		if modes[i] != w.plan.PasswordMode {
			w.plan.PasswordMode = modes[i]
			w.render()
		}
	})
	if w.plan.PasswordMode == wizard.Generate {
		f.AddTextView("Password", "[yellow::b]"+w.plan.Password+"[-::-]  — write it down or store it in a password manager. Without it nobody can read the backups.", 0, 2, true, false)
		f.AddCheckbox("I have saved the password", w.saved, func(c bool) { w.saved = c })
	} else {
		pw1 := w.plan.Password
		if w.plan.PasswordMode == wizard.Generate {
			pw1 = ""
		}
		f.AddPasswordField("Password (min. 10)", pw1, 30, '*', func(t string) { w.plan.Password = t })
		f.AddPasswordField("Repeat", w.pw2, 30, '*', func(t string) { w.pw2 = t })
	}
	f.AddCheckbox("Free space weekly, check monthly", w.plan.Maintenance, func(c bool) { w.plan.Maintenance = c })
	return f
}

func (w *wiz) checkWhere() error {
	if !w.newRepo {
		return nil
	}
	if _, err := w.plan.Repository(); err != nil {
		return err
	}
	if w.u.eng.Config().Repo(w.plan.New.Name) != nil {
		return fmt.Errorf("a repository named %s exists already", w.plan.New.Name)
	}
	switch w.plan.PasswordMode {
	case wizard.Generate:
		if !w.saved {
			return fmt.Errorf("save the password first and tick the box")
		}
	default:
		if len(w.plan.Password) < 10 {
			return fmt.Errorf("the password needs at least 10 characters")
		}
		if w.plan.Password != w.pw2 {
			return fmt.Errorf("the passwords do not match")
		}
	}
	return nil
}

// ---------- 4 when, 5 how long ----------

func presetIndex(ps []wizard.Preset, id string) int {
	for i, p := range ps {
		if p.ID == id {
			return i
		}
	}
	return 0
}

func labels(ps []wizard.Preset) []string {
	var out []string
	for _, p := range ps {
		out = append(out, p.Label)
	}
	return out
}

func (w *wiz) when() tview.Primitive {
	f := w.form("When should it run?", func() error { return nil })
	f.AddDropDown("Schedule", labels(wizard.Schedules), presetIndex(wizard.Schedules, w.plan.Schedule), func(_ string, i int) { w.plan.Schedule = wizard.Schedules[i].ID })
	f.AddInputField("Time (HH:MM)", w.plan.Time, 6, nil, func(t string) { w.plan.Time = t })
	f.AddTextView("", "Missed backups run when Herakles starts again. Scheduled backups need the daemon (herakles daemon install) or the tray running.", 0, 3, true, false)
	return f
}

func (w *wiz) keep() tview.Primitive {
	f := w.form("How long should backups be kept?", func() error { return nil })
	f.AddDropDown("Keep", labels(wizard.Retentions), presetIndex(wizard.Retentions, w.plan.Keep), func(_ string, i int) { w.plan.Keep = wizard.Retentions[i].ID })
	f.AddTextView("", "Older backups are thinned out after every run; the newest one is never removed.", 0, 2, true, false)
	return f
}

// ---------- 6 finish ----------

func (w *wiz) finish() tview.Primitive {
	cfg := w.u.eng.Config()
	if w.plan.Name == "" {
		w.plan.Name = wizard.DefaultName(w.plan.Kind, cfg)
	}
	runNow := true
	f := w.form("Check and create", func() error { return w.create(runNow) })
	f.AddInputField("Job name", w.plan.Name, 30, nil, func(t string) { w.plan.Name = strings.TrimSpace(t) })
	f.AddInputField("Description", w.plan.Description, 50, nil, func(t string) { w.plan.Description = t })
	f.AddCheckbox("Start the first backup right away", runNow, func(c bool) { runNow = c })
	f.AddTextView("Summary", w.summary(), 0, 7, true, false)
	return f
}

func (w *wiz) summary() string {
	var b strings.Builder
	switch w.plan.Kind {
	case wizard.Docker:
		d := w.plan.Docker
		s := "all Docker projects"
		if d != nil && (len(d.Projects) > 0 || len(d.Folders) > 0) {
			var parts []string
			if len(d.Folders) > 0 {
				parts = append(parts, "every project in "+strings.Join(d.Folders, ", "))
			}
			if len(d.Projects) > 0 {
				parts = append(parts, strings.Join(d.Projects, ", "))
			}
			s = strings.Join(parts, " + ")
		}
		if d != nil && len(d.ExcludeProjects) > 0 {
			s += " except " + strings.Join(d.ExcludeProjects, ", ")
		}
		fmt.Fprintf(&b, "Backs up:  %s\n", s)
	case wizard.Disk:
		fmt.Fprintf(&b, "Backs up:  %s\n", w.plan.Device)
	default:
		fmt.Fprintf(&b, "Backs up:  %s\n", strings.Join(w.plan.Paths, ", "))
	}
	if w.newRepo {
		r, _ := w.plan.Repository()
		if r != nil {
			fmt.Fprintf(&b, "Stores in: %s — %s (new)\n", r.Name, r.Location)
		}
	} else {
		fmt.Fprintf(&b, "Stores in: %s\n", w.plan.Repo)
	}
	fmt.Fprintf(&b, "Runs:      %s at %s\n", wizard.Schedules[presetIndex(wizard.Schedules, w.plan.Schedule)].Label, w.plan.Time)
	fmt.Fprintf(&b, "Keeps:     %s", wizard.Retentions[presetIndex(wizard.Retentions, w.plan.Keep)].Label)
	return b.String()
}

func (w *wiz) create(runNow bool) error {
	cfg := w.u.eng.Config()
	if cfg.Job(w.plan.Name) != nil {
		return fmt.Errorf("a job named %s exists already", w.plan.Name)
	}
	repoName := w.plan.Repo
	var rc *config.Repository
	if w.newRepo {
		var err error
		if rc, err = w.plan.Repository(); err != nil {
			return err
		}
		repoName = rc.Name
	}
	job, err := w.plan.Job(repoName)
	if err != nil {
		return err
	}
	c := cfg.Clone()
	c.Jobs = append(c.Jobs, job)
	if rc != nil {
		c.Repositories = append(c.Repositories, rc)
	}
	if err := c.Validate(); err != nil {
		return err
	}
	w.u.closeModal("wizard")
	pw, mode := w.plan.Password, w.plan.PasswordMode
	w.u.background("Backup wizard", func() (string, error) {
		if rc != nil {
			if mode != wizard.Ask {
				p, err := wizard.StorePassword(filepath.Dir(cfg.Path()), rc.Name, pw)
				if err != nil {
					return "", err
				}
				rc.PasswordFile = p
			}
			creds := crypto.Credentials{Password: pw}
			r, err := w.u.eng.Init(w.u.ctx, rc, creds)
			if err != nil {
				return "", fmt.Errorf("create repository: %w", err)
			}
			r.Close()
			if mode == wizard.Ask {
				w.u.eng.RememberCredentials(rc.Name, creds)
			}
		}
		done := make(chan error, 1)
		w.u.app.QueueUpdateDraw(func() {
			done <- w.u.saveConfig(c)
			if runNow {
				w.u.startJob(job.Name, "auto")
			}
			w.u.refreshJobs()
		})
		if err := <-done; err != nil {
			return "", err
		}
		msg := "Backup job " + job.Name + " is ready."
		if runNow {
			msg += "\nThe first backup is running."
		}
		if rc != nil && mode == wizard.Generate {
			msg += "\n\nKeep the repository password safe:\n" + pw
		}
		return msg, nil
	})
	return nil
}
