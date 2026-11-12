package window

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// helper finds herakles-window: next to herakles, in /usr/lib/herakles or
// in the PATH.
func helper() (string, error) {
	var dirs []string
	if exe, err := os.Executable(); err == nil {
		if real, err := filepath.EvalSymlinks(exe); err == nil {
			exe = real
		}
		dirs = append(dirs, filepath.Dir(exe))
	}
	dirs = append(dirs, "/usr/lib/herakles", "/usr/local/lib/herakles")
	for _, d := range dirs {
		p := filepath.Join(d, "herakles-window")
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return p, nil
		}
	}
	if p, err := exec.LookPath("herakles-window"); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("%w: herakles-window is not installed", ErrUnavailable)
}

func command(url string) (*exec.Cmd, error) {
	p, err := helper()
	if err != nil {
		return nil, err
	}
	cmd := withURL(exec.Command(p, "--title", Title, "--size", fmt.Sprintf("%dx%d", Width, Height)), url)
	cmd.Stderr = os.Stderr
	return cmd, nil
}

// Open shows url in a native window and returns once it is up.
func Open(url string) error {
	cmd, err := command(url)
	if err != nil {
		return err
	}
	return start(cmd)
}

// Show shows url in a native window and returns when it is closed.
func Show(url string) error {
	cmd, err := command(url)
	if err != nil {
		return err
	}
	return cmd.Run()
}
