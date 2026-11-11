package cli

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

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
	install := &cobra.Command{
		Use:   "install",
		Short: "Start the tray at login and add Herakles to the applications menu",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			exe, err := os.Executable()
			if err != nil {
				return err
			}
			if exe, err = filepath.EvalSymlinks(exe); err != nil {
				return err
			}
			cfgPath, err := filepath.Abs(a.cfg.Path())
			if err != nil {
				return err
			}
			written, err := tray.Install(tray.Command{Exe: exe, Args: []string{"--config", cfgPath, "tray"}})
			for _, w := range written {
				a.printf("wrote %s\n", w)
			}
			if err == nil {
				a.printf("Herakles starts in the tray at your next login; run 'herakles tray' to start it now.\n")
			}
			return err
		},
	}
	uninstall := &cobra.Command{
		Use: "uninstall", Short: "Undo 'tray install'", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			removed, err := tray.Uninstall()
			for _, r := range removed {
				a.printf("removed %s\n", r)
			}
			return err
		},
	}
	cmd.AddCommand(install, uninstall)
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
	d, closer, err := a.newDaemon(ctx)
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
