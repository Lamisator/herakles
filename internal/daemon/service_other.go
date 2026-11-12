//go:build !windows

package daemon

import (
	"context"
	"os"
	"os/exec"
	"strings"
)

// SystemServiceState reports whether the system service is installed and
// running, as any user may ask (systemd only).
func SystemServiceState() (installed, running bool) {
	if _, err := os.Stat("/run/systemd/system"); err != nil {
		return false, false
	}
	out, err := exec.Command("systemctl", "show", "--property=LoadState,ActiveState", ServiceName+".service").Output()
	if err != nil {
		return false, false
	}
	s := string(out)
	return strings.Contains(s, "LoadState=loaded"), strings.Contains(s, "ActiveState=active")
}

// runAsService is only needed on Windows; elsewhere the service manager
// runs the daemon like any program.
func runAsService(fn func(ctx context.Context) error) error { return fn(context.Background()) }
