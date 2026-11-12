//go:build !windows

package cli

import (
	"errors"

	"github.com/spf13/cobra"
)

// windowsSetupCmd exists only on Windows; see cmd_winsetup_windows.go.
func (a *App) windowsSetupCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "windows-setup",
		Hidden: true,
		RunE: func(*cobra.Command, []string) error {
			return errors.New("only used by the Windows installer")
		},
	}
}
