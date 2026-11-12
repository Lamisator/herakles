package update

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// Detect finds out how this installation updates: through the setup when
// it was installed with herakles-setup.exe, else by replacing the file.
func Detect() Method {
	exe, err := os.Executable()
	if err != nil {
		return Method{Kind: "manual", Hint: err.Error()}
	}
	if k, err := registry.OpenKey(registry.LOCAL_MACHINE, `Software\Herakles`, registry.QUERY_VALUE|registry.WOW64_64KEY); err == nil {
		dir, _, err := k.GetStringValue("InstallDir")
		k.Close()
		if err == nil && strings.EqualFold(filepath.Clean(dir), filepath.Dir(exe)) {
			return Method{Kind: "setup", Exe: exe, Hint: "runs the new setup (Windows asks for administrator rights)"}
		}
	}
	if writable(exe) {
		return Method{Kind: "file", Exe: exe, Hint: "replaces " + exe}
	}
	return Method{Kind: "manual", Exe: exe, Hint: exe + " is not writable for this user: install the new herakles-setup.exe from https://herakles.kloeck-it.de"}
}

// Install updates to rel. Afterwards the caller restarts Herakles; the
// setup restarts the Windows service itself.
func Install(ctx context.Context, rel *Release, m Method) error {
	switch m.Kind {
	case "setup":
		f, ok := rel.Files["windows-setup.exe"]
		if !ok {
			return errors.New("the release has no Windows setup")
		}
		path, err := download(ctx, f, os.TempDir(), "herakles-setup.exe")
		if err != nil {
			return err
		}
		exe := path + ".exe"
		if err := os.Rename(path, exe); err != nil {
			return err
		}
		defer os.Remove(exe)
		code, err := runElevated(exe, "/S")
		if err != nil {
			return err
		}
		if code != 0 {
			return fmt.Errorf("the setup ended with code %d", code)
		}
		return nil
	case "file":
		f, ok := rel.Files[platformFile(runtime.GOOS, runtime.GOARCH)]
		if !ok {
			return errors.New("the release has no file for " + runtime.GOOS + "/" + runtime.GOARCH)
		}
		fresh, err := download(ctx, f, filepath.Dir(m.Exe), filepath.Base(m.Exe))
		if err != nil {
			return err
		}
		if err := replaceFile(m.Exe, fresh); err != nil {
			os.Remove(fresh)
			return err
		}
		return nil
	}
	return errors.New(m.Hint)
}

// shellExecuteInfo is SHELLEXECUTEINFOW.
type shellExecuteInfo struct {
	cbSize     uint32
	fMask      uint32
	hwnd       uintptr
	verb       *uint16
	file       *uint16
	parameters *uint16
	directory  *uint16
	show       int32
	instApp    uintptr
	idList     uintptr
	class      *uint16
	keyClass   uintptr
	hotKey     uint32
	iconOrMon  uintptr
	process    windows.Handle
}

var procShellExecuteExW = windows.NewLazySystemDLL("shell32.dll").NewProc("ShellExecuteExW")

// runElevated starts path with administrator rights (the UAC prompt) and
// waits for its exit code.
func runElevated(path, args string) (uint32, error) {
	const seeMaskNoCloseProcess = 0x40
	verb, _ := windows.UTF16PtrFromString("runas")
	file, _ := windows.UTF16PtrFromString(path)
	params, _ := windows.UTF16PtrFromString(args)
	info := shellExecuteInfo{fMask: seeMaskNoCloseProcess, verb: verb, file: file, parameters: params, show: windows.SW_SHOWNORMAL}
	info.cbSize = uint32(unsafe.Sizeof(info))
	if r, _, err := procShellExecuteExW.Call(uintptr(unsafe.Pointer(&info))); r == 0 {
		if errors.Is(err, windows.ERROR_CANCELLED) {
			return 0, errors.New("the update was cancelled at the administrator prompt")
		}
		return 0, err
	}
	if info.process == 0 {
		return 0, errors.New("the setup did not start")
	}
	defer windows.CloseHandle(info.process)
	if _, err := windows.WaitForSingleObject(info.process, windows.INFINITE); err != nil {
		return 0, err
	}
	var code uint32
	if err := windows.GetExitCodeProcess(info.process, &code); err != nil {
		return 0, err
	}
	return code, nil
}
