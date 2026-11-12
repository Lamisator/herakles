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
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
	return start(cmd)
}

// Available reports whether the WebView2 runtime is installed.
func Available() bool {
	v, err := webviewloader.GetInstalledVersion()
	return err == nil && v != ""
}

var (
	shell32       = windows.NewLazySystemDLL("shell32.dll")
	user32        = windows.NewLazySystemDLL("user32.dll")
	extractIconEx = shell32.NewProc("ExtractIconExW")
	sendMessage   = user32.NewProc("SendMessageW")
	setForeground = user32.NewProc("SetForegroundWindow")
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
