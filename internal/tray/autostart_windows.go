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

// Install starts cmd at login through the user's Run registry key.
func Install(cmd Command) ([]string, error) {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return nil, err
	}
	defer k.Close()
	args := []string{cmd.Exe}
	args = append(args, cmd.Args...)
	for i, a := range args {
		args[i] = syscall.EscapeArg(a)
	}
	if err := k.SetStringValue("Herakles", strings.Join(args, " ")); err != nil {
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
// window. Started from a terminal, the console is kept.
func DetachConsole() {
	var pids [2]uint32
	n, _, _ := procGetConsoleProcessList.Call(uintptr(unsafe.Pointer(&pids[0])), 2)
	if n == 1 {
		_, _, _ = procFreeConsole.Call()
	}
}
