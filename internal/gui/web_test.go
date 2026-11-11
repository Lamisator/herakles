package gui

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lamisator/herakles/internal/config"
	"github.com/lamisator/herakles/internal/docker"
	"github.com/lamisator/herakles/internal/docker/dockertest"
	"github.com/lamisator/herakles/internal/engine"
	tu "github.com/lamisator/herakles/internal/testutil"
)

type webClient struct {
	t    *testing.T
	base string
	host string
	c    *http.Client
}

func (w *webClient) do(method, path string, body any, hdr ...string) (int, map[string]any, string) {
	w.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, w.base+path, rd)
	req.Host = w.host
	req.Header.Set("X-Herakles", "1")
	for i := 0; i+1 < len(hdr); i += 2 {
		if hdr[i] == "Host" {
			req.Host = hdr[i+1]
			continue
		}
		if hdr[i+1] == "" {
			req.Header.Del(hdr[i])
		} else {
			req.Header.Set(hdr[i], hdr[i+1])
		}
	}
	resp, err := w.c.Do(req)
	if err != nil {
		w.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return resp.StatusCode, m, string(raw)
}

func newWebClient(t *testing.T, srv *httptest.Server) *webClient {
	jar, _ := cookiejar.New(nil)
	return &webClient{t: t, base: srv.URL, host: "backup.test", c: &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func setupWeb(t *testing.T) (*Server, *httptest.Server, *engine.Engine, *dockertest.Fake) {
	t.Helper()
	dir := t.TempDir()
	fake := dockertest.New(t)
	wd := filepath.Join(dir, "srv", "wiki")
	os.MkdirAll(wd, 0o755)
	os.WriteFile(filepath.Join(wd, "compose.yml"), []byte("services: {}\n"), 0o644)
	fake.Set(dockertest.Container{Name: "wiki-db-1", Image: "postgres:16", Project: "wiki", Service: "db", WorkingDir: wd,
		ConfigFiles: []string{filepath.Join(wd, "compose.yml")}, Running: true,
		Mounts: []docker.Mount{{Type: "bind", Source: filepath.Join(wd, "pg"), Destination: "/var/lib/postgresql/data"}}})
	os.MkdirAll(filepath.Join(wd, "pg"), 0o700)
	// A project directory without containers (e.g. an archived stack).
	os.MkdirAll(filepath.Join(dir, "srv", "archived"), 0o755)
	os.WriteFile(filepath.Join(dir, "srv", "archived", "compose.yml"), []byte("services: {}\n"), 0o644)
	cfg := config.New(filepath.Join(dir, "h.yaml"))
	cfg.StateDir = filepath.Join(dir, "state")
	cfg.Daemon.ControlListen = "127.0.0.1:1"
	rc := &config.Repository{Name: "vault", Location: filepath.Join(dir, "repo"), Password: tu.Password.Password}
	cfg.Repositories = []*config.Repository{rc}
	cfg.Jobs = []*config.Job{{Name: "containers", Repository: "vault", Docker: &config.DockerJob{Binary: fake.Bin, ScanDirs: []string{filepath.Join(dir, "srv")}}}}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	eng := engine.New(cfg, nil)
	if _, err := eng.Init(context.Background(), rc, tu.Password); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s, err := newServer(ctx, eng, Options{Web: &WebOptions{URL: "http://backup.test", Accounts: filepath.Join(dir, "state", "web", "accounts.json")}})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return s, srv, eng, fake
}

func TestWebAuth(t *testing.T) {
	s, srv, _, _ := setupWeb(t)
	c := newWebClient(t, srv)

	if code, m, _ := c.do("GET", "/auth/state", nil); code != 200 || m["setup"] != true {
		t.Fatal(code, m)
	}
	if code, _, _ := c.do("GET", "/api/jobs", nil); code != 401 {
		t.Fatal("API without login:", code)
	}
	if code, _, _ := c.do("GET", "/", nil); code != http.StatusFound {
		t.Fatal("page without login should redirect:", code)
	}
	if code, _, _ := c.do("GET", "/login.html", nil); code != 200 {
		t.Fatal("login page must be public:", code)
	}
	link, _ := os.ReadFile(filepath.Join(filepath.Dir(s.web.store.Path()), "setup-link"))
	if !strings.Contains(string(link), "#setup="+s.web.setupToken) {
		t.Fatal("setup link file:", string(link))
	}
	if code, _, _ := c.do("POST", "/auth/setup", map[string]string{"token": "wrong", "name": "admin", "password": "long password 1"}); code != 403 {
		t.Fatal("setup with a wrong token:", code)
	}
	if code, m, _ := c.do("POST", "/auth/setup", map[string]string{"token": s.web.setupToken, "name": "admin", "password": "long password 1"}); code != 200 || m["role"] != "admin" {
		t.Fatal(code, m)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(s.web.store.Path()), "setup-link")); !os.IsNotExist(err) {
		t.Fatal("setup link must be removed after use")
	}
	if code, m, _ := c.do("GET", "/api/info", nil); code != 200 || m["mode"] != "web" {
		t.Fatal("logged in after setup:", code, m)
	}
	other := newWebClient(t, srv)
	if code, _, _ := other.do("POST", "/auth/setup", map[string]string{"token": "", "name": "evil", "password": "long password 2"}); code != 403 {
		t.Fatal("second setup:", code)
	}

	// Host and CSRF protection.
	if code, _, _ := c.do("GET", "/api/info", nil, "Host", "evil.example"); code != http.StatusMisdirectedRequest {
		t.Fatal("foreign Host accepted:", code)
	}
	if code, _, _ := c.do("POST", "/api/users", map[string]string{"name": "x"}, "X-Herakles", ""); code != 403 {
		t.Fatal("POST without CSRF header:", code)
	}

	// Admin creates a user; the user logs in.
	if code, m, _ := c.do("POST", "/api/users", map[string]string{"name": "alex", "role": "user", "password": "alex password"}); code != 200 {
		t.Fatal(code, m)
	}
	u := newWebClient(t, srv)
	if code, _, _ := u.do("POST", "/auth/login", map[string]string{"name": "alex", "password": "wrong"}); code != 401 {
		t.Fatal(code)
	}
	if code, _, _ := u.do("POST", "/auth/login", map[string]string{"name": "alex", "password": "alex password"}); code != 200 {
		t.Fatal(code)
	}
	if code, m, _ := u.do("GET", "/api/info", nil); code != 200 || m["user"].(map[string]any)["name"] != "alex" {
		t.Fatal(code, m)
	}
	// Plain users: no secrets, no configuration changes, no user admin.
	_, _, raw := u.do("GET", "/api/config", nil)
	if strings.Contains(raw, tu.Password.Password) || !strings.Contains(raw, `"password":"***"`) {
		t.Fatal("config not redacted for users:", raw)
	}
	_, _, raw = c.do("GET", "/api/config", nil)
	if !strings.Contains(raw, tu.Password.Password) {
		t.Fatal("admins see the full configuration")
	}
	for _, req := range [][2]string{{"PUT", "/api/config"}, {"GET", "/api/config/yaml"}, {"GET", "/api/users"}, {"POST", "/api/repos/vault/init"}, {"DELETE", "/api/repos/vault/snapshots/abcd"}} {
		if code, _, _ := u.do(req[0], req[1], map[string]any{}); code != 403 {
			t.Fatalf("user may not %s %s: %d", req[0], req[1], code)
		}
	}
	if code, _, _ := u.do("GET", "/api/jobs", nil); code != 200 {
		t.Fatal("users may see jobs:", code)
	}

	// Passkey ceremonies start with the right relying party.
	if code, m, _ := u.do("POST", "/api/account/passkeys/begin", nil); code != 200 || m["publicKey"].(map[string]any)["rp"].(map[string]any)["id"] != "backup.test" {
		t.Fatal("registration options:", code, m)
	}
	if code, m, _ := other.do("POST", "/auth/passkey/begin", nil); code != 200 || m["publicKey"].(map[string]any)["challenge"] == "" {
		t.Fatal("login options:", code, m)
	}
	if code, _, _ := other.do("POST", "/auth/passkey/finish", map[string]any{"id": "x"}); code != 401 {
		t.Fatal("bogus passkey assertion:", code)
	}

	// Disabling a user ends their sessions; the last admin is protected.
	var users []map[string]any
	_, _, raw = c.do("GET", "/api/users", nil)
	_ = json.Unmarshal([]byte(raw), &users)
	alexID := users[1]["id"]
	if code, _, _ := c.do("PATCH", "/api/users/"+jsonNum(alexID), map[string]any{"disabled": true}); code != 200 {
		t.Fatal(code)
	}
	if code, _, _ := u.do("GET", "/api/jobs", nil); code != 401 {
		t.Fatal("disabled user still signed in:", code)
	}
	if code, _, _ := c.do("PATCH", "/api/users/"+jsonNum(users[0]["id"]), map[string]any{"role": "user"}); code != 400 {
		t.Fatal("last admin demoted:", code)
	}
	if code, _, _ := c.do("POST", "/auth/logout", nil); code != 200 {
		t.Fatal(code)
	}
	if code, _, _ := c.do("GET", "/api/jobs", nil); code != 401 {
		t.Fatal("still signed in after logout:", code)
	}
}

func jsonNum(v any) string { b, _ := json.Marshal(v); return string(b) }

func TestWebLoginThrottle(t *testing.T) {
	s, srv, _, _ := setupWeb(t)
	if _, err := s.web.store.Create("admin", "admin", "long password 1"); err != nil {
		t.Fatal(err)
	}
	c := newWebClient(t, srv)
	for i := 0; i < 5; i++ {
		c.do("POST", "/auth/login", map[string]string{"name": "admin", "password": "guess"})
	}
	if code, m, _ := c.do("POST", "/auth/login", map[string]string{"name": "admin", "password": "long password 1"}); code != 429 {
		t.Fatal("no lockout after 5 failures:", code, m)
	}
}

func TestWebDocker(t *testing.T) {
	s, srv, _, fake := setupWeb(t)
	s.web.store.Create("admin", "admin", "long password 1")
	c := newWebClient(t, srv)
	c.do("POST", "/auth/login", map[string]string{"name": "admin", "password": "long password 1"})

	var jobs []dockerJobView
	_, _, raw := c.do("GET", "/api/docker", nil)
	if err := json.Unmarshal([]byte(raw), &jobs); err != nil || len(jobs) != 1 || len(jobs[0].Projects) != 2 {
		t.Fatal(raw)
	}
	if !strings.Contains(raw, `"name":"archived"`) || strings.Contains(raw, `"containers":null`) {
		t.Fatal("projects without containers must have an empty list, not null:", raw)
	}
	p := jobs[0].Projects[1]
	if p.Name != "wiki" || p.Mode != "dump" || p.Running != 1 || p.Last != nil {
		t.Fatalf("%+v", p)
	}
	if code, _, raw := c.do("POST", "/api/docker/containers/wiki/backup", map[string]string{}); code != 202 {
		t.Fatal(code, raw)
	}
	waitDone := func() {
		for i := 0; i < 200; i++ {
			if len(s.runner.Active()) == 0 {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatal("task did not finish")
	}
	waitDone()
	_, _, raw = c.do("GET", "/api/docker/containers/wiki/snapshots", nil)
	var snaps []projSnapView
	if err := json.Unmarshal([]byte(raw), &snaps); err != nil || len(snaps) != 1 || len(snaps[0].Dumps) != 1 {
		t.Fatal(raw)
	}
	if code, _, _ := c.do("POST", "/api/docker/containers/wiki/restore", map[string]string{"snapshot": snaps[0].ID}); code != 400 {
		t.Fatal("restore without typed confirmation:", code)
	}
	if code, _, raw := c.do("POST", "/api/docker/containers/wiki/restore", map[string]string{"snapshot": snaps[0].ID, "confirm": "wiki"}); code != 202 {
		t.Fatal(code, raw)
	}
	waitDone()
	recent := s.runner.Recent()
	if recent[0].Kind != "restore" || recent[0].Error != "" || recent[0].Restore == nil || len(recent[0].Restore.Loaded) != 1 {
		t.Fatalf("restore run: %+v", recent[0].Run)
	}
	if !strings.Contains(fake.Loaded("wiki-db-1"), "postgres dump of wiki-db-1") {
		t.Fatal("dump not loaded")
	}
}
