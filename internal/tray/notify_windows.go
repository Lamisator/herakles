package tray

import (
	"os"
	"runtime"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// notifyIconData is NOTIFYICONDATAW.
type notifyIconData struct {
	Size            uint32
	Wnd             windows.Handle
	ID              uint32
	Flags           uint32
	CallbackMessage uint32
	Icon            windows.Handle
	Tip             [128]uint16
	State           uint32
	StateMask       uint32
	Info            [256]uint16
	Timeout         uint32
	InfoTitle       [64]uint16
	InfoFlags       uint32
	GuidItem        windows.GUID
	BalloonIcon     windows.Handle
}

var (
	user32               = windows.NewLazySystemDLL("user32.dll")
	procCreateWindowExW  = user32.NewProc("CreateWindowExW")
	procDestroyWindow    = user32.NewProc("DestroyWindow")
	procLoadImageW       = user32.NewProc("LoadImageW")
	procDestroyIcon      = user32.NewProc("DestroyIcon")
	procShellNotifyIconW = windows.NewLazySystemDLL("shell32.dll").NewProc("Shell_NotifyIconW")
)

// Notify shows a Windows notification. It briefly adds a second icon to
// the notification area, which Windows needs to show one.
func Notify(title, body string) error {
	done := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		done <- notify(title, body)
	}()
	select {
	case err := <-done:
		return err
	case <-time.After(2 * time.Second):
		return nil // shown; the icon is removed in the background
	}
}

func notify(title, body string) error {
	// A message-only window of the predefined STATIC class owns the icon.
	class, _ := windows.UTF16PtrFromString("STATIC")
	const hwndMessage = ^uintptr(2) // (HWND)-3
	wnd, _, err := procCreateWindowExW.Call(0, uintptr(unsafe.Pointer(class)), 0, 0, 0, 0, 0, 0, hwndMessage, 0, 0, 0)
	if wnd == 0 {
		return err
	}
	defer procDestroyWindow.Call(wnd)

	var icon uintptr
	if f, err := os.CreateTemp("", "herakles-*.ico"); err == nil {
		_, _ = f.Write(ICO(Idle))
		f.Close()
		defer os.Remove(f.Name())
		name, _ := windows.UTF16PtrFromString(f.Name())
		const imageIcon, lrLoadFromFile, lrDefaultSize = 1, 0x10, 0x40
		icon, _, _ = procLoadImageW.Call(0, uintptr(unsafe.Pointer(name)), imageIcon, 0, 0, lrLoadFromFile|lrDefaultSize)
		if icon != 0 {
			defer procDestroyIcon.Call(icon)
		}
	}
	const (
		nimAdd, nimDelete          = 0, 2
		nifIcon, nifTip, nifInfo   = 0x2, 0x4, 0x10
		niifUser, niifRespectQuiet = 0x4, 0x80
		niifInfo                   = 0x1
	)
	nid := notifyIconData{Wnd: windows.Handle(wnd), ID: 1, Flags: nifTip | nifInfo, Icon: windows.Handle(icon)}
	nid.Size = uint32(unsafe.Sizeof(nid))
	nid.InfoFlags = niifInfo | niifRespectQuiet
	if icon != 0 {
		nid.Flags |= nifIcon
		nid.InfoFlags = niifUser | niifRespectQuiet
	}
	copy(nid.Tip[:len(nid.Tip)-1], windows.StringToUTF16("Herakles"))
	copy(nid.InfoTitle[:len(nid.InfoTitle)-1], windows.StringToUTF16(title))
	copy(nid.Info[:len(nid.Info)-1], windows.StringToUTF16(body))
	nid.Tip[len(nid.Tip)-1], nid.InfoTitle[len(nid.InfoTitle)-1], nid.Info[len(nid.Info)-1] = 0, 0, 0
	if r, _, err := procShellNotifyIconW.Call(nimAdd, uintptr(unsafe.Pointer(&nid))); r == 0 {
		return err
	}
	time.Sleep(10 * time.Second)
	procShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(&nid)))
	return nil
}
