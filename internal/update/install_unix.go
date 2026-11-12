//go:build !windows

package update

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Detect finds out how this installation updates.
func Detect() Method {
	exe, err := os.Executable()
	if err != nil {
		return Method{Kind: "manual", Hint: err.Error()}
	}
	if p, err := filepath.EvalSymlinks(exe); err == nil {
		exe = p
	}
	if runtime.GOOS == "linux" {
		if mgr := packageOwner(exe); mgr != "" {
			return Method{Kind: "package", Manager: mgr, Exe: exe, Hint: "installs the update with " + mgr + " (asks for the administrator password)"}
		}
	}
	if writable(exe) {
		return Method{Kind: "file", Exe: exe, Hint: "replaces " + exe}
	}
	return Method{Kind: "manual", Exe: exe, Hint: exe + " is not writable for this user: replace it with the new download from https://herakles.kloeck-it.de, or install the package (curl -fsSL https://herakles.kloeck-it.de/install.sh | sudo sh)"}
}

// packageOwner names the package manager that installed path, if any.
func packageOwner(path string) string {
	quiet := func(name string, args ...string) bool {
		if _, err := exec.LookPath(name); err != nil {
			return false
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return exec.CommandContext(ctx, name, args...).Run() == nil
	}
	switch {
	case quiet("dpkg-query", "-S", path):
		return "apt"
	case quiet("pacman", "-Qo", path):
		return "pacman"
	case quiet("rpm", "-qf", path):
		if _, err := exec.LookPath("zypper"); err == nil {
			return "zypper"
		}
		if _, err := exec.LookPath("dnf"); err == nil {
			return "dnf"
		}
		return "yum"
	}
	return ""
}

// packageScript updates only the herakles package.
func packageScript(mgr string) string {
	switch mgr {
	case "apt":
		return "apt-get update -o Dir::Etc::sourcelist=sources.list.d/herakles.list -o Dir::Etc::sourceparts=- -o APT::Get::List-Cleanup=0 && DEBIAN_FRONTEND=noninteractive apt-get install -y --only-upgrade herakles"
	case "dnf":
		return "dnf -y --refresh upgrade herakles"
	case "yum":
		return "yum -y upgrade herakles"
	case "zypper":
		return "zypper --non-interactive refresh herakles && zypper --non-interactive update herakles"
	case "pacman":
		return "pacman -Sy --noconfirm --needed herakles"
	}
	return ""
}

// Install updates to rel. Afterwards the caller restarts Herakles; the
// system service is restarted by the package or replaced file's owner.
func Install(ctx context.Context, rel *Release, m Method) error {
	switch m.Kind {
	case "package":
		script := packageScript(m.Manager)
		cmd := exec.CommandContext(ctx, "sh", "-c", script)
		if os.Geteuid() != 0 {
			if _, err := exec.LookPath("pkexec"); err != nil {
				return fmt.Errorf("pkexec is missing; update in a terminal: sudo sh -c '%s'", script)
			}
			cmd = exec.CommandContext(ctx, "pkexec", "sh", "-c", script)
		}
		out, err := cmd.CombinedOutput()
		if err != nil {
			msg := strings.TrimSpace(string(out))
			if len(msg) > 600 {
				msg = "…" + msg[len(msg)-600:]
			}
			return fmt.Errorf("%s: %v: %s", m.Manager, err, msg)
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
