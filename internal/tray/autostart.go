package tray

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/lamisator/herakles/internal/config"
)

// Command is how the session starts the tray: an absolute executable path
// and its arguments.
type Command struct {
	Exe  string
	Args []string
}

// desktopEntry renders a freedesktop .desktop file that runs cmd.
func desktopEntry(cmd Command, icon string, autostart bool) string {
	fields := []string{desktopQuote(cmd.Exe)}
	for _, a := range cmd.Args {
		fields = append(fields, desktopQuote(a))
	}
	var b strings.Builder
	b.WriteString("[Desktop Entry]\nType=Application\nName=Herakles Backup\n")
	b.WriteString("Comment=Scheduled, deduplicated and encrypted backups\n")
	b.WriteString("Exec=" + strings.Join(fields, " ") + "\n")
	if icon != "" {
		b.WriteString("Icon=" + icon + "\n")
	}
	// The Herakles window (herakles-window) runs as app ID / WM_CLASS
	// "herakles": the desktop shows this entry's icon for it.
	b.WriteString("StartupWMClass=herakles\nTerminal=false\nCategories=Utility;Archiving;\n")
	if autostart {
		b.WriteString("X-GNOME-Autostart-enabled=true\n")
	}
	return b.String()
}

// desktopQuote quotes an Exec argument as the Desktop Entry Specification
// requires: reserved characters force double quotes, inside which " ` $ and
// \ are backslash-escaped, and % is doubled to stay out of field codes.
func desktopQuote(s string) string {
	s = strings.ReplaceAll(s, "%", "%%")
	if s != "" && !strings.ContainsAny(s, " \t\n\"'\\><~|&;$*?#()`=") {
		return s
	}
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "`", "\\`", `$`, `\$`)
	return `"` + r.Replace(s) + `"`
}

// xdgDirs returns the XDG config and data directories.
func xdgDirs() (configDir, dataDir string, err error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", err
	}
	configDir = os.Getenv("XDG_CONFIG_HOME")
	if configDir == "" {
		configDir = filepath.Join(home, ".config")
	}
	dataDir = os.Getenv("XDG_DATA_HOME")
	if dataDir == "" {
		dataDir = filepath.Join(home, ".local", "share")
	}
	return configDir, dataDir, nil
}

// LoginCommand is the command that starts the tray for a configuration
// file: this executable with --config and the tray subcommand.
func LoginCommand(cfgPath string) (Command, error) {
	exe, err := os.Executable()
	if err != nil {
		return Command{}, err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return Command{}, err
	}
	if cfgPath, err = filepath.Abs(cfgPath); err != nil {
		return Command{}, err
	}
	return Command{Exe: exe, Args: []string{"--config", cfgPath, "tray"}}, nil
}

// Apply makes the login autostart match cfg.Tray.Autostart and returns
// what it changed. It does nothing when the state already matches, and it
// only removes an autostart entry that starts this configuration file.
func Apply(cfg *config.Config) ([]string, error) {
	cmd, err := LoginCommand(cfg.Path())
	if err != nil {
		return nil, err
	}
	cur, ok := installedEntry()
	if cfg.Tray.Autostart {
		if ok && cur == autostartEntry(cmd) {
			return nil, nil
		}
		return Install(cmd)
	}
	if ok && strings.Contains(cur, quoteArg(cmd.Args[1])) {
		return Uninstall()
	}
	return nil, nil
}

// ApplyLogged runs Apply and logs what changed or failed; saving the
// configuration must not fail because of the autostart entry.
func ApplyLogged(cfg *config.Config, log *slog.Logger) {
	changed, err := Apply(cfg)
	switch {
	case err != nil:
		log.Warn("cannot update login autostart", "err", err)
	case len(changed) > 0 && cfg.Tray.Autostart:
		log.Info("login autostart enabled", "files", strings.Join(changed, ", "))
	case len(changed) > 0:
		log.Info("login autostart disabled", "files", strings.Join(changed, ", "))
	}
}
