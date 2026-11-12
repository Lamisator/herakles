package tray

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows/registry"
)

func iconBytes(s State) []byte { return ICO(s) }

func available() error { return nil }

func hostReady(context.Context, time.Duration) bool { return true }

const noHostWarning = ""

// promoteIcon shows the icon in the taskbar instead of the overflow menu,
// where Windows 11 puts new icons: once, when Explorer has registered it
// and the user has not chosen yet (no IsPromoted value). Older Windows
// versions have no such setting.
func promoteIcon(ctx context.Context) {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	base := strings.ToLower(filepath.Base(exe))
	for range 20 {
		select {
		case <-ctx.Done():
			return
		case <-time.After(3 * time.Second):
		}
		if promoted(base) {
			return
		}
	}
}

func promoted(base string) bool {
	const settings = `Control Panel\NotifyIconSettings`
	k, err := registry.OpenKey(registry.CURRENT_USER, settings, registry.ENUMERATE_SUB_KEYS)
	if err != nil {
		return true // not Windows 11
	}
	names, _ := k.ReadSubKeyNames(-1)
	k.Close()
	for _, n := range names {
		ik, err := registry.OpenKey(registry.CURRENT_USER, settings+`\`+n, registry.QUERY_VALUE|registry.SET_VALUE)
		if err != nil {
			continue
		}
		path, _, err := ik.GetStringValue("ExecutablePath")
		if err != nil || strings.ToLower(filepath.Base(path)) != base {
			ik.Close()
			continue
		}
		if _, _, err := ik.GetIntegerValue("IsPromoted"); err != nil {
			_ = ik.SetDWordValue("IsPromoted", 1)
		}
		ik.Close()
		return true
	}
	return false
}
