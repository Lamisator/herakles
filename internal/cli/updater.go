package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"time"

	"github.com/lamisator/herakles/internal/config"
	"github.com/lamisator/herakles/internal/sysutil"
	"github.com/lamisator/herakles/internal/tray"
	"github.com/lamisator/herakles/internal/update"
)

// updater finds new releases for the tray and its window and installs one
// when the user picks it (semi-automatic: nothing happens unasked).
type updater struct {
	a      *App
	chk    *update.Checker
	method update.Method
	// busy reports a running backup, which an update must not cut off.
	busy func() bool
	// restart ends this tray; the new version is started first.
	restart func()

	mu     sync.Mutex
	failed string
}

// newUpdater returns nil when checks are off or Version is none (a "dev"
// build).
func (a *App) newUpdater(cfg *config.Config, busy func() bool, restart func()) *updater {
	if !cfg.Updates.Enabled() || !update.Valid(Version) {
		return nil
	}
	u := &updater{a: a, method: update.Detect(), busy: busy, restart: restart}
	u.chk = &update.Checker{URL: cfg.Updates.URL, Current: Version, OnNew: u.announce}
	return u
}

func (u *updater) announce(rel *update.Release) {
	body := "Choose “Update to " + rel.Version + "” in the menu of the Herakles tray icon, or in the Herakles window."
	if !u.method.CanInstall() {
		body = u.method.Hint
	}
	if err := tray.Notify("Herakles "+rel.Version+" is available", body); err != nil {
		u.a.eng.Log.Info("new Herakles version available", "version", rel.Version)
	}
}

// label is the tray menu entry ("" hides it).
func (u *updater) label() string {
	rel := u.chk.Available()
	if rel == nil || !u.method.CanInstall() {
		return ""
	}
	if u.chk.State(u.method).Installing {
		return "Updating to " + rel.Version + "…"
	}
	return "Update to " + rel.Version
}

// state is what the window shows.
func (u *updater) state() update.State {
	st := u.chk.State(u.method)
	u.mu.Lock()
	if u.failed != "" {
		st.Error = u.failed
	}
	u.mu.Unlock()
	return st
}

// install runs the update and restarts Herakles.
func (u *updater) install() error {
	rel := u.chk.Available()
	if rel == nil {
		return errors.New("no update available")
	}
	if u.chk.State(u.method).Installing {
		return errors.New("the update is already running")
	}
	fail := func(err error) error {
		u.mu.Lock()
		u.failed = err.Error()
		u.mu.Unlock()
		u.chk.SetInstalling(false)
		_ = tray.Notify("Herakles was not updated", err.Error())
		u.a.eng.Log.Error("update failed", "version", rel.Version, "err", err)
		return err
	}
	if u.busy != nil && u.busy() {
		return fail(errors.New("a backup or other task is running; update when it is done"))
	}
	u.chk.SetInstalling(true)
	u.mu.Lock()
	u.failed = ""
	u.mu.Unlock()
	u.a.eng.Log.Info("installing update", "version", rel.Version, "method", u.method.Kind)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	if err := update.Install(ctx, rel, u.method); err != nil {
		return fail(err)
	}
	u.a.eng.Log.Info("update installed; restarting", "version", rel.Version)
	if err := u.startNew(); err != nil {
		return fail(fmt.Errorf("updated, but Herakles did not restart (%v); start it again from the menu", err))
	}
	u.restart()
	return nil
}

// startNew starts the new tray, which waits until this one has ended. In
// the tray's systemd unit, systemd starts it instead.
func (u *updater) startNew() error {
	if inTrayUnit() {
		restartForUpdate = true
		return nil
	}
	exe := u.method.Exe
	if exe == "" {
		var err error
		if exe, err = os.Executable(); err != nil {
			return err
		}
	}
	args := []string{}
	if u.a.configPath != "" {
		args = append(args, "--config", u.a.configPath)
	}
	args = append(args, "tray", "--open", "--after", strconv.Itoa(os.Getpid()))
	cmd := exec.Command(exe, args...)
	sysutil.Background(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// waitForExit waits (up to a minute) until the tray that started this one
// for an update has ended.
func waitForExit(pid int) {
	for i := 0; i < 240 && sysutil.ProcessAlive(pid); i++ {
		time.Sleep(250 * time.Millisecond)
	}
}
