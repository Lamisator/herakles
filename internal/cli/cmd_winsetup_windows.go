package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/lamisator/herakles/internal/config"
	"github.com/lamisator/herakles/internal/daemon"
	"github.com/lamisator/herakles/internal/hub"
	"github.com/lamisator/herakles/internal/sysutil"
)

// windowsSetupCmd is what the Windows installer (herakles-setup.exe) runs
// after copying the files, and its uninstaller before removing them.
func (a *App) windowsSetupCmd() *cobra.Command {
	root := &cobra.Command{
		Use:    "windows-setup",
		Short:  "Set up Herakles after installation (used by herakles-setup.exe)",
		Hidden: true,
	}
	var mode, userName, code, name string
	var autostart, addPath bool
	install := &cobra.Command{
		Use:   "install",
		Short: "Set up how Herakles is used on this computer",
		Long: `Modes:
  desktop  backups run in the Windows service (LocalSystem: every file, also
           without anyone logged in); the user's "Herakles" window and tray
           icon open the service. --hub attaches the service to a hub.
  user     Herakles runs as the user while they are logged in.
  none     nothing besides --path.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !sysutil.Elevated() {
				return errors.New("run it as administrator")
			}
			exe, err := os.Executable()
			if err != nil {
				return err
			}
			if addPath {
				if err := sysutil.SystemPath(filepath.Dir(exe), true); err != nil {
					return fmt.Errorf("PATH: %w", err)
				}
				a.printf("added %s to PATH\n", filepath.Dir(exe))
			}
			if mode == "none" {
				return nil
			}
			if mode != "desktop" && mode != "user" {
				return fmt.Errorf("unknown mode %q (desktop, user, none)", mode)
			}
			if code != "" && mode != "desktop" {
				return errors.New("only the service (mode desktop) can be attached to a hub")
			}
			if userName == "" {
				if userName, err = sysutil.ConsoleUser(); err != nil {
					return fmt.Errorf("cannot tell who is logged in (%v); name the user with --user", err)
				}
			}
			u, err := user.Lookup(userName)
			if err != nil {
				return fmt.Errorf("unknown user %q: %w", userName, err)
			}
			run := ""
			if mode == "desktop" {
				if err := a.setupService(cmd, u, code, name); err != nil {
					return err
				}
				run = winQuote(exe) + " tray"
			} else {
				appData, err := sysutil.UserAppData(u.Uid)
				if err != nil {
					return err
				}
				p := filepath.Join(appData, "Herakles", "herakles.yaml")
				c, err := loadOrNew(p)
				if err != nil {
					return err
				}
				if c.Tray.Autostart != autostart {
					c.Tray.Autostart = autostart
					if err := c.Save(); err != nil {
						return err
					}
				}
				// What 'herakles tray autostart on' writes for this file.
				run = winQuote(exe) + " --config " + winQuote(p) + " tray"
			}
			if !autostart {
				run = ""
			}
			if err := sysutil.SetUserRun(u.Uid, "Herakles", run); err != nil {
				return err
			}
			if run != "" {
				a.printf("tray icon starts when %s logs in\n", u.Username)
			}
			return nil
		},
	}
	f := install.Flags()
	f.StringVar(&mode, "mode", "desktop", "desktop, user or none")
	f.StringVar(&userName, "user", "", "desktop user (default: the user logged in to this session)")
	f.StringVar(&code, "hub", "", "attach the service to a hub with this enrollment code")
	f.StringVar(&name, "name", "", "name on the hub, if the code sets none")
	f.BoolVar(&autostart, "autostart", true, "start the tray icon when the user logs in")
	f.BoolVar(&addPath, "path", false, "add the installation folder to the system PATH")

	var purge bool
	uninstall := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove the service, autostart entries and PATH entry",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !sysutil.Elevated() {
				return errors.New("run it as administrator")
			}
			exe, err := os.Executable()
			if err != nil {
				return err
			}
			if daemon.ServiceInstalled() {
				_, _ = daemon.ServiceControl("stop", "", false)
				if _, err := daemon.ServiceControl("uninstall", "", false); err != nil {
					return err
				}
				a.printf("removed the service\n")
			}
			for _, sid := range sysutil.RemoveRunEverywhere("Herakles", filepath.Dir(exe)) {
				a.printf("removed the tray autostart of %s\n", sid)
			}
			if err := sysutil.SystemPath(filepath.Dir(exe), false); err != nil {
				a.printf("PATH: %v\n", err)
			}
			_ = os.RemoveAll(daemon.DesktopDir)
			if purge {
				for _, d := range []string{filepath.Dir(config.SystemPath()), systemProfileState()} {
					if err := os.RemoveAll(d); err != nil {
						a.printf("%v\n", err)
					}
				}
				a.printf("removed the service's configuration and state\n")
			}
			return nil
		},
	}
	uninstall.Flags().BoolVar(&purge, "purge", false, "also delete the service's configuration and state (repositories stay)")
	restart := &cobra.Command{
		Use:   "restart",
		Short: "Restart the service, if installed, so it runs the new version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !daemon.ServiceInstalled() {
				return nil
			}
			_, _ = daemon.ServiceControl("stop", "", false)
			if _, err := daemon.ServiceControl("start", "", false); err != nil {
				return err
			}
			a.printf("restarted the service\n")
			return nil
		},
	}
	root.AddCommand(install, uninstall, restart)
	return root
}

// setupService prepares the machine-wide configuration, shares the window
// with u, optionally joins a hub, and installs and starts the service.
func (a *App) setupService(cmd *cobra.Command, u *user.User, code, name string) error {
	p := config.SystemPath()
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// The configuration holds repository passwords: keep ordinary users out.
	if err := sysutil.SecureDir(dir); err != nil {
		return fmt.Errorf("protect %s: %w", dir, err)
	}
	c, err := loadOrNew(p)
	if err != nil {
		return err
	}
	if c.StateDir == "" {
		// A service installed by hand kept its state in LocalSystem's
		// profile; keep using it there.
		c.StateDir = filepath.Join(dir, "state")
		if fi, err := os.Stat(systemProfileState()); err == nil && fi.IsDir() {
			c.StateDir = systemProfileState()
		}
	}
	if !slices.ContainsFunc(c.GUI.Users, func(n string) bool { return strings.EqualFold(n, u.Username) }) {
		c.GUI.Users = append(c.GUI.Users, u.Username)
	}
	if err := c.Save(); err != nil {
		return err
	}
	a.printf("the Herakles window of %s opens the service (%s)\n", u.Username, p)
	if code != "" {
		hc, err := hub.ParseCode(code)
		if err != nil {
			return err
		}
		if cur, _ := hub.LoadCredentials(c.GetStateDir()); cur != nil && c.Agent.Hub != "" {
			a.printf("already attached to %s as %s; the code was not used\n", c.Agent.Hub, cur.Name)
		} else {
			creds, err := hub.Attach(cmd.Context(), c, hc, config.Agent{}, name, Version)
			if err == nil {
				err = c.Save()
			}
			if err != nil {
				return fmt.Errorf("hub: %w", err)
			}
			a.printf("attached to %s as %q\n", hc.URL, creds.Name)
		}
	}
	switch {
	case !daemon.ServiceInstalled():
		if _, err := daemon.ServiceControl("install", p, false); err != nil {
			return err
		}
		fallthrough
	default:
		if cl, err := daemon.NewClient(c); err == nil && cl.Reload() == nil {
			a.printf("the running service applied the changes\n")
			break
		}
		_, _ = daemon.ServiceControl("stop", "", false)
		if _, err := daemon.ServiceControl("start", "", false); err != nil {
			return err
		}
		a.printf("started the service\n")
	}
	// The installer opens the window next: wait until the service shares it,
	// or the tray would start a Herakles of the user's own.
	share := daemon.DesktopPath(u.Uid)
	for i := 0; i < 60; i++ {
		if _, err := os.Stat(share); err == nil {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return errors.New("the service did not share its window within 30 seconds; see the Windows event log")
}

// systemProfileState is where a service without state_dir keeps its state.
func systemProfileState() string {
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	return filepath.Join(root, "System32", "config", "systemprofile", "AppData", "Local", "Herakles")
}

func loadOrNew(p string) (*config.Config, error) {
	c, err := config.Load(p)
	if errors.Is(err, fs.ErrNotExist) {
		return config.New(p), nil
	}
	return c, err
}

func winQuote(s string) string { return syscall.EscapeArg(s) }
