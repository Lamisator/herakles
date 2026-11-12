package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// DesktopDir holds one file per local user the system service shares its
// GUI with (gui.users), named after the user ID and readable only by that
// user. 'herakles tray' and 'herakles gui' look for it before starting
// Herakles as the user.
var DesktopDir = "/run/herakles/desktop"

// DesktopShare is what a shared user may use: the GUI address and its
// session token.
type DesktopShare struct {
	GUI     string `json:"gui"`
	Token   string `json:"token"`
	Version string `json:"version,omitempty"`
	Host    string `json:"host,omitempty"`
}

// DesktopPath is the share file of a user ID.
func DesktopPath(uid int) string { return filepath.Join(DesktopDir, strconv.Itoa(uid)+".json") }

// WriteDesktopShare writes the share file for uid, owned by uid:gid and
// readable only by them.
func WriteDesktopShare(uid, gid int, s DesktopShare) error {
	if err := os.MkdirAll(DesktopDir, 0o755); err != nil {
		return err
	}
	b, _ := json.Marshal(s)
	p := DesktopPath(uid)
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Chown(tmp, uid, gid); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, p)
}

// RemoveDesktopShares removes all share files except those of keep.
func RemoveDesktopShares(keep map[int]bool) {
	ents, _ := os.ReadDir(DesktopDir)
	for _, e := range ents {
		id, err := strconv.Atoi(strings.TrimSuffix(e.Name(), ".json"))
		if err != nil || !strings.HasSuffix(e.Name(), ".json") || keep[id] {
			continue
		}
		os.Remove(filepath.Join(DesktopDir, e.Name()))
	}
}

// SharedDesktop returns the system service's GUI if it is shared with the
// current user and answers. Root never uses a share: it runs Herakles
// itself.
func SharedDesktop() (*DesktopShare, error) {
	uid := os.Getuid()
	if uid <= 0 {
		return nil, ErrNotRunning
	}
	b, err := os.ReadFile(DesktopPath(uid))
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
