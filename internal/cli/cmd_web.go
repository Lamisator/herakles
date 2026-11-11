package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/lamisator/herakles/internal/accounts"
	"github.com/lamisator/herakles/internal/daemon"
	"github.com/lamisator/herakles/internal/gui"
	"github.com/lamisator/herakles/internal/ui"
)

// accountsPath is the user database of the web interface.
func (a *App) accountsPath() string {
	return filepath.Join(a.cfg.GetStateDir(), "web", "accounts.json")
}

func (a *App) webOptions(d *daemon.Daemon) gui.Options {
	w := a.cfg.Web
	ttl := time.Duration(w.SessionHours) * time.Hour
	return gui.Options{Listen: w.Listen, Version: Version, Runner: d.Runner, Logs: d.Logs, TLSCert: w.TLSCert, TLSKey: w.TLSKey,
		Web: &gui.WebOptions{URL: w.URL, Accounts: a.accountsPath(), SessionTTL: ttl, Log: a.eng.Log}}
}

func (a *App) webCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "web",
		Short: "Manage the accounts of the web interface",
		Long: `With a web section in the configuration, the daemon serves the GUI on the
network with logins (passwords and passkeys). There is no public sign-up:
the first administrator is created with a one-time setup link from the
daemon log (also shown by 'herakles web setup-link'), and administrators
create further users in the GUI or with these commands.

  web:
    listen: 127.0.0.1:7480                 # behind a TLS reverse proxy
    url: https://backup.example.com        # public address (passkeys)`,
	}
	open := func() (*accounts.Store, error) { return accounts.Open(a.accountsPath()) }
	readPW := func(file string) (string, error) {
		if file != "" {
			b, err := os.ReadFile(file)
			return strings.TrimRight(string(b), "\r\n"), err
		}
		pw, err := a.readPassword("Password: ")
		if err != nil {
			return "", err
		}
		again, err := a.readPassword("Repeat password: ")
		if err != nil {
			return "", err
		}
		if pw != again {
			return "", errors.New("passwords do not match")
		}
		return pw, nil
	}
	list := &cobra.Command{
		Use: "users", Short: "List accounts", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := open()
			if err != nil {
				return err
			}
			users := s.List()
			if a.jsonOut {
				return a.printJSON(users)
			}
			tw := tabwriter.NewWriter(a.out, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "NAME\tROLE\tPASSWORD\tPASSKEYS\tLAST LOGIN\tSTATE")
			for _, u := range users {
				state := "active"
				if u.Disabled {
					state = "disabled"
				}
				pw := "no"
				if u.HasPassword() {
					pw = "yes"
				}
				last := "never"
				if !u.LastLogin.IsZero() {
					last = ui.Time(u.LastLogin)
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\t%s\n", u.Name, u.Role, pw, len(u.Passkeys), last, state)
			}
			return tw.Flush()
		},
	}
	var admin bool
	var pwFile string
	add := &cobra.Command{
		Use: "add NAME", Short: "Create an account", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := open()
			if err != nil {
				return err
			}
			pw, err := readPW(pwFile)
			if err != nil {
				return err
			}
			role := accounts.RoleUser
			if admin || s.Empty() {
				role = accounts.RoleAdmin
			}
			u, err := s.Create(args[0], role, pw)
			if err != nil {
				return err
			}
			a.printf("created %s (%s); passkeys can be added after signing in\n", u.Name, u.Role)
			return nil
		},
	}
	add.Flags().BoolVar(&admin, "admin", false, "administrator (the first account always is)")
	add.Flags().StringVar(&pwFile, "password-file", "", "read the password from a file")
	passwd := &cobra.Command{
		Use: "passwd NAME", Short: "Set an account's password (also re-enables it)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := open()
			if err != nil {
				return err
			}
			u, err := s.ByName(args[0])
			if err != nil {
				return err
			}
			pw, err := readPW(pwFile)
			if err != nil {
				return err
			}
			if err := accounts.ValidatePassword(pw); err != nil {
				return err
			}
			_, err = s.Update(u.ID, func(u *accounts.User) error {
				u.PasswordHash, u.Disabled = accounts.HashPassword(pw), false
				return nil
			})
			if err == nil {
				a.printf("password of %s changed\n", u.Name)
			}
			return err
		},
	}
	passwd.Flags().StringVar(&pwFile, "password-file", "", "read the password from a file")
	remove := &cobra.Command{
		Use: "remove NAME", Short: "Delete an account", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := open()
			if err != nil {
				return err
			}
			u, err := s.ByName(args[0])
			if err != nil {
				return err
			}
			if err := s.Delete(u.ID); err != nil {
				return err
			}
			a.printf("deleted %s\n", u.Name)
			return nil
		},
	}
	link := &cobra.Command{
		Use: "setup-link", Short: "Print the one-time link for creating the first administrator", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := open()
			if err != nil {
				return err
			}
			if !s.Empty() {
				return errors.New("accounts exist already; sign in, or use 'herakles web add'")
			}
			b, err := os.ReadFile(filepath.Join(filepath.Dir(a.accountsPath()), "setup-link"))
			if err != nil {
				return errors.New("no setup link yet: is the daemon running with a web section? (it is in the daemon log)")
			}
			a.printf("%s", b)
			return nil
		},
	}
	cmd.AddCommand(list, add, passwd, remove, link)
	return cmd
}
