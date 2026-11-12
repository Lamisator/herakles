package cli

import (
	"os"
	"os/exec"
	"path/filepath"
)

// trayUnit is the systemd user unit of the tray (packaging/
// herakles-tray.service): systemd restarts the tray when it crashes and
// after updates.
const trayUnit = "herakles-tray.service"

// inTrayUnit reports whether this process is the tray unit's.
func inTrayUnit() bool { return os.Getenv("HERAKLES_TRAY_UNIT") != "" }

// startTrayUnit starts the tray as the systemd user unit instead of in this
// process, if the unit is installed and the user's systemd is reachable.
// Only for the default configuration, which the unit uses.
func (a *App) startTrayUnit() bool {
	if inTrayUnit() || a.configPath != "" || os.Getenv("HERAKLES_NO_TRAY_UNIT") != "" {
		return false
	}
	found := false
	for _, d := range []string{"/etc/systemd/user", "/usr/lib/systemd/user", "/usr/local/lib/systemd/user"} {
		if _, err := os.Stat(filepath.Join(d, trayUnit)); err == nil {
			found = true
			break
		}
	}
	if rt := os.Getenv("XDG_RUNTIME_DIR"); !found || rt == "" {
		return false
	} else if _, err := os.Stat(filepath.Join(rt, "systemd", "private")); err != nil {
		return false
	}
	// The unit runs in the user's systemd, which may not know this
	// session's display (Xfce and others do not pass it on).
	var vars []string
	for _, v := range []string{"DISPLAY", "WAYLAND_DISPLAY", "XAUTHORITY", "XDG_CURRENT_DESKTOP", "XDG_SESSION_TYPE", "XDG_SESSION_DESKTOP", "DESKTOP_SESSION"} {
		if os.Getenv(v) != "" {
			vars = append(vars, v)
		}
	}
	if len(vars) > 0 {
		_ = exec.Command("systemctl", append([]string{"--user", "import-environment"}, vars...)...).Run()
	}
	if out, err := exec.Command("systemctl", "--user", "start", trayUnit).CombinedOutput(); err != nil {
		a.eng.Log.Warn("cannot start the tray unit; running the tray directly", "err", err, "out", string(out))
		return false
	}
	return true
}
