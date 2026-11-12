package hub

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/lamisator/herakles/internal/config"
)

func testLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// hubConfig is the hub's configuration with one shared repository.
func hubConfig(t *testing.T) *config.Config {
	dir := t.TempDir()
	pw := filepath.Join(dir, "repo.pw")
	if err := os.WriteFile(pw, []byte("s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := config.New(filepath.Join(dir, "hub.yaml"))
	c.StateDir = filepath.Join(dir, "state")
	c.Repositories = []*config.Repository{
		{Name: "box", Location: "sftp://u@box.example/backup", PasswordFile: pw,
			Remote:      config.Remote{Username: "u", Password: "remote-pw"},
			Maintenance: config.Maintenance{PruneSchedule: "0 4 * * 0"}},
		{Name: "local", Location: "/srv/backup", Password: "x"},
	}
	c.Hub.Repositories = []string{"box"}
	return c
}

type agentEnv struct {
	mu  sync.Mutex
	cfg *config.Config
}

func (e *agentEnv) get() *config.Config {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.cfg
}

func TestHubRoundTrip(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hc := hubConfig(t)
	store := OpenStore(StorePath(hc.GetStateDir()))
	h := New(ctx, store, testLog(), func() *config.Config { return hc })
	mux := http.NewServeMux()
	mux.Handle("/hub/", h.Handler())
	mux.HandleFunc("/proxy/{id}/", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		h.Proxy(w, r, id, strings.TrimPrefix(r.URL.EscapedPath(), "/proxy/"+id), "alice", "user")
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	code, err := store.NewEnrollment(ts.URL, "", time.Hour, nil)
	if err != nil {
		t.Fatal(err)
	}
	// The code survives text round trips and catches damage.
	parsed, err := ParseCode(" " + code.Encode()[:20] + "\n" + code.Encode()[20:])
	if err != nil || parsed.Secret != code.Secret || parsed.URL != ts.URL {
		t.Fatalf("parse %+v %v", parsed, err)
	}
	enc := []byte(code.Encode())
	enc[30] ^= 1
	if _, err := ParseCode(string(enc)); err == nil {
		t.Fatal("a damaged code was accepted")
	}
	// A wrong secret is refused.
	bad := *parsed
	bad.Secret = "nope"
	if _, err := Enroll(ctx, &bad, config.Agent{}, Hello{Hostname: "web-1"}); err == nil {
		t.Fatal("enrolled with a wrong secret")
	}
	// A code naming another hub key: the answer is not trusted (and the
	// secret is used up, as an attacker in between would have done).
	impostor := *parsed
	impostor.HubKey = make([]byte, 32)
	if _, err := Enroll(ctx, &impostor, config.Agent{}, Hello{Hostname: "web-1"}); err == nil || !strings.Contains(err.Error(), "not signed") {
		t.Fatalf("answer from the wrong hub accepted: %v", err)
	}
	code, _ = store.NewEnrollment(ts.URL, "", time.Hour, nil)
	creds, err := Enroll(ctx, &code, config.Agent{}, Hello{Hostname: "web-1"})
	if err != nil {
		t.Fatal(err)
	}
	if creds.Name != "web-1-2" && creds.Name != "web-1" {
		t.Fatalf("name %q", creds.Name)
	}
	if _, err := Enroll(ctx, &code, config.Agent{}, Hello{Hostname: "web-2"}); err == nil {
		t.Fatal("a code worked twice")
	}
	// A recorded login cannot be used again.
	key, _ := creds.privateKey()
	auth, _ := connectAuth(creds.ID, key, creds.HubKey, time.Now())
	r1, _ := http.NewRequest("GET", "/", nil)
	r1.Header.Set(AuthHeader, auth)
	if _, _, err := h.authenticate(r1, creds.HubKey); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.authenticate(r1, creds.HubKey); err == nil {
		t.Fatal("replayed login accepted")
	}
	old, _ := connectAuth(creds.ID, key, creds.HubKey, time.Now().Add(-time.Hour))
	r1.Header.Set(AuthHeader, old)
	if _, _, err := h.authenticate(r1, creds.HubKey); err == nil {
		t.Fatal("old login accepted")
	}

	// The attached server.
	adir := t.TempDir()
	ac := config.New(filepath.Join(adir, "config.yaml"))
	ac.StateDir = filepath.Join(adir, "state")
	ac.Agent.Hub = ts.URL
	ac.Repositories = []*config.Repository{{Name: "local", Location: "/backup", Password: "mine"}}
	if err := SaveCredentials(ac.GetStateDir(), creds); err != nil {
		t.Fatal(err)
	}
	env := &agentEnv{cfg: ac}
	var seen http.Header
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
		if r.URL.Path == "/api/page" {
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, "<script>alert(1)</script>")
			return
		}
		writeJSON(w, 200, map[string]string{"path": r.URL.Path, "query": r.URL.RawQuery})
	})
	applied := make(chan SyncResult, 4)
	ag := &Agent{Config: env.get, Handler: api, Version: "test", Log: testLog(),
		Apply: func(shared []SharedRepository) (SyncResult, error) {
			env.mu.Lock()
			defer env.mu.Unlock()
			c := env.cfg.Clone()
			res, err := Apply(c, shared)
			env.cfg = c
			applied <- res
			return res, err
		}}
	go ag.Run(ctx)

	var res SyncResult
	select {
	case res = <-applied:
	case <-time.After(10 * time.Second):
		t.Fatal("the hub did not pass the repositories on")
	}
	if len(res.Added) != 1 || res.Added[0] != "box" {
		t.Fatalf("sync %+v", res)
	}
	box := env.get().Repo("box")
	if box == nil || !box.FromHub || box.Maintenance.PruneSchedule != "" || box.Remote.Password != "remote-pw" {
		t.Fatalf("shared repository %+v", box)
	}
	if c, err := box.Credentials(); err != nil || c.Password != "s3cret" {
		t.Fatalf("password %q %v", c.Password, err)
	}
	if st := ag.State(); st == nil || !st.Connected {
		t.Fatalf("state %+v", st)
	}

	// Requests through the tunnel: path and query arrive, the hub's
	// cookies do not, and the role does.
	req, _ := http.NewRequest("GET", ts.URL+"/proxy/"+creds.ID+"/api/repos/a%2Fb/snapshots?x=1", nil)
	req.Header.Set("Cookie", "herakles_web=secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), `"query":"x=1"`) || !strings.Contains(string(body), `/api/repos/a/b/snapshots`) {
		t.Fatalf("proxied answer %s", body)
	}
	if seen.Get("Cookie") != "" || seen.Get(RoleHeader) != "user" || seen.Get(UserHeader) != "alice" {
		t.Fatalf("forwarded headers %v", seen)
	}
	// A server cannot serve pages under the hub's address.
	resp, err = http.Get(ts.URL + "/proxy/" + creds.ID + "/api/page")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); strings.Contains(ct, "html") || !strings.Contains(resp.Header.Get("Content-Security-Policy"), "sandbox") {
		t.Fatalf("unsafe answer headers %v", resp.Header)
	}

	// Unsharing removes the repository on the server.
	hc.Hub.Repositories = nil
	h.PushAll()
	select {
	case res = <-applied:
	case <-time.After(10 * time.Second):
		t.Fatal("no second sync")
	}
	if len(res.Removed) != 1 || env.get().Repo("box") != nil {
		t.Fatalf("after unsharing %+v", res)
	}

	// Removing the server ends its connection and its login.
	if err := h.Remove(creds.ID); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for ag.State().Connected && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if ag.State().Connected {
		t.Fatal("still connected after removal")
	}
	auth, _ = connectAuth(creds.ID, key, creds.HubKey, time.Now())
	r1.Header.Set(AuthHeader, auth)
	if _, _, err := h.authenticate(r1, creds.HubKey); err == nil {
		t.Fatal("login still works after removal")
	}
}

func TestApplyConflictsAndKept(t *testing.T) {
	dir := t.TempDir()
	c := config.New(filepath.Join(dir, "c.yaml"))
	c.StateDir = dir
	c.Repositories = []*config.Repository{
		{Name: "nas", Location: "/nas", Password: "local"},
		{Name: "old", Location: "/old", PasswordFile: "/x", FromHub: true},
	}
	c.Jobs = []*config.Job{{Name: "home", Repository: "old", Paths: []string{"/home"}}}
	shared := []SharedRepository{
		{Repository: &config.Repository{Name: "nas", Location: "/hub-nas"}, Password: "p"},
		{Repository: &config.Repository{Name: "../evil", Location: "/x"}, Password: "p"},
	}
	res, err := Apply(c, shared)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Conflicts) != 1 || c.Repo("nas").Location != "/nas" {
		t.Fatalf("local repository overwritten: %+v", res)
	}
	if len(res.Kept) != 1 || c.Repo("old") == nil {
		t.Fatalf("used repository removed: %+v", res)
	}
	if c.Repo("../evil") != nil {
		t.Fatal("unsafe name accepted")
	}
	if got := Release(c); len(got) != 1 || c.Repo("old").FromHub {
		t.Fatalf("release %v", got)
	}
}

func TestSharedNeedsPassword(t *testing.T) {
	c := config.New("")
	c.Repositories = []*config.Repository{{Name: "r", Location: "/r"}}
	c.Hub.Repositories = []string{"r"}
	t.Setenv("HERAKLES_PASSWORD", "")
	if _, err := Shared(c); err == nil {
		t.Fatal("a repository without password source was shared")
	}
}

// A server refuses a hub that cannot sign with the enrolled key.
func TestAgentRefusesImpostorHub(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	real := OpenStore(StorePath(t.TempDir()))
	fake := OpenStore(StorePath(t.TempDir()))
	hc := hubConfig(t)
	h := New(ctx, real, testLog(), func() *config.Config { return hc })
	ts := httptest.NewServer(h.Handler())
	defer ts.Close()
	code, _ := real.NewEnrollment(ts.URL, "", time.Hour, nil)
	creds, err := Enroll(ctx, &code, config.Agent{}, Hello{Hostname: "a"})
	if err != nil {
		t.Fatal(err)
	}
	// The impostor accepts any login but signs with another key. (A hub
	// with another key would also refuse the login itself: the server
	// signs the hub key it expects.)
	fakeKey, _ := fake.Identity()
	fts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pa, _ := parseAuth(r.Header.Get(AuthHeader))
		w.Header().Set(HubSigHeader, b64.EncodeToString(ed25519.Sign(fakeKey, hubMessage(pa.id, pa.nonce))))
		ws, err := websocket.Accept(w, r, nil)
		if err == nil {
			defer ws.CloseNow()
			time.Sleep(time.Second)
		}
	}))
	defer fts.Close()
	creds.Hub = fts.URL
	ac := config.New(filepath.Join(t.TempDir(), "c.yaml"))
	ac.StateDir = t.TempDir()
	ac.Agent.Hub = fts.URL
	ag := &Agent{Config: func() *config.Config { return ac }, Handler: http.NotFoundHandler(), Log: testLog()}
	err = ag.session(ctx, ac, creds, make(chan struct{}))
	if err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("connected to an impostor: %v", err)
	}
}

// A hub with its own self-signed certificate is trusted through the pin
// in the enrollment code, and only through it.
func TestEnrollPinnedTLS(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := OpenStore(StorePath(t.TempDir()))
	hc := hubConfig(t)
	h := New(ctx, store, testLog(), func() *config.Config { return hc })
	ts := httptest.NewTLSServer(h.Handler())
	defer ts.Close()
	pin := sha256.Sum256(ts.Certificate().RawSubjectPublicKeyInfo)
	unpinned, _ := store.NewEnrollment(ts.URL, "", time.Hour, nil)
	if _, err := Enroll(ctx, &unpinned, config.Agent{}, Hello{Hostname: "a"}); err == nil {
		t.Fatal("untrusted certificate accepted without a pin")
	}
	code, _ := store.NewEnrollment(ts.URL, "", time.Hour, pin[:])
	c2, err := ParseCode(code.Encode())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Enroll(ctx, c2, config.Agent{}, Hello{Hostname: "a"}); err != nil {
		t.Fatal(err)
	}
}
