package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"slices"
	"sync"

	"github.com/spf13/cobra"

	"github.com/lamisator/herakles/internal/config"
	"github.com/lamisator/herakles/internal/daemon"
	"github.com/lamisator/herakles/internal/gui"
	"github.com/lamisator/herakles/internal/hub"
	"github.com/lamisator/herakles/internal/sysutil"
)

// desktopShare serves the GUI of the system service for the users in
// gui.users and keeps their share files in daemon.DesktopDir up to date.
type desktopShare struct {
	a    *App
	d    *daemon.Daemon
	ctx  context.Context
	mu   sync.Mutex
	inst *gui.Instance
	// agent reports the hub connection, once started.
	agent func() *hub.AgentState
}

// update starts the GUI when the first user is added and rewrites the share
// files; it runs at start and on every reload.
func (s *desktopShare) update(cfg *config.Config) {
	s.mu.Lock()
	defer s.mu.Unlock()
	log := s.a.eng.Log
	if len(cfg.GUI.Users) == 0 {
		daemon.RemoveDesktopShares(nil)
		return
	}
	if !daemon.CanShareDesktop() {
		log.Warn("gui.users only works for the system service (Linux as root, Windows); ignored")
		return
	}
	if s.inst == nil {
		inst, err := gui.Start(s.ctx, s.a.eng, gui.Options{Listen: cfg.GUI.Listen, Version: Version, Runner: s.d.Runner, Logs: s.d.Logs, Service: true, Agent: s.agent})
		if err != nil {
			log.Error("desktop GUI not started", "err", err)
			return
		}
		s.inst = inst
		log.Info("desktop GUI started", "address", inst.Base, "users", fmt.Sprint(cfg.GUI.Users))
	}
	host, _ := os.Hostname()
	keep := map[string]bool{}
	for _, name := range cfg.GUI.Users {
		u, err := user.Lookup(name)
		if err != nil {
			log.Warn("gui.users: unknown user", "user", name)
			continue
		}
		if u.Uid == "0" || u.Uid == sysutil.SystemSID {
			continue
		}
		if err := daemon.WriteDesktopShare(u, daemon.DesktopShare{GUI: s.inst.Base, Token: s.inst.Token, Version: Version, Host: host}); err != nil {
			log.Error("cannot share the GUI", "user", name, "err", err)
			continue
		}
		keep[u.Uid] = true
	}
	daemon.RemoveDesktopShares(keep)
}

func (s *desktopShare) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	daemon.RemoveDesktopShares(nil)
	if s.inst != nil {
		_ = s.inst.Shutdown()
		s.inst = nil
	}
}

// openShared opens the system service's GUI if it is shared with this user.
func openShared() (bool, error) {
	sh, err := daemon.SharedDesktop()
	if err != nil {
		return false, nil
	}
	u, err := sh.OpenURL()
	if err != nil {
		return true, err
	}
	return true, openWindow(u)
}

func (a *App) daemonDesktopCmd() *cobra.Command {
	var names []string
	var noStart bool
	cmd := &cobra.Command{
		Use:       "desktop [on|off]",
		Short:     "Use the system service from your desktop (whole disks, system files)",
		ValidArgs: []string{"on", "off"},
		Long: `Let the system service, which runs as root (LocalSystem on Windows), serve
the Herakles window for local users (gui.users). Their tray icon and "Herakles" menu entry then open
the service instead of running Herakles as that user, so backups can read
whole disks and every file, and run without anyone logged in.

"on" adds the user who ran sudo (or --user), creates the configuration if
needed, and enables and starts the service. On Windows the default user is
the one logged in to the session, and the service is installed if needed.
"off" removes the user. Without
an argument, show who it is shared with. Shared users get full control over
the service, restores included: only add people who may be root here.`,
		Example: "  sudo herakles daemon desktop on\n  sudo herakles daemon desktop on --user alice\n  sudo herakles daemon desktop off",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				if len(a.cfg.GUI.Users) == 0 {
					a.printf("the system service is not shared with any desktop user\n")
				} else {
					a.printf("shared with: %v\n", a.cfg.GUI.Users)
				}
				return nil
			}
			if args[0] != "on" && args[0] != "off" {
				return fmt.Errorf("use 'on' or 'off', not %q", args[0])
			}
			if runtime.GOOS != "linux" && runtime.GOOS != "windows" {
				return errors.New("only available on Linux and Windows")
			}
			if !sysutil.Elevated() {
				if runtime.GOOS == "windows" {
					return errors.New("run it in a terminal opened as administrator")
				}
				return errors.New("run it as root: sudo herakles daemon desktop " + args[0])
			}
			if len(names) == 0 {
				if su := desktopUser(); su != "" {
					names = []string{su}
				} else {
					return errors.New("name the desktop user with --user")
				}
			}
			for _, n := range names {
				if _, err := user.Lookup(n); err != nil {
					return fmt.Errorf("unknown user %q", n)
				}
			}
			// Start from the file, not a.cfg, which may hold command line
			// credential overrides.
			c, err := config.Load(a.cfg.Path())
			switch {
			case errors.Is(err, fs.ErrNotExist):
				c = config.New(a.cfg.Path())
			case err != nil:
				return err
			}
			on := args[0] == "on"
			for _, n := range names {
				has := slices.Contains(c.GUI.Users, n)
				if on && !has {
					c.GUI.Users = append(c.GUI.Users, n)
				} else if !on && has {
					c.GUI.Users = slices.DeleteFunc(c.GUI.Users, func(x string) bool { return x == n })
				}
			}
			if err := c.Save(); err != nil {
				return err
			}
			a.printf("saved %s (gui.users: %v)\n", c.Path(), c.GUI.Users)
			if cl, err := daemon.NewClient(c); err == nil && cl.Reload() == nil {
				a.printf("the running service applied it\n")
			} else if on && !noStart {
				if err := startSystemService(c.Path()); err != nil {
					return err
				}
				a.printf("enabled and started the system service\n")
			}
			if on {
				a.printf("Open \"Herakles\" from the applications menu (or run 'herakles tray --open' as %s).\n", names[0])
				a.printf("Tip: 'herakles tray autostart on' (as %s) shows the tray icon after every login.\n", names[0])
				for _, n := range names {
					if jobs := userJobs(n); jobs > 0 {
						a.printf("Note: %s has %d job(s) in their own configuration; they do not run while the desktop uses the service. Recreate them in the Herakles window.\n", n, jobs)
					}
				}
			}
			return nil
		},
	}
	cmd.Flags().StringSliceVar(&names, "user", nil, "desktop user (default: the user who ran sudo)")
	cmd.Flags().BoolVar(&noStart, "no-start", false, "only change the configuration")
	return cmd
}

// desktopUser is the user who asked for the desktop: the one who ran sudo,
// or on Windows the one logged in at the screen.
func desktopUser() string {
	if su := os.Getenv("SUDO_USER"); su != "" && su != "root" {
		return su
	}
	if runtime.GOOS == "windows" {
		if u, err := sysutil.ConsoleUser(); err == nil {
			return u
		}
	}
	return ""
}

// startSystemService enables and starts the system service (installing it
// on Windows with the configuration file cfgPath).
func startSystemService(cfgPath string) error {
	if runtime.GOOS != "windows" {
		if out, err := exec.Command("systemctl", "enable", "--now", "herakles.service").CombinedOutput(); err != nil {
			return fmt.Errorf("systemctl enable --now herakles: %v: %s", err, out)
		}
		return nil
	}
	if !daemon.ServiceInstalled() {
		if _, err := daemon.ServiceControl("install", cfgPath, false); err != nil {
			return err
		}
	}
	if st, _ := daemon.ServiceControl("status", "", false); st == "running" {
		return nil
	}
	_, err := daemon.ServiceControl("start", "", false)
	return err
}

// userJobs counts the jobs in a user's own configuration.
func userJobs(name string) int {
	u, err := user.Lookup(name)
	if err != nil {
		return 0
	}
	p := filepath.Join(u.HomeDir, ".config", "herakles", "herakles.yaml")
	if runtime.GOOS == "windows" {
		p = filepath.Join(u.HomeDir, "AppData", "Roaming", "Herakles", "herakles.yaml")
	}
	c, err := config.Load(p)
	if err != nil {
		return 0
	}
	return len(c.Jobs)
}
