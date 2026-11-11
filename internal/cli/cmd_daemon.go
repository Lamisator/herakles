package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/lamisator/herakles/internal/config"
	"github.com/lamisator/herakles/internal/daemon"
	"github.com/lamisator/herakles/internal/server"
	"github.com/lamisator/herakles/internal/ui"
)

func (a *App) daemonCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "daemon", Short: "Run and control the background backup service"}
	var userService bool
	run := &cobra.Command{
		Use:   "run",
		Short: "Run the daemon in the foreground (used by the system service)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			fn := func(ctx context.Context) error { return a.runDaemon(ctx) }
			if !daemon.Interactive() {
				return daemon.RunService(a.cfg.Path(), fn)
			}
			return fn(cmd.Context())
		},
	}
	status := &cobra.Command{
		Use: "status", Short: "Show daemon status, schedule and running tasks", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := daemon.NewClient(a.cfg)
			if err != nil {
				return err
			}
			st, err := c.Status()
			if err != nil {
				return err
			}
			if a.jsonOut {
				return a.printJSON(st)
			}
			a.printf("daemon %s (pid %d) running since %s\n\n", st.Version, st.PID, ui.Time(st.Started))
			tw := tabwriter.NewWriter(a.out, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "NEXT RUN\tIN\tTASK\tTARGET\tTYPE\tSCHEDULE")
			for _, e := range st.Schedule {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", ui.Time(e.Next), ui.Until(e.Next), e.Kind, e.Target, dash(e.Type), e.Cron)
			}
			tw.Flush()
			if len(st.Active) > 0 {
				a.printf("\nactive tasks:\n")
				for _, r := range st.Active {
					line := fmt.Sprintf("  [%s] %s %s (%s, %s)", r.ID, r.Kind, r.Target, r.State, r.Trigger)
					if r.ProgressStats != nil {
						line += " " + progressLine("", *r.ProgressStats)
					} else if r.Message != "" {
						line += " " + r.Message
					}
					a.printf("%s\n", line)
				}
			}
			if len(st.Last) > 0 {
				a.printf("\nlast results:\n")
				for job, r := range st.Last {
					a.printf("  %-20s %-8s %s (%s)%s\n", job, r.Status, ui.Time(r.Start), r.Type, errSuffix(r.Error))
				}
			}
			return nil
		},
	}
	var typ string
	var wait bool
	trigger := &cobra.Command{
		Use: "trigger JOB|REPO", Short: "Ask the daemon to run a job (or prune/check a repository) now", Args: cobra.ExactArgs(1),
		Example: "  herakles daemon trigger home --type full\n  herakles daemon trigger local --kind prune",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := daemon.NewClient(a.cfg)
			if err != nil {
				return err
			}
			kind, _ := cmd.Flags().GetString("kind")
			run, err := c.Run(kind, args[0], typ)
			if err != nil {
				return err
			}
			a.printf("queued task %s (%s %s)\n", run.ID, run.Kind, run.Target)
			if !wait {
				return nil
			}
			for {
				select {
				case <-cmd.Context().Done():
					return cmd.Context().Err()
				case <-time.After(time.Second):
				}
				v, err := c.GetRun(run.ID)
				if err != nil {
					return err
				}
				if v.ProgressStats != nil && a.isTTY() {
					fmt.Fprint(a.err, "\r\033[K"+progressLine(args[0], *v.ProgressStats))
				}
				if v.State == "done" {
					if a.isTTY() {
						fmt.Fprint(a.err, "\r\033[K")
					}
					if v.Error != "" {
						return errors.New(v.Error)
					}
					if v.Result != nil {
						a.printResult(v.Result)
					}
					return nil
				}
			}
		},
	}
	trigger.Flags().StringVarP(&typ, "type", "t", "", "backup type")
	trigger.Flags().String("kind", "backup", "task kind: backup, prune, check, forget")
	trigger.Flags().BoolVarP(&wait, "wait", "w", false, "wait for completion")
	cancel := &cobra.Command{
		Use: "cancel TASK-ID|JOB", Short: "Cancel a queued or running task", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := daemon.NewClient(a.cfg)
			if err != nil {
				return err
			}
			return c.Cancel(args[0])
		},
	}
	reload := &cobra.Command{
		Use: "reload", Short: "Reload the configuration in the running daemon", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := daemon.NewClient(a.cfg)
			if err != nil {
				return err
			}
			if err := c.Reload(); err != nil {
				return err
			}
			a.printf("configuration reloaded\n")
			return nil
		},
	}
	logs := &cobra.Command{
		Use: "logs", Short: "Show recent daemon log messages", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := daemon.NewClient(a.cfg)
			if err != nil {
				return err
			}
			lines, err := c.Logs()
			if err != nil {
				return err
			}
			if a.jsonOut {
				return a.printJSON(lines)
			}
			for _, l := range lines {
				fmt.Fprintf(a.out, "%s %-5s %s %s\n", ui.Time(l.Time), l.Level, l.Message, l.Attrs)
			}
			return nil
		},
	}
	svc := func(action, short string) *cobra.Command {
		c := &cobra.Command{
			Use: action, Short: short, Args: cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				msg, err := daemon.ServiceControl(action, a.cfg.Path(), userService)
				if err != nil {
					return err
				}
				a.printf("%s\n", msg)
				return nil
			},
		}
		c.Flags().BoolVar(&userService, "user", false, "per-user service (systemd --user / launchd agent)")
		return c
	}
	svcStatus := svc("status", "Show the system service status")
	svcStatus.Use = "service-status"
	svcStatus.RunE = func(cmd *cobra.Command, args []string) error {
		msg, err := daemon.ServiceControl("status", a.cfg.Path(), userService)
		if err != nil {
			return err
		}
		a.printf("%s\n", msg)
		return nil
	}
	cmd.AddCommand(run, status, trigger, cancel, reload, logs,
		svc("install", "Install the daemon as a system service (systemd / Windows service)"),
		svc("uninstall", "Remove the system service"),
		svc("start", "Start the system service"),
		svc("stop", "Stop the system service"),
		svc("restart", "Restart the system service"),
		svcStatus)
	return cmd
}

func errSuffix(e string) string {
	if e == "" {
		return ""
	}
	return ": " + e
}

func (a *App) daemonLogger(buf *daemon.LogBuffer) (*slog.Logger, io.Closer, error) {
	var w io.Writer = a.err
	var closer io.Closer = io.NopCloser(nil)
	if a.cfg.Daemon.LogFile != "" {
		f, err := os.OpenFile(config.ExpandHome(a.cfg.Daemon.LogFile), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, nil, err
		}
		w, closer = f, f
	}
	lvl := slog.LevelInfo
	if a.verbose {
		lvl = slog.LevelDebug
	}
	h := daemon.NewTeeHandler(slog.NewTextHandler(w, &slog.HandlerOptions{Level: lvl}), buf)
	return slog.New(h), closer, nil
}

func (a *App) runDaemon(ctx context.Context) error {
	for _, rc := range a.cfg.Repositories {
		if !rc.HasCredentialSource() {
			return fmt.Errorf("repository %s has no non-interactive password source; the daemon cannot prompt (set password_file, password_env, password_command or key_file)", rc.Name)
		}
	}
	d, closer, err := a.newDaemon(ctx, nil)
	if err != nil {
		return err
	}
	defer closer.Close()
	return d.Run(ctx)
}

// newDaemon sets up logging and creates the daemon; it reloads the
// configuration on SIGHUP until ctx ends. onReload, if set, sees every
// configuration loaded by a reload.
func (a *App) newDaemon(ctx context.Context, onReload func(*config.Config)) (*daemon.Daemon, io.Closer, error) {
	if len(a.cfg.Jobs) == 0 {
		fmt.Fprintf(a.err, "warning: no jobs configured in %s\n", a.cfg.Path())
	}
	buf := daemon.NewLogBuffer(1000)
	log, closer, err := a.daemonLogger(buf)
	if err != nil {
		return nil, nil, err
	}
	a.eng.Log = log
	a.eng.Prompt = nil
	path := a.cfg.Path()
	d, err := daemon.New(ctx, a.eng, daemon.Options{Version: Version, Logs: buf, Reload: func() (*config.Config, error) {
		c, err := config.Load(path)
		if err == nil && onReload != nil {
			onReload(c)
		}
		return c, err
	}})
	if err != nil {
		closer.Close()
		return nil, nil, err
	}
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	go func() {
		defer signal.Stop(hup)
		for {
			select {
			case <-hup:
				if err := d.Reload(); err != nil {
					log.Error("reload failed", "err", err)
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	return d, closer, nil
}

func (a *App) serverCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "server", Short: "Run the Herakles repository server"}
	var listen, dataDir string
	var appendOnly, noAuth bool
	run := &cobra.Command{
		Use: "run", Short: "Serve repositories over HTTP(S)", Args: cobra.NoArgs,
		Example: "  herakles server run --data-dir /srv/herakles --listen :8443\n  herakles server run --append-only",
		RunE: func(cmd *cobra.Command, args []string) error {
			sc := a.cfg.Server
			if listen != "" {
				sc.Listen = listen
			}
			if dataDir != "" {
				sc.DataDir = dataDir
			}
			if appendOnly {
				sc.AppendOnly = true
			}
			if noAuth {
				sc.NoAuth = true
			}
			lvl := slog.LevelInfo
			if a.verbose {
				lvl = slog.LevelDebug
			}
			srv, err := server.New(sc, slog.New(slog.NewTextHandler(a.err, &slog.HandlerOptions{Level: lvl})))
			if err != nil {
				return err
			}
			if sc.TLSCert == "" {
				fmt.Fprintln(a.err, "warning: serving without TLS; use tls_cert/tls_key (see 'herakles server gen-cert') outside trusted networks")
			}
			return srv.ListenAndServe(cmd.Context())
		},
	}
	run.Flags().StringVar(&listen, "listen", "", "listen address (default from config or :8443)")
	run.Flags().StringVar(&dataDir, "data-dir", "", "directory holding the repositories")
	run.Flags().BoolVar(&appendOnly, "append-only", false, "deny deletion for all users")
	run.Flags().BoolVar(&noAuth, "no-auth", false, "disable authentication (trusted networks only)")
	hash := &cobra.Command{
		Use: "hash-password", Short: "Create a bcrypt hash for server.users[].password_hash", Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			pw := ""
			if len(args) == 1 {
				pw = args[0]
			} else {
				var err error
				if pw, err = a.readPassword("Password: "); err != nil {
					return err
				}
			}
			h, err := server.HashPassword(pw)
			if err != nil {
				return err
			}
			fmt.Fprintln(a.out, h)
			return nil
		},
	}
	var hosts string
	gen := &cobra.Command{
		Use: "gen-cert CERT KEY", Short: "Generate a self-signed TLS certificate", Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			h := strings.Split(hosts, ",")
			if err := server.GenerateSelfSigned(args[0], args[1], h); err != nil {
				return err
			}
			a.printf("wrote %s and %s; clients use the certificate as remote.ca_cert\n", args[0], args[1])
			return nil
		},
	}
	hn, _ := os.Hostname()
	gen.Flags().StringVar(&hosts, "hosts", "localhost,127.0.0.1,"+hn, "comma separated host names / IPs")
	cmd.AddCommand(run, hash, gen)
	return cmd
}
