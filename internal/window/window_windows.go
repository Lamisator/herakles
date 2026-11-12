package window

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"unsafe"

	webview2 "github.com/jchv/go-webview2"
	"github.com/jchv/go-webview2/webviewloader"
	"golang.org/x/sys/windows"
)

// Open shows url in a native window and returns once it is up. The window
// runs in a process of its own ('herakles window'), so it does not share
// the message loop of the tray icon.
func Open(url string) error {
	if !Available() {
		return fmt.Errorf("%w: the WebView2 runtime is not installed", ErrUnavailable)
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := withURL(exec.Command(exe, "window"), url)
	// CREATE_NO_WINDOW: no console. Not HideWindow: Windows would apply
	// SW_HIDE to the Herakles window's first ShowWindow.
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000}
	if f := logFile(); f != nil {
		defer f.Close()
		cmd.Stdout, cmd.Stderr = f, f
	}
	// The tray was clicked, so it may bring the new window to the front.
	allowSetForeground.Call(^uintptr(0)) // ASFW_ANY
	return start(cmd)
}

// Available reports whether the WebView2 runtime is installed.
func Available() bool {
	v, err := webviewloader.GetInstalledVersion()
	return err == nil && v != ""
}

var (
	shell32            = windows.NewLazySystemDLL("shell32.dll")
	user32             = windows.NewLazySystemDLL("user32.dll")
	extractIconEx      = shell32.NewProc("ExtractIconExW")
	sendMessage        = user32.NewProc("SendMessageW")
	setForeground      = user32.NewProc("SetForegroundWindow")
	showWindow         = user32.NewProc("ShowWindow")
	allowSetForeground = user32.NewProc("AllowSetForegroundWindow")
)

// Show shows url in a native window and returns when it is closed. It must
// run on the main thread of a process of its own.
func Show(url string) error {
	runtime.LockOSThread()
	if !Available() {
		return fmt.Errorf("%w: the WebView2 runtime is not installed", ErrUnavailable)
	}
	data := filepath.Join(os.Getenv("LOCALAPPDATA"), "Herakles", "WebView2")
	w := webview2.NewWithOptions(webview2.WebViewOptions{
		DataPath:  data,
		AutoFocus: true,
		WindowOptions: webview2.WindowOptions{
			Title: Title, Width: Width, Height: Height, Center: true,
		},
	})
	if w == nil {
		return fmt.Errorf("%w: WebView2 did not start", ErrUnavailable)
	}
	defer w.Destroy()
	hwnd := uintptr(w.Window())
	setIcon(hwnd)
	// Show it again in case the first ShowWindow took a hidden or minimised
	// state from the starting process.
	const swShowNormal = 1
	showWindow.Call(hwnd, swShowNormal)
	setForeground.Call(hwnd)
	w.Navigate(url)
	w.Run()
	return nil
}

// setIcon gives the window the icon of herakles.exe.
func setIcon(hwnd uintptr) {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	p, err := windows.UTF16PtrFromString(exe)
	if err != nil {
		return
	}
	var large, small uintptr
	if n, _, _ := extractIconEx.Call(uintptr(unsafe.Pointer(p)), 0, uintptr(unsafe.Pointer(&large)), uintptr(unsafe.Pointer(&small)), 1); n == 0 {
		return
	}
	const wmSetIcon = 0x0080
	if large != 0 {
		sendMessage.Call(hwnd, wmSetIcon, 1, large)
	}
	if small != 0 {
		sendMessage.Call(hwnd, wmSetIcon, 0, small)
	}
}

// logFile is window.log in Herakles' state folder: what the window process
// reports, since it has no console.
func logFile() *os.File {
	dir := filepath.Join(os.Getenv("LOCALAPPDATA"), "Herakles")
	if os.MkdirAll(dir, 0o700) != nil {
		return nil
	}
	f, err := os.OpenFile(filepath.Join(dir, "window.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil
	}
	return f
}
