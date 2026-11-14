package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/user"
	"runtime"
	"sync/atomic"
	"testing"
)

// After the service restarts (an update) it writes a new token: a running
// tray picks it up instead of failing with 401.
func TestDesktopShareReloadsToken(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("share files are owned by a SID")
	}
	var token atomic.Value
	token.Store("old")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token.Load().(string) {
			w.WriteHeader(401)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"ticket": "t1"})
	}))
	defer srv.Close()
	DesktopDir = t.TempDir()
	u, _ := user.Current()
	write := func(tok string) {
		b, _ := json.Marshal(DesktopShare{GUI: srv.URL, Token: tok})
		if err := os.WriteFile(DesktopPath(u.Uid), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("old")
	sh, err := readShare()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sh.OpenURL(); err != nil {
		t.Fatal(err)
	}
	token.Store("new")
	write("new")
	if u, err := sh.OpenURL(); err != nil || u != srv.URL+"/?ticket=t1" {
		t.Fatalf("%q %v", u, err)
	}
	token.Store("gone")
	if _, err := sh.OpenURL(); err == nil {
		t.Fatal("stale token accepted")
	}
}
