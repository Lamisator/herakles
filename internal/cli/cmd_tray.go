package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/lamisator/herakles/internal/config"
	"github.com/lamisator/herakles/internal/daemon"
	"github.com/lamisator/herakles/internal/gui"
	"github.com/lamisator/herakles/internal/sysutil"
	"github.com/lamisator/herakles/internal/tray"
	"github.com/lamisator/herakles/internal/update"
)

func (a *App) trayCmd() *cobra.Command {
	var openNow bool
	var after int
	cmd := &cobra.Command{
		Use:   "tray",
		Short: "Run the daemon with the GUI and a system tray icon",
		Long: `Run scheduled backups like 'herakles daemon run', serve the GUI and show a
tray icon. Its menu opens the GUI or quits Herakles; the icon shows whether
a backup is running, a repository needs its password or a backup failed.

Unlike the system service, the tray mode can unlock repositories that have no
password source: enter the password once in the GUI and scheduled backups
use it until Herakles quits.

Starting it a second time opens the window of the running instance.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if after > 0 {
				waitForExit(after)
			}
			update.CleanOld()
			if a.startTrayUnit() {
				// The unit runs the tray; open its window once it is up.
				if openNow {
					return a.openRunningTray(cmd.Context())
				}
				return nil
			}
			err := a.runTray(cmd.Context(), openNow)
			if err == nil && restartForUpdate && inTrayUnit() {
				// systemd starts the new version (RestartForceExitStatus).
				return exitError{code: exitRestart}
			}
			return err
		},
	}
	cmd.Flags().BoolVar(&openNow, "open", false, "open the GUI window right away")
	cmd.Flags().IntVar(&after, "after", 0, "start once this process has ended (used by updates)")
	_ = cmd.Flags().MarkHidden("after")
	autostart := &cobra.Command{
		Use:   "autostart [on|off]",
		Short: "Start the tray when you log in (sets tray.autostart)",
		Long: `Set tray.autostart in the configuration and apply it: with "on", Herakles
starts in the tray when you log in, and "Herakles Backup" is added to the
applications menu (Linux: XDG autostart; Windows: the Run registry key).
Without an argument, show the current setting. Saving the option in the
GUI, TUI or YAML editor has the same effect.`,
		Args:      cobra.MaximumNArgs(1),
		ValidArgs: []string{"on", "off"},
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				state := "off"
				if a.cfg.Tray.Autostart {
					state = "on"
				}
				a.printf("tray autostart is %s\n", state)
				return nil
			}
			if args[0] != "on" && args[0] != "off" {
				return fmt.Errorf("use 'on' or 'off', not %q", args[0])
			}
			on := args[0] == "on"
			// Start from the file, not a.cfg, which may hold command line
			// credential overrides.
			c, err := config.Load(a.cfg.Path())
			switch {
			case errors.Is(err, fs.ErrNotExist):
				c = config.New(a.cfg.Path())
			case err != nil:
				return err
			}
			c.Tray.Autostart = on
			if err := c.Save(); err != nil {
				return err
			}
			changed, err := tray.Apply(c)
			for _, f := range changed {
				a.printf("updated %s\n", f)
			}
			if err != nil {
				return err
			}
			if cl, err := daemon.NewClient(c); err == nil {
				_ = cl.Reload()
			}
			if on {
				a.printf("Herakles starts in the tray when you log in; run 'herakles tray' to start it now.\n")
			} else {
				a.printf("tray autostart is off\n")
			}
			return nil
		},
	}
	cmd.AddCommand(autostart)
	return cmd
}

// openWindow is replaced in tests.
var openWindow = gui.OpenWindow

// guiURLPath stores the running tray's GUI URL (with its session token) so
// a second start can open that window instead of failing.
func guiURLPath(a *App) string { return filepath.Join(a.cfg.GetStateDir(), "gui.url") }

func (a *App) runTray(parent context.Context, openNow bool) (err error) {
	if tray.DetachConsole() {
		// Started from the Start menu or at login: there is no console, so
		// messages go to a log file and a failure to a message box.
		if f := a.trayLog(); f != nil {
			defer f.Close()
		}
		defer func() {
			if err != nil {
				a.eng.Log.Error("tray stopped", "err", err)
				tray.ErrorBox("Herakles", "Herakles could not start:\n\n"+err.Error())
			}
		}()
	}
	if lnk, err := tray.EnsureMenuEntry(a.cfg.GetStateDir()); err != nil {
		a.eng.Log.Warn("no Start menu entry", "err", err)
	} else if lnk != "" {
		a.eng.Log.Info("added Herakles to the Start menu", "shortcut", lnk)
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	if sh, err := a.waitSharedDesktop(ctx); err == nil {
		return a.runServiceTray(ctx, sh, openNow)
	}
	d, closer, err := a.newDaemon(ctx, func(c *config.Config) { tray.ApplyLogged(c, a.eng.Log) })
	if err != nil {
		return err
	}
	defer closer.Close()
	if err := d.Listen(); err != nil {
		if url := a.runningGUI(); url != "" {
			a.printf("Herakles is already running; opening its window.\n")
			return openWindow(url)
		}
		return fmt.Errorf("%w (is another Herakles daemon running? see 'herakles daemon status')", err)
	}
	tray.ApplyLogged(a.cfg, a.eng.Log)
	for _, name := range a.eng.Locked() {
		a.eng.Log.Warn("repository needs its password before scheduled backups can run; unlock it in the GUI", "repo", name)
	}
	upd := a.newUpdater(a.cfg, func() bool { return len(d.Status().Active) > 0 }, cancel)
	opts := gui.Options{Listen: a.cfg.GUI.Listen, Version: Version, Runner: d.Runner, Logs: d.Logs}
	if upd != nil {
		go upd.chk.Run(ctx)
		opts.Update, opts.InstallUpdate = upd.state, upd.install
	}
	inst, err := gui.Start(ctx, a.eng, opts)
	if err != nil {
		return err
	}
	urlFile := guiURLPath(a)
	if err := os.MkdirAll(filepath.Dir(urlFile), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(urlFile, []byte(inst.URL+"\n"), 0o600); err != nil {
		a.eng.Log.Warn("cannot record GUI address", "err", err)
	}
	defer os.Remove(urlFile)

	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	open := func() {
		if err := openWindow(inst.URL); err != nil {
			a.eng.Log.Error("cannot open the GUI window", "err", err, "url", inst.URL)
			_ = tray.Notify("Herakles window could not open", err.Error())
		}
	}
	if openNow {
		open()
	}
	status := func() tray.Status { return tray.Summarize(d.Status(), a.eng.Locked(), time.Now()) }
	warn := func(msg string) { a.eng.Log.Warn(msg, "gui", inst.URL) }
	topts := tray.Options{Status: status, Open: open, Quit: cancel, Warn: warn}
	if upd != nil {
		topts.Update, topts.Install = upd.label, func() { _ = upd.install() }
	}
	err = tray.Run(ctx, topts)
	if err != nil {
		// No tray available: keep running as a plain daemon with the GUI.
		fmt.Fprintf(a.err, "warning: no tray icon: %v\nThe daemon keeps running; the GUI is at %s\nPress Ctrl+C to quit.\n", err, inst.URL)
		<-ctx.Done()
	}
	cancel()
	runErr := <-done
	_ = inst.Shutdown()
	return runErr
}

// runningGUI returns the GUI URL of a running tray instance, if it answers.
func (a *App) runningGUI() string {
	b, err := os.ReadFile(guiURLPath(a))
	if err != nil {
		return ""
	}
	url := strings.TrimSpace(string(b))
	c := &http.Client{Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Get(url)
	if err != nil {
		return ""
	}
	resp.Body.Close()
	if resp.StatusCode >= 400 {
		return ""
	}
	return url
}

// waitSharedDesktop returns the system service's GUI if it is shared with
// this user. At login and right after an update the service may still be
// starting (it shares its GUI once it runs): while it is installed, wait
// for it rather than starting Herakles as this user, whose daemon would
// then take the service's control port.
func (a *App) waitSharedDesktop(ctx context.Context) (*daemon.DesktopShare, error) {
	sh, err := daemon.SharedDesktop()
	if installed, _ := daemon.SystemServiceState(); err == nil || !installed {
		return sh, err
	}
	a.eng.Log.Info("waiting for the Herakles system service")
	running := 0
	for deadline := time.Now().Add(2 * time.Minute); time.Now().Before(deadline); {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(2 * time.Second):
		}
		if sh, err = daemon.SharedDesktop(); err == nil {
			return sh, nil
		}
		// Running for a while without sharing its GUI with this user.
		if _, up := daemon.SystemServiceState(); up {
			if running++; running >= 5 {
				break
			}
		}
	}
	a.eng.Log.Info("the system service does not share its window with this user; running Herakles as this user")
	return nil, err
}

// exitRestart asks the tray's systemd unit to start the tray again.
const exitRestart = 75

// restartForUpdate is set when the tray ends for an update.
var restartForUpdate bool

// openRunningTray opens the window of the tray that is starting or running
// (the system service's when it is shared, else the tray's own).
func (a *App) openRunningTray(ctx context.Context) error {
	for end := time.Now().Add(30 * time.Second); time.Now().Before(end); {
		if sh, err := daemon.SharedDesktop(); err == nil {
			u, err := sh.OpenURL()
			if err != nil {
				return err
			}
			return openWindow(u)
		}
		if url := a.runningGUI(); url != "" {
			return openWindow(url)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return errors.New("the Herakles tray started but its window does not answer; see: journalctl --user -u " + "herakles-tray")
}

// trayLog sends the tray's messages to tray.log in the state directory
// (the previous run's file is kept as tray.log.1).
func (a *App) trayLog() *os.File {
	dir := a.cfg.GetStateDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil
	}
	p := filepath.Join(dir, "tray.log")
	_ = os.Rename(p, p+".1")
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil
	}
	a.err = f
	a.eng.Log = slog.New(slog.NewTextHandler(f, &slog.HandlerOptions{Level: slog.LevelInfo}))
	a.eng.Log.Info("herakles tray started", "version", Version, "pid", os.Getpid())
	return f
}

// trayPIDPath marks a running tray of the system service (one per user).
func trayPIDPath(a *App) string { return filepath.Join(a.cfg.GetStateDir(), "tray.pid") }

// runServiceTray shows the tray icon for the system service shared with this
// user (gui.users): backups run in the service, the icon shows its status
// and opens its window.
func (a *App) runServiceTray(ctx context.Context, sh *daemon.DesktopShare, openNow bool) error {
	open := func() {
		u, err := sh.OpenURL()
		if err == nil {
			err = openWindow(u)
		}
		if err != nil {
			fmt.Fprintf(a.err, "cannot open the Herakles window: %v\n", err)
			_ = tray.Notify("Herakles window could not open", err.Error())
		}
	}
	pidFile := trayPIDPath(a)
	if b, err := os.ReadFile(pidFile); err == nil {
		if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && pid != os.Getpid() && sysutil.ProcessIsHerakles(pid) {
			open()
			return nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(pidFile), 0o700); err == nil {
		_ = os.WriteFile(pidFile, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600)
		defer os.Remove(pidFile)
	}
	if len(a.cfg.Jobs) > 0 {
		fmt.Fprintf(a.err, "note: the jobs in %s do not run; the Herakles system service runs the backups\n", a.cfg.Path())
	}
	if openNow {
		open()
	}
	status := func() tray.Status {
		st, err := sh.Status()
		if err != nil {
			return tray.Status{State: tray.Failed, Line: "System service not reachable"}
		}
		return tray.Summarize(*st, nil, time.Now())
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	hidden := func() {
		_ = tray.Notify("Herakles icon hidden", "Backups keep running in the system service. Open Herakles from the applications menu to show the icon again.")
	}
	topts := tray.Options{Status: status, Open: open, Quit: hidden, QuitLabel: "Hide icon", QuitHint: "Remove the tray icon; backups keep running in the system service",
		Warn: func(msg string) { fmt.Fprintln(a.err, "warning:", msg) }}
	// The update replaces the program the service runs too; the package or
	// the setup restarts the service.
	busy := func() bool { st, err := sh.Status(); return err == nil && len(st.Active) > 0 }
	if upd := a.newUpdater(a.cfg, busy, cancel); upd != nil {
		go upd.chk.Run(ctx)
		topts.Update, topts.Install = upd.label, func() { _ = upd.install() }
	}
	err := tray.Run(ctx, topts)
	if err != nil {
		if !openNow {
			open()
		}
		return nil
	}
	return nil
}
