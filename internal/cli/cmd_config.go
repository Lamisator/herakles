package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/lamisator/herakles/internal/config"
)

func (a *App) configCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "config", Short: "Inspect and edit the configuration"}
	var force, reveal bool
	initC := &cobra.Command{
		Use: "init", Short: "Write a commented example configuration to the config path", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			p := a.cfg.Path()
			if _, err := os.Stat(p); err == nil && !force {
				return fmt.Errorf("%s already exists (use --force to overwrite)", p)
			}
			if err := os.MkdirAll(dirOf(p), 0o700); err != nil {
				return err
			}
			if err := os.WriteFile(p, []byte(config.Example), 0o600); err != nil {
				return err
			}
			a.printf("wrote example configuration to %s — edit it, or use 'herakles tui' / 'herakles gui'\n", p)
			return nil
		},
	}
	initC.Flags().BoolVar(&force, "force", false, "overwrite an existing file")
	show := &cobra.Command{
		Use: "show", Short: "Print the effective configuration", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c := a.cfg.Clone()
			if !reveal {
				redact(c)
			}
			if a.jsonOut {
				return a.printJSON(c)
			}
			data, err := c.Marshal()
			if err != nil {
				return err
			}
			_, err = a.out.Write(data)
			return err
		},
	}
	show.Flags().BoolVar(&reveal, "reveal-secrets", false, "do not redact passwords")
	cmd.AddCommand(
		&cobra.Command{
			Use: "path", Short: "Print the configuration file path", Args: cobra.NoArgs,
			Run: func(cmd *cobra.Command, args []string) { fmt.Fprintln(a.out, a.cfg.Path()) },
		},
		&cobra.Command{
			Use: "example", Short: "Print a commented example configuration", Args: cobra.NoArgs,
			Run: func(cmd *cobra.Command, args []string) { fmt.Fprint(a.out, config.Example) },
		},
		&cobra.Command{
			Use: "validate", Short: "Validate the configuration file", Args: cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				if _, err := os.Stat(a.cfg.Path()); errors.Is(err, fs.ErrNotExist) {
					return fmt.Errorf("%s does not exist", a.cfg.Path())
				}
				// setup() already loaded and validated it.
				a.printf("%s is valid: %d repositories, %d jobs\n", a.cfg.Path(), len(a.cfg.Repositories), len(a.cfg.Jobs))
				return nil
			},
		},
		&cobra.Command{
			Use: "edit", Short: "Open the configuration in $EDITOR and validate it", Args: cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				editor := os.Getenv("VISUAL")
				if editor == "" {
					editor = os.Getenv("EDITOR")
				}
				if editor == "" {
					editor = "vi"
					if runtime.GOOS == "windows" {
						editor = "notepad"
					}
				}
				p := a.cfg.Path()
				if _, err := os.Stat(p); errors.Is(err, fs.ErrNotExist) {
					if err := os.MkdirAll(dirOf(p), 0o700); err != nil {
						return err
					}
					if err := os.WriteFile(p, []byte(config.Example), 0o600); err != nil {
						return err
					}
				}
				for {
					parts := strings.Fields(editor)
					c := exec.Command(parts[0], append(parts[1:], p)...)
					c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
					if err := c.Run(); err != nil {
						return err
					}
					if _, err := config.Load(p); err != nil {
						fmt.Fprintf(a.err, "configuration is invalid:\n%v\n", err)
						if !a.isTTY() {
							return err
						}
						fmt.Fprint(a.err, "Edit again? [Y/n] ")
						var ans string
						fmt.Fscanln(a.in, &ans)
						if strings.HasPrefix(strings.ToLower(ans), "n") {
							return err
						}
						continue
					}
					a.printf("configuration saved and valid\n")
					return nil
				}
			},
		},
		initC, show, a.configSetCmd(),
	)
	return cmd
}

func dirOf(p string) string {
	i := strings.LastIndexAny(p, `/\`)
	if i <= 0 {
		return "."
	}
	return p[:i]
}

func redact(c *config.Config) {
	for _, r := range c.Repositories {
		if r.Password != "" {
			r.Password = "***"
		}
		if r.Remote.Password != "" {
			r.Remote.Password = "***"
		}
	}
	for _, n := range c.Notifications {
		if n.SMTPPassword != "" {
			n.SMTPPassword = "***"
		}
	}
}

// configSetCmd provides scriptable config editing.
func (a *App) configSetCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "set", Short: "Add or replace repositories and jobs from the command line"}
	var repoYAML, jobYAML string
	var r config.Repository
	repo := &cobra.Command{
		Use:     "repo NAME LOCATION",
		Short:   "Add or replace a repository definition",
		Example: "  herakles config set repo local /mnt/backup --password-file /etc/herakles/pw\n  herakles config set repo nas smb://user@nas/share/herakles --yaml 'remote: {password_env: NAS_PW}'",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			nr := r
			if repoYAML != "" {
				if err := yaml.Unmarshal([]byte(repoYAML), &nr); err != nil {
					return err
				}
			}
			nr.Name, nr.Location = args[0], args[1]
			c := a.cfg.Clone()
			replaced := false
			for i, x := range c.Repositories {
				if x.Name == nr.Name {
					c.Repositories[i] = &nr
					replaced = true
				}
			}
			if !replaced {
				c.Repositories = append(c.Repositories, &nr)
			}
			if err := c.Save(); err != nil {
				return err
			}
			a.printf("repository %s saved to %s\n", nr.Name, c.Path())
			return nil
		},
	}
	repo.Flags().StringVar(&r.PasswordFile, "password-file-path", "", "password_file setting")
	repo.Flags().StringVar(&r.PasswordEnv, "password-env", "", "password_env setting")
	repo.Flags().StringVar(&r.PasswordCommand, "password-command", "", "password_command setting")
	repo.Flags().StringVar(&r.KeyFile, "key-file-path", "", "key_file setting")
	repo.Flags().StringVar(&r.Compression, "compression", "", "compression setting")
	repo.Flags().StringVar(&repoYAML, "yaml", "", "additional settings as inline YAML")

	var j config.Job
	var schedules []string
	job := &cobra.Command{
		Use:   "job NAME",
		Short: "Add or replace a backup job",
		Example: `  herakles config set job home --repository local --path /home --exclude '*.tmp' \
      --schedule '0 2 * * 0=full' --schedule '0 2 * * 1-6=incremental' \
      --yaml 'retention: {keep_daily: 7, keep_weekly: 4, keep_monthly: 12}'`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			nj := j
			if jobYAML != "" {
				if err := yaml.Unmarshal([]byte(jobYAML), &nj); err != nil {
					return err
				}
			}
			nj.Name = args[0]
			for _, s := range schedules {
				expr, typ, _ := strings.Cut(s, "=")
				nj.Schedules = append(nj.Schedules, config.Schedule{Cron: strings.TrimSpace(expr), Type: strings.TrimSpace(typ)})
			}
			c := a.cfg.Clone()
			replaced := false
			for i, x := range c.Jobs {
				if x.Name == nj.Name {
					c.Jobs[i] = &nj
					replaced = true
				}
			}
			if !replaced {
				c.Jobs = append(c.Jobs, &nj)
			}
			if err := c.Save(); err != nil {
				return err
			}
			a.printf("job %s saved to %s\n", nj.Name, c.Path())
			return nil
		},
	}
	job.Flags().StringVar(&j.Repository, "repository", "", "repository name")
	job.Flags().StringArrayVar(&j.Paths, "path", nil, "path to back up (repeatable)")
	job.Flags().StringArrayVar(&j.Devices, "device", nil, "device to image (repeatable)")
	job.Flags().StringArrayVar(&j.Exclude, "exclude", nil, "exclude pattern (repeatable)")
	job.Flags().StringArrayVar(&schedules, "schedule", nil, "schedule as 'CRON=TYPE' (repeatable)")
	job.Flags().StringVar(&j.FullInterval, "full-interval", "", "promote auto backups to full after this duration")
	job.Flags().StringVar(&jobYAML, "yaml", "", "additional settings as inline YAML")

	remove := &cobra.Command{
		Use: "remove repo|job NAME", Short: "Remove a repository or job", Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c := a.cfg.Clone()
			found := false
			switch args[0] {
			case "repo":
				for i, x := range c.Repositories {
					if x.Name == args[1] {
						c.Repositories = append(c.Repositories[:i], c.Repositories[i+1:]...)
						found = true
						break
					}
				}
			case "job":
				for i, x := range c.Jobs {
					if x.Name == args[1] {
						c.Jobs = append(c.Jobs[:i], c.Jobs[i+1:]...)
						found = true
						break
					}
				}
			default:
				return errors.New("first argument must be repo or job")
			}
			if !found {
				return fmt.Errorf("%s %q not found", args[0], args[1])
			}
			if err := c.Save(); err != nil {
				return err
			}
			a.printf("removed %s %s\n", args[0], args[1])
			return nil
		},
	}
	cmd.AddCommand(repo, job, remove)
	return cmd
}
