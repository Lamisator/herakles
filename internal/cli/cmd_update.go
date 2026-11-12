package cli

import (
	"bufio"
	"context"
	"errors"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/lamisator/herakles/internal/daemon"
	"github.com/lamisator/herakles/internal/sysutil"
	"github.com/lamisator/herakles/internal/update"
)

func (a *App) updateCmd() *cobra.Command {
	var checkOnly, yes bool
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Look for a new Herakles version and install it",
		Long: `Look for a new Herakles release and, after asking, install it the way
Herakles was installed: with the package manager (apt, dnf, zypper,
pacman), with the Windows setup, or by replacing the program file. The
release announcement must be signed with the Herakles release key and every
download must match its checksum.

A running system service is restarted with the new version (the package or
the setup does it; for a replaced file this command does). The tray icon
offers the same update in its menu.`,
		Example: "  herakles update --check\n  sudo herakles update\n  sudo herakles update --yes",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Minute)
			defer cancel()
			rel, err := update.Check(ctx, a.cfg.Updates.URL)
			if err != nil {
				return err
			}
			if !update.Newer(rel.Version, Version) {
				a.printf("Herakles %s is up to date (latest release: %s)\n", Version, rel.Version)
				return nil
			}
			a.printf("Herakles %s is available (this is %s)", rel.Version, Version)
			if rel.Notes != "" {
				a.printf(": %s", rel.Notes)
			}
			a.printf("\n")
			if checkOnly {
				return nil
			}
			m := update.Detect()
			if !m.CanInstall() {
				return errors.New(m.Hint)
			}
			a.printf("This %s.\n", m.Hint)
			if !yes {
				if !a.isTTY() {
					return errors.New("add --yes to install without asking")
				}
				a.printf("Install it now? [Y/n] ")
				line, _ := bufio.NewReader(a.in).ReadString('\n')
				if l := strings.ToLower(strings.TrimSpace(line)); l != "" && l != "y" && l != "yes" && l != "j" {
					return nil
				}
			}
			if err := update.Install(ctx, rel, m); err != nil {
				return err
			}
			a.printf("installed Herakles %s\n", rel.Version)
			if m.Kind == "file" {
				a.restartServiceAfterUpdate()
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&checkOnly, "check", false, "only look for a new version")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "install without asking")
	return cmd
}

// restartServiceAfterUpdate restarts the system service so it runs the
// replaced program file (packages and the setup do this themselves).
func (a *App) restartServiceAfterUpdate() {
	if !sysutil.Elevated() {
		if cl, err := daemon.NewClient(a.cfg); err == nil {
			if _, err := cl.Status(); err == nil {
				a.printf("Restart the running Herakles daemon to use the new version (sudo herakles daemon restart, or quit and start the tray).\n")
			}
		}
		return
	}
	if runtime.GOOS == "windows" {
		if st, _ := daemon.ServiceControl("status", "", false); st == "running" {
			if _, err := daemon.ServiceControl("restart", "", false); err != nil {
				a.printf("restart the service: %v\n", err)
				return
			}
			a.printf("restarted the Herakles service\n")
		}
		return
	}
	if exec.Command("systemctl", "is-active", "--quiet", "herakles.service").Run() == nil {
		if out, err := exec.Command("systemctl", "restart", "herakles.service").CombinedOutput(); err != nil {
			a.printf("systemctl restart herakles: %v: %s\n", err, out)
			return
		}
		a.printf("restarted herakles.service\n")
	}
}
