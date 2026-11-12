package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/lamisator/herakles/internal/sysutil"
)

// DesktopDir holds one file per local user the system service shares its
// GUI with (gui.users), named after the user's ID (uid, or SID on Windows)
// and readable only by that user. 'herakles tray' and 'herakles gui' look
// for it before starting Herakles as the user.
var DesktopDir = defaultDesktopDir()

// DesktopShare is what a shared user may use: the GUI address and its
// session token.
type DesktopShare struct {
	GUI     string `json:"gui"`
	Token   string `json:"token"`
	Version string `json:"version,omitempty"`
	Host    string `json:"host,omitempty"`
}

// DesktopPath is the share file of a user ID.
func DesktopPath(id string) string { return filepath.Join(DesktopDir, id+".json") }

// CanShareDesktop reports whether this process may serve its GUI to other
// users: the system service (root, or elevated / LocalSystem on Windows).
func CanShareDesktop() bool {
	return (runtime.GOOS == "linux" || runtime.GOOS == "windows") && sysutil.Elevated()
}

// WriteDesktopShare writes the share file for u, readable only by them.
func WriteDesktopShare(u *user.User, s DesktopShare) error {
	if err := os.MkdirAll(DesktopDir, 0o755); err != nil {
		return err
	}
	if err := sysutil.SecureDir(DesktopDir); err != nil {
		return err
	}
	b, _ := json.Marshal(s)
	p := DesktopPath(u.Uid)
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := grantShare(tmp, u); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, p)
}

// RemoveDesktopShares removes all share files except those of keep (user
// IDs).
func RemoveDesktopShares(keep map[string]bool) {
	ents, _ := os.ReadDir(DesktopDir)
	for _, e := range ents {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || keep[id] {
			continue
		}
		os.Remove(filepath.Join(DesktopDir, e.Name()))
	}
}

// SharedDesktop returns the system service's GUI if it is shared with the
// current user and answers. Root (LocalSystem) never uses a share: it runs
// Herakles itself.
func SharedDesktop() (*DesktopShare, error) {
	id, ok := sysutil.SelfID()
	if !ok {
		return nil, ErrNotRunning
	}
	b, err := os.ReadFile(DesktopPath(id))
	if err != nil {
		return nil, ErrNotRunning
	}
	var s DesktopShare
	if err := json.Unmarshal(b, &s); err != nil || s.GUI == "" || s.Token == "" {
		return nil, ErrNotRunning
	}
	if _, err := s.Status(); err != nil {
		return nil, err
	}
	return &s, nil
}

func (s *DesktopShare) call(method, path string, out any) error {
	req, err := http.NewRequest(method, s.GUI+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+s.Token)
	req.Header.Set("X-Herakles", "1")
	c := &http.Client{Timeout: 3 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		return ErrNotRunning
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("system service: %s", resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// Status returns the service's daemon status.
func (s *DesktopShare) Status() (*Status, error) {
	var info struct {
		Daemon *Status `json:"daemon"`
	}
	if err := s.call("GET", "/api/info", &info); err != nil {
		return nil, err
	}
	if info.Daemon == nil {
		return nil, errors.New("system service: no daemon status")
	}
	return info.Daemon, nil
}

// OpenURL returns a one-time URL that logs the browser in.
func (s *DesktopShare) OpenURL() (string, error) {
	var t struct {
		Ticket string `json:"ticket"`
	}
	if err := s.call("POST", "/api/ticket", &t); err != nil {
		return "", err
	}
	return s.GUI + "/?ticket=" + t.Ticket, nil
}
