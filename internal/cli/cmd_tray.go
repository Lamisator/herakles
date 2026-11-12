package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/lamisator/herakles/internal/config"
	"github.com/lamisator/herakles/internal/daemon"
	"github.com/lamisator/herakles/internal/gui"
	"github.com/lamisator/herakles/internal/tray"
)

func (a *App) trayCmd() *cobra.Command {
	var openNow bool
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
			return a.runTray(cmd.Context(), openNow)
		},
	}
	cmd.Flags().BoolVar(&openNow, "open", false, "open the GUI window right away")
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

func (a *App) runTray(parent context.Context, openNow bool) error {
	tray.DetachConsole()
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	if sh, err := daemon.SharedDesktop(); err == nil {
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
	inst, err := gui.Start(ctx, a.eng, gui.Options{Listen: a.cfg.GUI.Listen, Version: Version, Runner: d.Runner, Logs: d.Logs})
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
		}
	}
	if openNow {
		open()
	}
	status := func() tray.Status { return tray.Summarize(d.Status(), a.eng.Locked(), time.Now()) }
	warn := func(msg string) { a.eng.Log.Warn(msg, "gui", inst.URL) }
	err = tray.Run(ctx, tray.Options{Status: status, Open: open, Quit: cancel, Warn: warn})
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
		}
	}
	pidFile := trayPIDPath(a)
	if b, err := os.ReadFile(pidFile); err == nil {
		if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && pid != os.Getpid() && processAlive(pid) {
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
	err := tray.Run(ctx, tray.Options{Status: status, Open: open, Quit: func() {}, QuitLabel: "Hide icon", QuitHint: "Remove the tray icon; backups keep running in the system service",
		Warn: func(msg string) { fmt.Fprintln(a.err, "warning:", msg) }})
	if err != nil {
		if !openNow {
			open()
		}
		return nil
	}
	return nil
}

// processAlive reports whether pid runs (Unix; elsewhere always false).
func processAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	return err == nil && p.Signal(syscall.Signal(0)) == nil
}
