// Package update finds new Herakles releases and installs them when the
// user asks (semi-automatic): the website publishes downloads/latest.json,
// signed with the release key below, and Install updates the way Herakles
// was installed — package manager, Windows setup, or the single file.
package update

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultURL is where releases are announced.
const DefaultURL = "https://herakles.kloeck-it.de/downloads/latest.json"

// releaseKey verifies latest.json (the private half signs it in
// website/deploy/deploy.sh and never leaves the release machine).
var releaseKey = ed25519.PublicKey{
	0xf3, 0xa8, 0x31, 0xd4, 0x7a, 0xe4, 0x3c, 0x5a, 0x9b, 0x3e, 0x3d, 0xf2, 0xf0, 0xce, 0x19, 0x8f,
	0x70, 0x7f, 0x40, 0xb1, 0xcd, 0x97, 0x13, 0x85, 0x7d, 0x71, 0xba, 0xb8, 0x57, 0xf1, 0xe8, 0xa6,
}

// File is one download of a release.
type File struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// Release is the content of latest.json.
type Release struct {
	Version  string    `json:"version"`
	Released time.Time `json:"released"`
	Notes    string    `json:"notes,omitempty"`
	// Files by platform: linux-amd64, linux-arm64, linux-arm,
	// windows-amd64.exe, windows-arm64.exe, darwin-arm64, windows-setup.exe.
	Files map[string]File `json:"files"`
}

var client = &http.Client{Timeout: 5 * time.Minute}

func get(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, limit))
}

// Check fetches the announced release and its signature (url + ".sig",
// base64) and accepts it only when the release key signed it.
func Check(ctx context.Context, url string) (*Release, error) {
	if url == "" {
		url = DefaultURL
	}
	body, err := get(ctx, url, 1<<20)
	if err != nil {
		return nil, err
	}
	sigText, err := get(ctx, url+".sig", 4096)
	if err != nil {
		return nil, err
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sigText)))
	if err != nil || !ed25519.Verify(releaseKey, body, sig) {
		return nil, errors.New("the release announcement is not signed with the Herakles release key; ignored")
	}
	var r Release
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, err
	}
	if _, ok := parse(r.Version); !ok {
		return nil, fmt.Errorf("bad version %q in the release announcement", r.Version)
	}
	return &r, nil
}

// parse reads vX.Y.Z (with an optional -suffix of a development build).
func parse(v string) ([3]int, bool) {
	var out [3]int
	v, _, _ = strings.Cut(strings.TrimPrefix(v, "v"), "-")
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// Valid reports whether v is a version (vX.Y.Z, also with the suffix of a
// development build after a tag).
func Valid(v string) bool { _, ok := parse(v); return ok }

// Newer reports whether release version latest is newer than current.
// Development builds (no version, or vX.Y.Z-N-gHASH after a tag) count as
// their tag, so they are only offered later releases.
func Newer(latest, current string) bool {
	l, ok1 := parse(latest)
	c, ok2 := parse(current)
	if !ok1 || !ok2 {
		return false
	}
	for i := range l {
		if l[i] != c[i] {
			return l[i] > c[i]
		}
	}
	return false
}

// download fetches f into dir and checks size and checksum.
func download(ctx context.Context, f File, dir, name string) (string, error) {
	if f.URL == "" || len(f.SHA256) != 64 {
		return "", errors.New("the release has no file for this system")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.URL, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("%s: %s", f.URL, resp.Status)
	}
	out, err := os.CreateTemp(dir, "."+name+"-*")
	if err != nil {
		return "", err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, h), io.LimitReader(resp.Body, 512<<20))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil && f.Size > 0 && n != f.Size {
		err = fmt.Errorf("download incomplete: %d of %d bytes", n, f.Size)
	}
	if err == nil && hex.EncodeToString(h.Sum(nil)) != strings.ToLower(f.SHA256) {
		err = errors.New("the download does not match the release checksum")
	}
	if err != nil {
		os.Remove(out.Name())
		return "", err
	}
	return out.Name(), nil
}

// State is what the interfaces show about updates.
type State struct {
	Current   string    `json:"current"`
	Latest    string    `json:"latest,omitempty"`
	Available bool      `json:"available"`
	Checked   time.Time `json:"checked,omitempty"`
	Error     string    `json:"error,omitempty"`
	// Method is how this installation updates; Hint explains it, or why
	// it cannot update itself.
	Method string `json:"method"`
	Hint   string `json:"hint,omitempty"`
	// Installing is set while an update runs.
	Installing bool `json:"installing,omitempty"`
}

// Checker looks for new releases in the background.
type Checker struct {
	URL     string
	Current string
	// Every is the time between checks (default 12h).
	Every time.Duration
	// OnNew is called once per newly found release.
	OnNew func(*Release)

	mu         sync.Mutex
	latest     *Release
	checked    time.Time
	err        error
	told       string
	installing bool
}

// Run checks now and then every c.Every until ctx ends.
func (c *Checker) Run(ctx context.Context) {
	every := c.Every
	if every <= 0 {
		every = 12 * time.Hour
	}
	// Leave the start of the session to the backups.
	select {
	case <-ctx.Done():
		return
	case <-time.After(20 * time.Second):
	}
	for {
		c.CheckNow(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		}
	}
}

// CheckNow checks once and returns the release if it is newer.
func (c *Checker) CheckNow(ctx context.Context) (*Release, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	r, err := Check(ctx, c.URL)
	c.mu.Lock()
	c.checked = time.Now()
	c.err = err
	if err == nil {
		c.latest = r
	}
	newRel := err == nil && Newer(r.Version, c.Current) && c.told != r.Version
	if newRel {
		c.told = r.Version
	}
	c.mu.Unlock()
	if newRel && c.OnNew != nil {
		c.OnNew(r)
	}
	if err != nil || !Newer(r.Version, c.Current) {
		return nil, err
	}
	return r, nil
}

// Available returns the newer release, if one was found.
func (c *Checker) Available() *Release {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.latest != nil && Newer(c.latest.Version, c.Current) {
		return c.latest
	}
	return nil
}

// SetInstalling marks an update in progress (false when it failed).
func (c *Checker) SetInstalling(v bool) {
	c.mu.Lock()
	c.installing = v
	c.mu.Unlock()
}

// State reports the checker's view, with the update method m.
func (c *Checker) State(m Method) State {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := State{Current: c.Current, Checked: c.checked, Method: m.Kind, Hint: m.Hint, Installing: c.installing}
	if c.err != nil {
		st.Error = c.err.Error()
	}
	if c.latest != nil {
		st.Latest = c.latest.Version
		st.Available = Newer(c.latest.Version, c.Current)
	}
	return st
}

// Method is how this installation of Herakles is updated.
type Method struct {
	// Kind: "package" (apt, dnf, zypper, pacman), "setup" (Windows
	// setup), "file" (replace the program file) or "manual".
	Kind    string
	Manager string // package manager
	Exe     string // the running program
	Hint    string
}

// CanInstall reports whether Install can do the update itself.
func (m Method) CanInstall() bool { return m.Kind != "manual" && m.Kind != "" }

// platformFile is the release file for replacing the program itself.
func platformFile(goos, goarch string) string {
	name := goos + "-" + goarch
	if goos == "windows" {
		name += ".exe"
	}
	return name
}

// writable reports whether files can be created next to path.
func writable(path string) bool {
	f, err := os.CreateTemp(filepath.Dir(path), ".herakles-write-test-*")
	if err != nil {
		return false
	}
	f.Close()
	os.Remove(f.Name())
	return true
}

// replaceFile swaps the program file for the downloaded one. The running
// program keeps the old file open (Windows: renamed to .old, removed at the
// next start by CleanOld).
func replaceFile(exe, fresh string) error {
	if err := os.Chmod(fresh, 0o755); err != nil {
		return err
	}
	old := exe + ".old"
	os.Remove(old)
	if err := os.Rename(exe, old); err != nil {
		return err
	}
	if err := os.Rename(fresh, exe); err != nil {
		_ = os.Rename(old, exe)
		return err
	}
	_ = os.Remove(old) // fails on Windows while the old program runs
	return nil
}

// CleanOld removes the program file an earlier update left behind.
func CleanOld() {
	if exe, err := os.Executable(); err == nil {
		_ = os.Remove(exe + ".old")
	}
}
