// Package window shows the Herakles GUI in a native window of its own
// instead of a web browser: WebView2 on Windows (part of Windows 11, and
// installed by the setup elsewhere), WebKitGTK on Linux through the
// herakles-window helper. Open returns an error when neither is available,
// and the caller falls back to a browser.
package window

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"
)

// Title and size of the window.
const (
	Title  = "Herakles"
	Width  = 1280
	Height = 860
)

// URLEnv passes the address to the window process: unlike the command
// line, the environment (with the GUI's session token) is not readable by
// other users.
const URLEnv = "HERAKLES_WINDOW_URL"

// ErrUnavailable means this system has no native window for the GUI.
var ErrUnavailable = errors.New("no native window available")

// startWait is how long Open waits for the window process to fail (missing
// libraries, no display) before it counts as open.
var startWait = 4 * time.Second

// start runs cmd and reports an error if it ends with a failure within
// startWait. The window process keeps running on its own.
func start(cmd *exec.Cmd) error {
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("%w: %v", ErrUnavailable, err)
		}
		return nil
	case <-time.After(startWait):
		go func() { <-done }()
		return nil
	}
}

// withURL passes url to cmd through the environment.
func withURL(cmd *exec.Cmd, url string) *exec.Cmd {
	cmd.Env = append(os.Environ(), URLEnv+"="+url)
	return cmd
}
