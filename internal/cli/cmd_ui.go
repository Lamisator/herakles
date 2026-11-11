package cli

import (
	"github.com/spf13/cobra"

	"github.com/lamisator/herakles/internal/gui"
	"github.com/lamisator/herakles/internal/tui"
)

func (a *App) tuiCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "tui",
		Short: "Interactive terminal interface (dashboard, configuration, browse & restore)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return tui.Run(cmd.Context(), a.eng, Version)
		},
	}
}

func (a *App) guiCmd() *cobra.Command {
	var listen string
	var noBrowser bool
	cmd := &cobra.Command{
		Use:   "gui",
		Short: "Graphical desktop interface (opens in an app window / browser)",
		Long: `Start the graphical interface. It is served locally (127.0.0.1 only,
protected by a random session token) and opened in a Chromium/Edge app window
or the default browser. Works on Linux and Windows without extra runtime.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
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
