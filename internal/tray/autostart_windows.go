package tray

import (
	"errors"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`

// autostartEntry is the Run registry value Install writes for cmd.
func autostartEntry(cmd Command) string {
	args := []string{cmd.Exe}
	args = append(args, cmd.Args...)
	for i, a := range args {
		args[i] = syscall.EscapeArg(a)
	}
	return strings.Join(args, " ")
}

// installedEntry returns the current Run registry value, if any.
func installedEntry() (string, bool) {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		return "", false
	}
	defer k.Close()
	v, _, err := k.GetStringValue("Herakles")
	return v, err == nil
}

func quoteArg(s string) string { return syscall.EscapeArg(s) }

// Install starts cmd at login through the user's Run registry key.
func Install(cmd Command) ([]string, error) {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return nil, err
	}
	defer k.Close()
	if err := k.SetStringValue("Herakles", autostartEntry(cmd)); err != nil {
		return nil, err
	}
	return []string{`HKCU\` + runKey + `\Herakles`}, nil
}

// Uninstall removes the Run registry value.
func Uninstall() ([]string, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return nil, err
	}
	defer k.Close()
	if err := k.DeleteValue("Herakles"); err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	return []string{`HKCU\` + runKey + `\Herakles`}, nil
}

var (
	kernel32                  = windows.NewLazySystemDLL("kernel32.dll")
	procGetConsoleProcessList = kernel32.NewProc("GetConsoleProcessList")
	procFreeConsole           = kernel32.NewProc("FreeConsole")
)

// DetachConsole closes the console window when it was opened only for this
// process (started from Explorer or at login), so the tray runs without a
// window. Started from a terminal, the console is kept. It reports whether
// the console was closed: output then goes nowhere.
func DetachConsole() bool {
	var pids [2]uint32
	n, _, _ := procGetConsoleProcessList.Call(uintptr(unsafe.Pointer(&pids[0])), 2)
	if n == 1 {
		r, _, _ := procFreeConsole.Call()
		return r != 0
	}
	return false
}

// ErrorBox shows an error to a user who has no console.
func ErrorBox(title, msg string) {
	t, _ := windows.UTF16PtrFromString(title)
	m, _ := windows.UTF16PtrFromString(msg)
	const mbIconError, mbSetForeground = 0x10, 0x10000
	_, _ = windows.MessageBox(0, m, t, mbIconError|mbSetForeground)
}
