package cli

import (
	"errors"
	"os"

	"github.com/spf13/cobra"

	"github.com/lamisator/herakles/internal/gui"
	"github.com/lamisator/herakles/internal/tui"
	"github.com/lamisator/herakles/internal/window"
)

// windowCmd shows a GUI address in a native window; the tray and 'gui'
// start it as a process of its own.
func (a *App) windowCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "window [URL]",
		Short:  "Show a Herakles GUI address in a window of its own",
		Hidden: true,
		Args:   cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			url := os.Getenv(window.URLEnv)
			if len(args) == 1 {
				url = args[0]
			}
			if url == "" {
				return errors.New("no address to show")
			}
			return window.Show(url)
		},
	}
}

func (a *App) tuiCmd() *cobra.Command {
	var wizard bool
	cmd := &cobra.Command{
		Use:   "tui",
		Short: "Interactive terminal interface (dashboard, configuration, browse & restore)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return tui.Run(cmd.Context(), a.eng, Version, wizard)
		},
	}
	cmd.Flags().BoolVar(&wizard, "wizard", false, "start with the backup wizard")
	return cmd
}

func (a *App) guiCmd() *cobra.Command {
	var listen string
	var noBrowser bool
	cmd := &cobra.Command{
		Use:   "gui",
		Short: "Graphical desktop interface (opens in a window of its own)",
		Long: `Start the graphical interface. It is served locally (127.0.0.1 only,
protected by a random session token) and shown in a window of its own:
WebView2 on Windows, WebKitGTK (herakles-window, libwebkit2gtk-4.1) on Linux.
Without those it opens in a Chromium/Edge app window or the default browser;
HERAKLES_BROWSER=1 always uses the browser.

If the system service is shared with you ('sudo herakles daemon desktop on'),
this opens the service's window instead.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if listen == "" && !noBrowser {
				// The system service is shared with this user: open it.
				if ok, err := openShared(); ok {
					return err
				}
			}
			if listen == "" {
				listen = a.cfg.GUI.Listen
			}
			open := !noBrowser
			if a.cfg.GUI.OpenBrowser != nil && !*a.cfg.GUI.OpenBrowser {
				open = false
			}
			return gui.Run(cmd.Context(), a.eng, gui.Options{Listen: listen, OpenBrowser: open, Version: Version, Out: a.err})
		},
	}
	cmd.Flags().StringVar(&listen, "listen", "", "listen address (default 127.0.0.1:7478)")
	cmd.Flags().BoolVar(&noBrowser, "no-browser", false, "do not open a window; print the URL")
	return cmd
}
