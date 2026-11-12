package tray

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/windows"
)

// EnsureMenuEntry adds "Herakles" to the user's Start menu, once: not when
// the setup added it for everyone, and not again after the user removed it
// (a marker in stateDir remembers). It returns the shortcut it created.
func EnsureMenuEntry(stateDir string) (string, error) {
	if common, err := windows.KnownFolderPath(windows.FOLDERID_CommonPrograms, 0); err == nil {
		if _, err := os.Stat(filepath.Join(common, "Herakles.lnk")); err == nil {
			return "", nil
		}
	}
	marker := filepath.Join(stateDir, "start-menu-entry")
	if _, err := os.Stat(marker); err == nil {
		return "", nil
	}
	programs, err := windows.KnownFolderPath(windows.FOLDERID_Programs, windows.KF_FLAG_CREATE)
	if err != nil {
		return "", err
	}
	lnk := filepath.Join(programs, "Herakles.lnk")
	if _, err := os.Stat(lnk); err != nil {
		exe, err := os.Executable()
		if err != nil {
			return "", err
		}
		// WScript.Shell writes the .lnk; values come from the environment so
		// no quoting is needed. Window style 7 = minimised (the console of
		// herakles.exe closes right away).
		script := `$s = (New-Object -ComObject WScript.Shell).CreateShortcut($env:HK_LNK); ` +
			`$s.TargetPath = $env:HK_EXE; $s.Arguments = 'tray --open'; $s.IconLocation = $env:HK_EXE + ',0'; ` +
			`$s.WorkingDirectory = [IO.Path]::GetDirectoryName($env:HK_EXE); $s.WindowStyle = 7; ` +
			`$s.Description = 'Back up and restore with Herakles'; $s.Save()`
		cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script)
		cmd.Env = append(os.Environ(), "HK_LNK="+lnk, "HK_EXE="+exe)
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
		if out, err := cmd.CombinedOutput(); err != nil {
			return "", fmt.Errorf("start menu entry: %v: %s", err, out)
		}
	} else {
		lnk = ""
	}
	if err := os.MkdirAll(stateDir, 0o700); err == nil {
		_ = os.WriteFile(marker, []byte("created by herakles tray; delete the Start menu entry and it stays deleted\n"), 0o600)
	}
	return lnk, nil
}
