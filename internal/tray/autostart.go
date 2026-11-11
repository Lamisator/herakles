package tray

import (
	"os"
	"path/filepath"
	"strings"
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
	b.WriteString("Terminal=false\nCategories=Utility;Archiving;\n")
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
