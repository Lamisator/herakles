package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/lamisator/herakles/internal/config"
	"github.com/lamisator/herakles/internal/crypto"
	"github.com/lamisator/herakles/internal/daemon"
	"github.com/lamisator/herakles/internal/id"
	"github.com/lamisator/herakles/internal/repository"
	"github.com/lamisator/herakles/internal/restorer"
	"github.com/lamisator/herakles/internal/ui"
)

// ---------- repositories ----------

func (u *UI) buildRepos() tview.Primitive {
	u.repos = tview.NewTable().SetSelectable(true, false).SetFixed(1, 0)
	u.repos.SetBorder(true).SetTitle(" Repositories ")
	u.repos.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		rc := u.selectedRepo()
		switch {
		case ev.Rune() == 'n':
			u.editRepo(nil)
		case (ev.Rune() == 'e' || ev.Key() == tcell.KeyEnter) && rc != nil:
			u.editRepo(rc)
		case ev.Rune() == 'I' && rc != nil:
			u.initRepo(rc)
		case ev.Rune() == 'k' && rc != nil:
			if _, err := u.runner.Enqueue(daemon.KindCheck, rc.Name, "", "manual"); err != nil {
				u.errorf("%v", err)
			} else {
				u.message("Check", "Repository check started; see the dashboard for progress.")
			}
		case ev.Rune() == 'p' && rc != nil:
			name := rc.Name
			u.confirm("Apply retention policies and prune repository "+name+"?", func() {
				if _, err := u.runner.Enqueue(daemon.KindPrune, name, "", "manual"); err != nil {
					u.errorf("%v", err)
				}
			})
		case ev.Rune() == 's' && rc != nil:
			name := rc.Name
			u.background("Statistics", func() (string, error) {
				r, err := u.openRepo(name)
				if err != nil {
					return "", err
				}
				st, err := r.Stats(u.ctx)
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("Repository %s\n\nSnapshots: %d\nPacks: %d\nBlobs: %d\nUnique data: %s\nStored: %s (%s)",
					name, st.Snapshots, st.Packs, st.Blobs, ui.Bytes(st.RawBytes), ui.Bytes(st.StoredBytes), ui.Ratio(st.RawBytes, st.StoredBytes)), nil
			})
		case (ev.Key() == tcell.KeyDelete || ev.Rune() == 'x') && rc != nil:
			name := rc.Name
			u.confirm("Remove repository "+name+" from the configuration?\n(The stored data is NOT deleted.)", func() {
				c := u.eng.Config().Clone()
				for i, r := range c.Repositories {
					if r.Name == name {
						c.Repositories = append(c.Repositories[:i], c.Repositories[i+1:]...)
						break
					}
				}
				if err := u.saveConfig(c); err != nil {
					u.errorf("%v", err)
				}
				u.refreshRepos()
			})
		default:
			return ev
		}
		return nil
	})
	return u.repos
}

func (u *UI) selectedRepo() *config.Repository {
	row, _ := u.repos.GetSelection()
	cell := u.repos.GetCell(row, 0)
	if row == 0 || cell == nil {
		return nil
	}
	return u.eng.Config().Repo(cell.Text)
}

func credSource(r *config.Repository) string {
	var s []string
	switch {
	case r.Password != "":
		s = append(s, "inline password")
	case r.PasswordFile != "":
		s = append(s, "password file")
	case r.PasswordEnv != "":
		s = append(s, "env "+r.PasswordEnv)
	case r.PasswordCommand != "":
		s = append(s, "command")
	}
	if r.KeyFile != "" {
		s = append(s, "key file")
	}
	if len(s) == 0 {
		return "prompt"
	}
	return strings.Join(s, " + ")
}

func (u *UI) refreshRepos() {
	cfg := u.eng.Config()
	row, _ := u.repos.GetSelection()
	u.repos.Clear()
	cellsHeader(u.repos, "NAME", "LOCATION", "UNLOCK WITH", "COMPRESSION", "MAINTENANCE")
	for i, r := range cfg.Repositories {
		comp := r.Compression
		if comp == "" {
			comp = "auto"
		}
		var m []string
		if r.Maintenance.PruneSchedule != "" {
			m = append(m, "prune "+r.Maintenance.PruneSchedule)
		}
		if r.Maintenance.CheckSchedule != "" {
			m = append(m, "check "+r.Maintenance.CheckSchedule)
		}
		u.repos.SetCell(i+1, 0, tview.NewTableCell(r.Name).SetExpansion(1))
		u.repos.SetCell(i+1, 1, tview.NewTableCell(r.Location).SetExpansion(3))
		u.repos.SetCell(i+1, 2, tview.NewTableCell(credSource(r)).SetExpansion(1))
		u.repos.SetCell(i+1, 3, tview.NewTableCell(comp).SetExpansion(1))
		u.repos.SetCell(i+1, 4, tview.NewTableCell(strings.Join(m, "; ")).SetExpansion(2))
	}
	if len(cfg.Repositories) == 0 {
		u.repos.SetCell(1, 0, tview.NewTableCell("[gray]no repositories — press n to add one").SetSelectable(false))
	}
	if row < 1 {
		row = 1
	}
	u.repos.Select(row, 0)
}

func (u *UI) editRepo(orig *config.Repository) {
	r := &config.Repository{}
	title := " New repository "
	if orig != nil {
		r = u.eng.Config().Clone().Repo(orig.Name)
		title = " Edit repository " + orig.Name + " "
	}
	form := tview.NewForm()
	form.AddInputField("Name", r.Name, 30, nil, nil)
	form.AddInputField("Location", r.Location, 70, nil, nil)
	form.AddTextView("", "[gray]/path, \\\\server\\share\\dir, smb://user@host/share/dir,\nsftp://user@host/dir, herakles://server:8443/repo", 70, 2, true, false)
	form.AddInputField("Password file", r.PasswordFile, 50, nil, nil)
	form.AddInputField("Password env var", r.PasswordEnv, 30, nil, nil)
	form.AddInputField("Password command", r.PasswordCommand, 50, nil, nil)
	form.AddPasswordField("Inline password", r.Password, 30, '*', nil)
	form.AddInputField("Key file", r.KeyFile, 50, nil, nil)
	form.AddInputField("Compression", r.Compression, 15, nil, nil)
	form.AddInputField("Remote user", r.Remote.Username, 30, nil, nil)
	form.AddPasswordField("Remote password", r.Remote.Password, 30, '*', nil)
	form.AddInputField("Upload limit KiB/s", itoa(r.UploadLimitKiB), 8, nil, nil)
	form.AddInputField("Prune schedule (cron)", r.Maintenance.PruneSchedule, 20, nil, nil)
	form.AddInputField("Check schedule (cron)", r.Maintenance.CheckSchedule, 20, nil, nil)
	form.AddInputField("Check read data (%)", r.Maintenance.CheckReadData, 8, nil, nil)
	text := func(label string) string {
		return strings.TrimSpace(form.GetFormItemByLabel(label).(*tview.InputField).GetText())
	}
	form.AddButton("Save", func() {
		nr := *r
		nr.Name, nr.Location = text("Name"), text("Location")
		nr.PasswordFile, nr.PasswordEnv, nr.PasswordCommand = text("Password file"), text("Password env var"), text("Password command")
		nr.Password, nr.KeyFile, nr.Compression = text("Inline password"), text("Key file"), text("Compression")
		nr.Remote.Username, nr.Remote.Password = text("Remote user"), text("Remote password")
		nr.UploadLimitKiB = atoi(text("Upload limit KiB/s"))
		nr.Maintenance.PruneSchedule, nr.Maintenance.CheckSchedule = text("Prune schedule (cron)"), text("Check schedule (cron)")
		nr.Maintenance.CheckReadData = text("Check read data (%)")
		c := u.eng.Config().Clone()
		replaced := false
		for i, x := range c.Repositories {
			if orig != nil && x.Name == orig.Name {
				c.Repositories[i] = &nr
				replaced = true
				// Keep jobs pointing at a renamed repository.
				for _, j := range c.Jobs {
					if j.Repository == orig.Name {
						j.Repository = nr.Name
					}
				}
			}
		}
		if !replaced {
			c.Repositories = append(c.Repositories, &nr)
		}
		if err := u.saveConfig(c); err != nil {
			u.errorf("Configuration invalid:\n%v", err)
			return
		}
		u.closeModal("repo")
		u.refreshRepos()
		if orig == nil {
			u.confirm("Initialize a new repository at "+nr.Location+" now?\n(Choose No if it already exists.)", func() { u.initRepo(u.eng.Config().Repo(nr.Name)) })
		}
	})
	form.AddButton("Cancel", func() { u.closeModal("repo") })
	form.SetCancelFunc(func() { u.closeModal("repo") })
	form.SetBorder(true).SetTitle(title)
	u.modal("repo", form, 100, 38)
}

func (u *UI) initRepo(rc *config.Repository) {
	if rc == nil {
		return
	}
	run := func(creds crypto.Credentials) {
		u.background("Initialize", func() (string, error) {
			r, err := u.eng.Init(u.ctx, rc, creds)
			if err != nil {
				return "", err
			}
			r.Close()
			return "Repository " + rc.Name + " created.\nKeep the password / key file safe: without it the data cannot be recovered.", nil
		})
	}
	creds, err := rc.Credentials()
	if err == nil {
		run(creds)
		return
	}
	if err != config.ErrNoPassword {
		u.errorf("%v", err)
		return
	}
	form := tview.NewForm()
	form.AddPasswordField("New password", "", 40, '*', nil)
	form.AddPasswordField("Repeat", "", 40, '*', nil)
	form.AddButton("Create", func() {
		a := form.GetFormItem(0).(*tview.InputField).GetText()
		b := form.GetFormItem(1).(*tview.InputField).GetText()
		if a == "" || a != b {
			u.errorf("Passwords are empty or do not match")
			return
		}
		u.closeModal("init")
		u.eng.RememberCredentials(rc.Name, crypto.Credentials{Password: a})
		run(crypto.Credentials{Password: a})
	})
	form.AddButton("Cancel", func() { u.closeModal("init") })
	form.SetBorder(true).SetTitle(" Initialize " + rc.Name + " ")
	u.modal("init", form, 64, 9)
}

// ---------- snapshots ----------

func (u *UI) buildSnapshots() tview.Primitive {
	u.snapRepo = tview.NewDropDown().SetLabel("Repository: ")
	u.snapTable = tview.NewTable().SetSelectable(true, false).SetFixed(1, 0)
	u.snapTable.SetBorder(true).SetTitle(" Snapshots ")
	u.tree = tview.NewTreeView()
	u.tree.SetBorder(true).SetTitle(" Browse (Enter: expand, r: restore) ")
	u.snapTable.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		s := u.selectedSnap()
		switch {
		case ev.Key() == tcell.KeyTab:
			u.app.SetFocus(u.snapRepo)
		case ev.Key() == tcell.KeyEnter && s != nil:
			u.browse(s)
		case ev.Rune() == 'r' && s != nil:
			u.restoreDialog(s, "/", "snapshot "+s.ID().Short())
		case ev.Rune() == 'R':
			u.loadSnapshots()
		case ev.Rune() == 'd' && s != nil:
			snap := s
			u.confirm("Forget snapshot "+snap.ID().Short()+" from "+ui.Time(snap.Time)+"?\nRun prune afterwards to free the space.", func() {
				_, repoName := u.snapRepo.GetCurrentOption()
				u.background("Forget", func() (string, error) {
					r, err := u.openRepo(repoName)
					if err != nil {
						return "", err
					}
					l, err := r.Lock(u.ctx, false, "forget")
					if err != nil {
						return "", err
					}
					defer l.Unlock()
					if err := r.RemoveSnapshot(u.ctx, snap.ID()); err != nil {
						return "", err
					}
					u.app.QueueUpdateDraw(u.loadSnapshots)
					return "", nil
				})
			})
		default:
			return ev
		}
		return nil
	})
	u.snapRepo.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		if ev.Key() == tcell.KeyTab {
			u.app.SetFocus(u.snapTable)
			return nil
		}
		return ev
	})
	u.tree.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		switch {
		case ev.Key() == tcell.KeyEscape:
			u.app.SetFocus(u.snapTable)
			return nil
		case ev.Rune() == 'r':
			if n := u.tree.GetCurrentNode(); n != nil {
				if ref, ok := n.GetReference().(*treeRef); ok {
					u.restoreDialog(ref.snap, ref.path, ref.path)
				}
			}
			return nil
		}
		return ev
	})
	left := tview.NewFlex().SetDirection(tview.FlexRow).AddItem(u.snapRepo, 1, 0, false).AddItem(u.snapTable, 0, 1, true)
	return tview.NewFlex().AddItem(left, 0, 3, true).AddItem(u.tree, 0, 2, false)
}

type treeRef struct {
	snap   *repository.Snapshot
	path   string
	node   *repository.Node
	loaded bool
}

func (u *UI) refreshSnapRepos() {
	cfg := u.eng.Config()
	var names []string
	for _, r := range cfg.Repositories {
		names = append(names, r.Name)
	}
	cur, _ := u.snapRepo.GetCurrentOption()
	u.snapRepo.SetOptions(names, func(text string, index int) { u.loadSnapshots() })
	if len(names) > 0 {
		if cur < 0 || cur >= len(names) {
			cur = 0
		}
		u.snapRepo.SetCurrentOption(cur)
	}
}

func (u *UI) selectedSnap() *repository.Snapshot {
	row, _ := u.snapTable.GetSelection()
	if row < 1 || row > len(u.snapList) {
		return nil
	}
	return u.snapList[len(u.snapList)-row] // newest first
}

func (u *UI) loadSnapshots() {
	_, name := u.snapRepo.GetCurrentOption()
	if name == "" {
		return
	}
	u.snapTable.Clear()
	cellsHeader(u.snapTable, "ID", "TIME", "JOB", "HOST", "TYPE", "SIZE", "ADDED")
	u.snapTable.SetCell(1, 0, tview.NewTableCell("[gray]loading…").SetSelectable(false))
	go func() {
		var snaps []*repository.Snapshot
		r, err := u.openRepo(name)
		if err == nil {
			snaps, err = r.Snapshots(u.ctx)
		}
		u.app.QueueUpdateDraw(func() {
			u.snapTable.Clear()
			cellsHeader(u.snapTable, "ID", "TIME", "JOB", "HOST", "TYPE", "SIZE", "ADDED")
			if err != nil {
				u.snapTable.SetCell(1, 0, tview.NewTableCell("[red]"+tview.Escape(err.Error())).SetSelectable(false))
				return
			}
			u.snapList = snaps
			for i := range snaps {
				s := snaps[len(snaps)-1-i]
				size, added := "-", "-"
				if s.Summary != nil {
					size, added = ui.Bytes(s.Summary.TotalBytes), ui.Bytes(s.Summary.BytesStored)
				}
				for c, v := range []string{s.ID().Short(), ui.Time(s.Time), dashed2(s.Job), s.Hostname, s.Type, size, added} {
					u.snapTable.SetCell(i+1, c, tview.NewTableCell(v).SetExpansion(1))
				}
			}
			if len(snaps) == 0 {
				u.snapTable.SetCell(1, 0, tview.NewTableCell("[gray]no snapshots").SetSelectable(false))
			}
			u.snapTable.Select(1, 0)
		})
	}()
}

func dashed2(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func (u *UI) browse(s *repository.Snapshot) {
	_, name := u.snapRepo.GetCurrentOption()
	rootRef := &treeRef{snap: s, path: "/", node: &repository.Node{Name: "/", Type: repository.NodeDir, Subtree: ptr(s.Tree)}}
	root := tview.NewTreeNode(s.ID().Short() + " " + ui.Time(s.Time)).SetReference(rootRef).SetColor(tcell.ColorAqua)
	u.tree.SetRoot(root).SetCurrentNode(root)
	expand := func(n *tview.TreeNode) {
		ref := n.GetReference().(*treeRef)
		if ref.node.Type != repository.NodeDir {
			return
		}
		if ref.loaded {
			n.SetExpanded(!n.IsExpanded())
			return
		}
		ref.loaded = true
		go func() {
			r, err := u.openRepo(name)
			var t *repository.Tree
			if err == nil {
				t, err = r.LoadTree(u.ctx, *ref.node.Subtree)
			}
			u.app.QueueUpdateDraw(func() {
				if err != nil {
					n.AddChild(tview.NewTreeNode("error: " + err.Error()).SetColor(tcell.ColorRed))
					return
				}
				for _, c := range t.Nodes {
					p := strings.TrimSuffix(ref.path, "/") + "/" + c.Name
					label := c.Name
					color := tcell.ColorWhite
					switch c.Type {
					case repository.NodeDir:
						label += "/"
						color = tcell.ColorAqua
					case repository.NodeDevice:
						label += fmt.Sprintf("  [disk image %s]", ui.Bytes(int64(c.Size)))
						color = tcell.ColorFuchsia
					case repository.NodeSymlink:
						label += " -> " + c.LinkTarget
						color = tcell.ColorGray
					default:
						label += "  " + ui.Bytes(int64(c.Size))
					}
					n.AddChild(tview.NewTreeNode(label).SetReference(&treeRef{snap: ref.snap, path: p, node: c}).SetColor(color))
				}
				n.SetExpanded(true)
			})
		}()
	}
	u.tree.SetSelectedFunc(expand)
	expand(root)
	u.app.SetFocus(u.tree)
}

func ptr(i id.ID) *id.ID { return &i }

func (u *UI) restoreDialog(s *repository.Snapshot, path, label string) {
	_, repoName := u.snapRepo.GetCurrentOption()
	home, _ := os.UserHomeDir()
	form := tview.NewForm()
	form.AddInputField("Target directory", filepath.Join(home, "herakles-restore"), 60, nil, nil)
	form.AddDropDown("Existing files", []string{"always", "if-changed", "never"}, 0, nil)
	form.AddInputField("Include patterns (;)", "", 50, nil, nil)
	form.AddCheckbox("Verify after restore", true, nil)
	form.AddButton("Restore", func() {
		target := form.GetFormItemByLabel("Target directory").(*tview.InputField).GetText()
		_, ow := form.GetFormItemByLabel("Existing files").(*tview.DropDown).GetCurrentOption()
		inc := splitList(form.GetFormItemByLabel("Include patterns (;)").(*tview.InputField).GetText())
		verify := form.GetFormItemByLabel("Verify after restore").(*tview.Checkbox).IsChecked()
		u.closeModal("restore")
		u.background("Restore", func() (string, error) {
			r, err := u.openRepo(repoName)
			if err != nil {
				return "", err
			}
			var errs []string
			res, err := restorer.Restore(u.ctx, r, s, path, restorer.Options{Target: config.ExpandHome(target), Overwrite: ow, Includes: inc, Verify: verify,
				OnError: func(p string, err error) {
					if len(errs) < 5 {
						errs = append(errs, p+": "+err.Error())
					}
				}})
			if err != nil {
				return "", err
			}
			msg := fmt.Sprintf("Restored %d files and %d directories (%s) to %s", res.Files, res.Dirs, ui.Bytes(res.Bytes), target)
			if res.Skipped > 0 {
				msg += fmt.Sprintf("\nSkipped %d existing files", res.Skipped)
			}
			if res.Errors > 0 {
				msg += fmt.Sprintf("\n[red]%d errors:\n%s", res.Errors, strings.Join(errs, "\n"))
			}
			return msg, nil
		})
	})
	form.AddButton("Cancel", func() { u.closeModal("restore") })
	form.SetCancelFunc(func() { u.closeModal("restore") })
	form.SetBorder(true).SetTitle(" Restore " + label + " ")
	u.modal("restore", form, 80, 13)
}

// ---------- YAML editor ----------

func (u *UI) buildYAML() tview.Primitive {
	u.yaml = tview.NewTextArea()
	u.yaml.SetBorder(true)
	u.yaml.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		switch ev.Key() {
		case tcell.KeyCtrlS:
			c, err := config.Parse([]byte(u.yaml.GetText()))
			if err != nil {
				u.errorf("Not saved — configuration invalid:\n%v", err)
				return nil
			}
			c.SetPath(u.eng.Config().Path())
			// Write the text verbatim so comments are preserved.
			if err := os.MkdirAll(filepath.Dir(c.Path()), 0o700); err != nil {
				u.errorf("%v", err)
				return nil
			}
			if err := os.WriteFile(c.Path(), []byte(u.yaml.GetText()), 0o600); err != nil {
				u.errorf("%v", err)
				return nil
			}
			u.eng.SetConfig(c)
			if cl, err := daemon.NewClient(c); err == nil {
				_ = cl.Reload()
			}
			u.message("Saved", "Configuration saved to "+c.Path())
			return nil
		case tcell.KeyCtrlR:
			u.loadYAML()
			return nil
		case tcell.KeyEscape:
			u.show("dashboard")
			return nil
		}
		return ev
	})
	return u.yaml
}

func (u *UI) loadYAML() {
	p := u.eng.Config().Path()
	data, err := os.ReadFile(p)
	if err != nil {
		data, _ = u.eng.Config().Marshal()
	}
	u.yaml.SetText(string(data), false)
	u.yaml.SetTitle(" " + p + " ")
	u.app.SetFocus(u.yaml)
}

// ---------- logs ----------

func (u *UI) buildLogs() tview.Primitive {
	u.logView = tview.NewTextView().SetDynamicColors(true).SetScrollable(true)
	u.logView.SetBorder(true).SetTitle(" Log ")
	return u.logView
}

func (u *UI) refreshLogs() {
	var b strings.Builder
	for _, l := range u.logs.Lines() {
		color := "[white]"
		switch l.Level {
		case "WARN":
			color = "[yellow]"
		case "ERROR":
			color = "[red]"
		}
		fmt.Fprintf(&b, "[gray]%s[-] %s%-5s[-] %s [gray]%s[-]\n", ui.Time(l.Time), color, l.Level, tview.Escape(l.Message), tview.Escape(l.Attrs))
	}
	u.logView.SetText(b.String())
	u.logView.ScrollToEnd()
}
