package cli

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/lamisator/herakles/internal/config"
	"github.com/lamisator/herakles/internal/daemon"
	"github.com/lamisator/herakles/internal/gui"
	"github.com/lamisator/herakles/internal/hub"
	"github.com/lamisator/herakles/internal/ui"
)

// startAgent keeps the daemon connected to its hub (if attached; it
// notices 'herakles hub join' on the next reload).
func (a *App) startAgent(ctx context.Context, d *daemon.Daemon) *hub.Agent {
	var applyMu sync.Mutex
	ag := &hub.Agent{
		Config:  a.eng.Config,
		Version: Version,
		Log:     a.eng.Log,
		Handler: gui.NewRemote(ctx, a.eng, gui.Options{Version: Version, Runner: d.Runner, Logs: d.Logs, Service: true}),
	}
	ag.Apply = func(shared []hub.SharedRepository) (hub.SyncResult, error) {
		applyMu.Lock()
		defer applyMu.Unlock()
		c := a.eng.Config().Clone()
		res, err := hub.Apply(c, shared)
		if err != nil || !res.Changed() {
			return res, err
		}
		if err := c.Validate(); err != nil {
			return res, err
		}
		if err := c.Save(); err != nil {
			return res, err
		}
		a.eng.Log.Info("hub: repositories updated", "added", res.Added, "updated", res.Updated, "removed", res.Removed)
		return res, d.Reload()
	}
	d.AfterReload = ag.Wake
	go ag.Run(ctx)
	return ag
}

func (a *App) hubCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "hub",
		Short: "Manage servers centrally: attach this machine to a hub, or manage the hub's servers",
		Long: `A hub is a Herakles with the web interface (web section) that other
servers attach to. Their daemons keep a connection open to the hub's web
address, so they need no open port. In the hub's web interface, pick a
server at the top of the menu to set up its backups with the wizard, see
its disks, run, browse and restore, as if you were logged in there.

The repositories listed under hub.repositories are passed to every
attached server with their passwords, so the servers need no credentials
of their own. Pruning and checks of these repositories run on the hub.

On the hub:
  herakles hub enroll --name web-1      # one-time enrollment code, 24 hours
  herakles hub servers                  # attached servers

On the server to attach (as root, with the daemon running):
  herakles hub join HKE1-...

Whoever administers the hub controls the attached servers completely
(their backups run as root and can run hooks). Attach only servers the
hub's administrators may control.`,
	}
	cmd.AddCommand(a.hubJoinCmd(), a.hubLeaveCmd(), a.hubStatusCmd(), a.hubTokenCmd(), a.hubServersCmd(), a.hubRemoveCmd(), a.hubRenameCmd())
	return cmd
}

// reloadDaemon asks a running daemon to read the configuration again.
func (a *App) reloadDaemon() string {
	c, err := daemon.NewClient(a.cfg)
	if err == nil {
		err = c.Reload()
	}
	if err != nil {
		return "the daemon is not running; start it with 'herakles daemon start' (or install it with 'herakles daemon install')"
	}
	return "the daemon connects now"
}

func (a *App) hubJoinCmd() *cobra.Command {
	var caCert, name string
	var insecure bool
	c := &cobra.Command{
		Use:   "join ENROLLMENT-CODE",
		Short: "Attach this machine to a hub (code from the hub's Servers page)",
		Long: `Attach this machine to a hub with an enrollment code. The code (HKE1-...)
holds everything needed: the hub's address, a one-time secret, the hub's
identity key and, for a hub serving TLS itself, its certificate pin.

This machine makes its own key pair; the hub keeps only the public half.
Every answer from the hub must be signed with the identity key from the
code, so a server cannot be attached to an impostor.`,
		Args:    cobra.ExactArgs(1),
		Example: `  sudo herakles hub join HKE1-eyJ1Ijoi...`,
		RunE: func(cmd *cobra.Command, args []string) error {
			code, err := hub.ParseCode(args[0])
			if err != nil {
				return err
			}
			c := a.cfg.Clone()
			creds, err := hub.Attach(cmd.Context(), c, code, config.Agent{CACert: caCert, InsecureSkipVerify: insecure}, name, Version)
			if err != nil {
				return err
			}
			if err := c.Save(); err != nil {
				return err
			}
			a.cfg = c
			a.printf("attached to %s as %q (hub key %s)\n%s\n", code.URL, creds.Name, hub.KeyFingerprint(code.HubKey), a.reloadDaemon())
			return nil
		},
	}
	c.Flags().StringVar(&name, "name", "", "name on the hub, if the code sets none (default: the host name)")
	c.Flags().StringVar(&caCert, "ca-cert", "", "trust the hub's certificate from this CA file")
	c.Flags().BoolVar(&insecure, "insecure-skip-verify", false, "do not check the hub's TLS certificate (the hub key is still checked)")
	return c
}

func (a *App) hubLeaveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "leave",
		Short: "Detach this machine from its hub; its repositories stay as ordinary ones",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if a.cfg.Agent.Hub == "" {
				return errors.New("this machine is not attached to a hub")
			}
			c := a.cfg.Clone()
			hubURL := c.Agent.Hub
			kept, err := hub.Detach(c)
			if err != nil {
				return err
			}
			if err := c.Save(); err != nil {
				return err
			}
			a.cfg = c
			a.printf("detached from %s\n", hubURL)
			if len(kept) > 0 {
				a.printf("the hub's repositories stay configured here: %v\n", kept)
			}
			a.printf("remove this server on the hub too (Servers page or 'herakles hub remove')\n")
			if cl, err := daemon.NewClient(a.cfg); err == nil {
				_ = cl.Reload()
			}
			return nil
		},
	}
}

func (a *App) hubStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show whether this machine is attached to a hub and connected",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			creds, err := hub.LoadCredentials(a.cfg.GetStateDir())
			if err != nil {
				return err
			}
			var st *daemon.Status
			if c, err := daemon.NewClient(a.cfg); err == nil {
				st, _ = c.Status()
			}
			if a.jsonOut {
				out := map[string]any{"hub": a.cfg.Agent.Hub}
				if creds != nil {
					out["name"], out["id"] = creds.Name, creds.ID
				}
				if st != nil {
					out["connection"], out["servers_online"] = st.Agent, st.Hub
				}
				return a.printJSON(out)
			}
			if a.cfg.Agent.Hub == "" || creds == nil {
				a.printf("not attached to a hub\n")
			} else {
				a.printf("attached to %s as %q\n", a.cfg.Agent.Hub, creds.Name)
				switch {
				case st == nil:
					a.printf("the daemon is not running, so there is no connection\n")
				case st.Agent == nil:
					a.printf("the daemon has not picked it up yet; run 'herakles daemon reload'\n")
				default:
					if m, ok := st.Agent.(map[string]any); ok {
						if m["connected"] == true {
							a.printf("connected since %v\n", m["since"])
						} else {
							a.printf("not connected: %v\n", m["error"])
						}
					}
				}
			}
			if len(a.cfg.Hub.Repositories) > 0 || (st != nil && len(st.Hub) > 0) {
				a.printf("\nthis machine is a hub; shared repositories: %v\n", a.cfg.Hub.Repositories)
				if st != nil {
					a.printf("servers connected: %d (see 'herakles hub servers')\n", len(st.Hub))
				}
			}
			return nil
		},
	}
}

func (a *App) hubStore() *hub.Store { return hub.OpenStore(hub.StorePath(a.cfg.GetStateDir())) }

// newEnrollment creates an enrollment code for this hub.
func (a *App) newEnrollment(name string, ttl time.Duration) (hub.Code, error) {
	if a.cfg.Web.Listen == "" || a.cfg.Web.URL == "" {
		return hub.Code{}, errors.New("this machine is no hub: servers attach to the web interface, so set up the web section first (see 'herakles web --help')")
	}
	pin, err := hub.CertPin(config.ExpandHome(a.cfg.Web.TLSCert))
	if err != nil {
		return hub.Code{}, err
	}
	return a.hubStore().NewEnrollment(a.cfg.Web.URL, name, ttl, pin)
}

func (a *App) hubTokenCmd() *cobra.Command {
	var name string
	var ttl time.Duration
	c := &cobra.Command{
		Use:     "enroll",
		Aliases: []string{"token", "code"},
		Short:   "Create a one-time enrollment code that attaches a server to this hub",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			code, err := a.newEnrollment(name, ttl)
			if err != nil {
				return err
			}
			text := code.Encode()
			if a.jsonOut {
				return a.printJSON(map[string]any{"code": text, "expires": code.ExpiresAt(), "command": hub.JoinCommand(text), "hub_key": hub.KeyFingerprint(code.HubKey)})
			}
			a.printf("Run this on the server to attach (works once, until %s):\n\n  %s\n\nHub key: %s\n", ui.Time(code.ExpiresAt()), hub.JoinCommand(text), hub.KeyFingerprint(code.HubKey))
			return nil
		},
	}
	c.Flags().StringVar(&name, "name", "", "name of the server on the hub (default: its host name)")
	c.Flags().DurationVar(&ttl, "valid", hub.TokenTTL, "how long the code can be used")
	return c
}

func (a *App) hubServersCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "servers",
		Aliases: []string{"ls"},
		Short:   "List the servers attached to this hub",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			servers, err := a.hubStore().Servers()
			if err != nil {
				return err
			}
			online := map[string]bool{}
			if c, err := daemon.NewClient(a.cfg); err == nil {
				if st, err := c.Status(); err == nil {
					for _, id := range st.Hub {
						online[id] = true
					}
				}
			}
			if a.jsonOut {
				type row struct {
					*hub.Server
					Online bool `json:"online"`
				}
				out := []row{}
				for _, s := range servers {
					out = append(out, row{s, online[s.ID]})
				}
				return a.printJSON(out)
			}
			if len(servers) == 0 {
				a.printf("no servers attached; create an enrollment code with 'herakles hub enroll'\n")
				return nil
			}
			tw := tabwriter.NewWriter(a.out, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "NAME\tID\tSTATE\tHOST\tSYSTEM\tVERSION\tLAST SEEN")
			for _, s := range servers {
				state := "offline"
				if online[s.ID] {
					state = "online"
				}
				seen := "-"
				if !s.LastSeen.IsZero() {
					seen = ui.Time(s.LastSeen)
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s/%s\t%s\t%s\n", s.Name, s.ID, state, dash(s.Hello.Hostname), s.Hello.OS, s.Hello.Arch, dash(s.Hello.Version), seen)
			}
			return tw.Flush()
		},
	}
}

func (a *App) hubRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove NAME|ID",
		Short: "Detach a server from this hub (it can no longer connect)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			st := a.hubStore()
			s, err := st.Find(args[0])
			if err != nil {
				return err
			}
			if err := st.Remove(s.ID); err != nil {
				return err
			}
			a.printf("removed %s; its connection closes within 15 seconds\n", s.Name)
			return nil
		},
	}
}

func (a *App) hubRenameCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rename NAME|ID NEW-NAME",
		Short: "Rename an attached server",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			st := a.hubStore()
			s, err := st.Find(args[0])
			if err != nil {
				return err
			}
			return st.Rename(s.ID, args[1])
		},
	}
}
