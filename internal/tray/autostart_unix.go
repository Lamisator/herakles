//go:build !windows

package tray

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

const desktopFile = "herakles.desktop"

func iconPath(dataDir string) string {
	return filepath.Join(dataDir, "icons", "hicolor", "256x256", "apps", "herakles.png")
}

// autostartEntry is the XDG autostart file Install writes for cmd.
func autostartEntry(cmd Command) string {
	_, dataDir, _ := xdgDirs()
	return desktopEntry(cmd, iconPath(dataDir), true)
}

// installedEntry returns the current XDG autostart file, if any.
func installedEntry() (string, bool) {
	cfgDir, _, err := xdgDirs()
	if err != nil {
		return "", false
	}
	b, err := os.ReadFile(filepath.Join(cfgDir, "autostart", desktopFile))
	return string(b), err == nil
}

func quoteArg(s string) string { return desktopQuote(s) }

// Install starts cmd at login (XDG autostart) and adds a launcher to the
// applications menu that runs cmd with --open, so it opens the window. It
// returns the files written.
func Install(cmd Command) ([]string, error) {
	cfgDir, dataDir, err := xdgDirs()
	if err != nil {
		return nil, err
	}
	icon := iconPath(dataDir)
	launcher := Command{Exe: cmd.Exe, Args: append(append([]string{}, cmd.Args...), "--open")}
	files := []struct {
		path string
		data []byte
	}{
		{icon, PNG(256, Idle)},
		{filepath.Join(cfgDir, "autostart", desktopFile), []byte(desktopEntry(cmd, icon, true))},
		{filepath.Join(dataDir, "applications", desktopFile), []byte(desktopEntry(launcher, icon, false))},
	}
	var written []string
	for _, f := range files {
		if err := os.MkdirAll(filepath.Dir(f.path), 0o755); err != nil {
			return written, err
		}
		if err := os.WriteFile(f.path, f.data, 0o644); err != nil {
			return written, err
		}
		written = append(written, f.path)
	}
	return written, nil
}

// Uninstall removes what Install wrote. It returns the files removed.
func Uninstall() ([]string, error) {
	cfgDir, dataDir, err := xdgDirs()
	if err != nil {
		return nil, err
	}
	var removed []string
	for _, p := range []string{
		filepath.Join(cfgDir, "autostart", desktopFile),
		filepath.Join(dataDir, "applications", desktopFile),
		iconPath(dataDir),
	} {
		switch err := os.Remove(p); {
		case err == nil:
			removed = append(removed, p)
		case !errors.Is(err, fs.ErrNotExist):
			return removed, err
		}
	}
	return removed, nil
}

// DetachConsole is a no-op outside Windows.
func DetachConsole() {}
