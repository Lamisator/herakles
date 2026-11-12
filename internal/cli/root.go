// Package cli implements the herakles command line interface.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/inconshreveable/mousetrap"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/lamisator/herakles/internal/archiver"
	"github.com/lamisator/herakles/internal/config"
	"github.com/lamisator/herakles/internal/crypto"
	"github.com/lamisator/herakles/internal/engine"
	"github.com/lamisator/herakles/internal/repository"
)

// Version is set at build time via -ldflags.
var Version = "dev"

// ExitWarning is returned when a backup finished but some files failed.
const ExitWarning = 3

type exitError struct {
	code int
	err  error
}

func (e exitError) Error() string { return e.err.Error() }

// App holds global CLI state.
type App struct {
	configPath   string
	repoFlag     string
	passwordFile string
	keyFile      string
	jsonOut      bool
	verbose      bool
	quiet        bool
	noPrompt     bool

	cfg *config.Config
	eng *engine.Engine
	out io.Writer
	err io.Writer
	in  *os.File
}

// Execute runs the CLI and returns the process exit code.
func Execute() int {
	archiver.Version = Version
	app := &App{out: os.Stdout, err: os.Stderr, in: os.Stdin}
	root := app.rootCommand()
	// Double-clicked in Explorer (Windows): open the GUI instead of
	// printing help into a console window that closes at once.
	cobra.MousetrapHelpText = ""
	if len(os.Args) == 1 && mousetrap.StartedByExplorer() {
		root.SetArgs([]string{"tray", "--open"})
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := root.ExecuteContext(ctx); err != nil {
		var ee exitError
		if errors.As(err, &ee) {
			if ee.err != nil && ee.code != ExitWarning {
				fmt.Fprintln(app.err, "error:", ee.err)
			}
			return ee.code
		}
		fmt.Fprintln(app.err, "error:", err)
		return 1
	}
	return 0
}

// NewRootCommand is exported for tests.
func NewRootCommand(out, errOut io.Writer, in *os.File) *cobra.Command {
	app := &App{out: out, err: errOut, in: in}
	return app.rootCommand()
}

func (a *App) rootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:   "herakles",
		Short: "Herakles — deduplicating, encrypted backup system",
		Long: `Herakles backs up files, directories and whole disks/partitions into
deduplicated, compressed and AES-256 encrypted repositories stored locally,
on network shares (NFS/SMB/UNC), via SFTP or on a Herakles repository server.

Configuration lives in a YAML file (see 'herakles config example') and can be
edited interactively with 'herakles tui' or 'herakles gui'.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       Version,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			return a.setup()
		},
	}
	root.SetOut(a.out)
	root.SetErr(a.err)
	pf := root.PersistentFlags()
	pf.StringVarP(&a.configPath, "config", "c", "", "configuration file (default "+config.DefaultPath()+")")
	pf.StringVarP(&a.repoFlag, "repo", "r", os.Getenv("HERAKLES_REPOSITORY"), "repository name from the config or a location/URL")
	pf.StringVar(&a.passwordFile, "password-file", "", "read the repository password from a file")
	pf.StringVar(&a.keyFile, "key-file", "", "use a key file to unlock the repository")
	pf.BoolVar(&a.jsonOut, "json", false, "machine readable JSON output")
	pf.BoolVarP(&a.verbose, "verbose", "v", false, "verbose logging")
	pf.BoolVarP(&a.quiet, "quiet", "q", false, "only print errors")
	pf.BoolVar(&a.noPrompt, "no-prompt", false, "never prompt for passwords")

	root.AddCommand(
		a.initCmd(), a.backupCmd(), a.jobsCmd(), a.snapshotsCmd(), a.lsCmd(), a.findCmd(), a.diffCmd(),
		a.restoreCmd(), a.restoreDeviceCmd(), a.dumpCmd(), a.forgetCmd(), a.pruneCmd(), a.checkCmd(),
		a.statsCmd(), a.keyCmd(), a.unlockCmd(), a.copyCmd(), a.repairCmd(), a.configCmd(),
		a.dockerCmd(), a.databasesCmd(), a.devicesCmd(), a.daemonCmd(), a.hubCmd(), a.webCmd(), a.serverCmd(), a.tuiCmd(), a.guiCmd(), a.trayCmd(), a.versionCmd(), a.updateCmd(), a.windowsSetupCmd(),
	)
	return root
}

func (a *App) setup() error {
	path := a.configPath
	if path == "" {
		path = config.DefaultPath()
	}
	cfg, err := config.Load(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		cfg = config.New(path)
	case err != nil:
		return err
	}
	a.cfg = cfg
	logLevel := slog.LevelWarn
	if a.verbose {
		logLevel = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(a.err, &slog.HandlerOptions{Level: logLevel}))
	a.eng = engine.New(cfg, log)
	a.eng.Prompt = a.prompt
	// Command line credential overrides apply to every repository used.
	if a.passwordFile != "" || a.keyFile != "" {
		for _, rc := range cfg.Repositories {
			a.applyCredOverrides(rc)
		}
	}
	return nil
}

func (a *App) applyCredOverrides(rc *config.Repository) {
	if a.passwordFile != "" {
		rc.Password, rc.PasswordEnv, rc.PasswordCommand = "", "", ""
		rc.PasswordFile = a.passwordFile
	}
	if a.keyFile != "" {
		rc.KeyFile = a.keyFile
		if a.passwordFile == "" && !rc.HasCredentialSource() {
			rc.Password = ""
		}
	}
}

func (a *App) isTTY() bool { return a.in != nil && term.IsTerminal(int(a.in.Fd())) }

func (a *App) readPassword(prompt string) (string, error) {
	if a.noPrompt || !a.isTTY() {
		return "", errors.New("no password configured and no terminal to prompt (use password_file, password_env, HERAKLES_PASSWORD or --password-file)")
	}
	fmt.Fprint(a.err, prompt)
	pw, err := term.ReadPassword(int(a.in.Fd()))
	fmt.Fprintln(a.err)
	return string(pw), err
}

func (a *App) prompt(repo string) (crypto.Credentials, error) {
	pw, err := a.readPassword(fmt.Sprintf("Password for repository %s: ", repo))
	return crypto.Credentials{Password: pw}, err
}

// repoConfig resolves --repo (or a default) into a repository config with
// command line overrides applied.
func (a *App) repoConfig(name string) (*config.Repository, error) {
	if name == "" {
		name = a.repoFlag
	}
	rc, err := a.eng.RepoConfig(name)
	if err != nil {
		return nil, err
	}
	a.applyCredOverrides(rc)
	return rc, nil
}

func (a *App) openRepo(ctx context.Context, name string) (*repository.Repository, *config.Repository, error) {
	rc, err := a.repoConfig(name)
	if err != nil {
		return nil, nil, err
	}
	r, err := a.eng.Open(ctx, rc)
	return r, rc, err
}

func (a *App) printf(f string, args ...any) {
	if !a.quiet && !a.jsonOut {
		fmt.Fprintf(a.out, f, args...)
	}
}

func (a *App) printJSON(v any) error {
	enc := json.NewEncoder(a.out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func (a *App) logf(f string, args ...any) {
	if !a.quiet && !a.jsonOut {
		fmt.Fprintf(a.err, f+"\n", args...)
	}
}

func (a *App) versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Fprintf(a.out, "herakles %s\n", Version)
		},
	}
}

// parseSnapSpec splits "snapshot:path".
func parseSnapSpec(s string) (string, string) {
	// Only "latest" or a hex ID prefix may precede the colon, so Windows
	// paths such as C:\Users are never split at the drive letter.
	if i := strings.Index(s, ":"); i > 0 && isSnapRef(s[:i]) {
		return s[:i], s[i+1:]
	}
	return s, ""
}

func isSnapRef(s string) bool {
	if s == "latest" {
		return true
	}
	if len(s) < 4 {
		return false
	}
	for _, c := range strings.ToLower(s) {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
