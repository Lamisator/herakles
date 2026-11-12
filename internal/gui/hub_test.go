package gui

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lamisator/herakles/internal/config"
	"github.com/lamisator/herakles/internal/engine"
	"github.com/lamisator/herakles/internal/hub"
)

// TestWebHub runs a hub's web interface with one attached server and
// checks logins and roles on the forwarded requests.
func TestWebHub(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dir := t.TempDir()
	cfg := config.New(filepath.Join(dir, "hub.yaml"))
	cfg.StateDir = filepath.Join(dir, "state")
	cfg.Daemon.ControlListen = "127.0.0.1:1"
	cfg.Web = config.Web{Listen: "127.0.0.1:0", URL: "http://backup.test"}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	eng := engine.New(cfg, nil)
	hb := hub.New(ctx, hub.OpenStore(hub.StorePath(cfg.StateDir)), eng.Log, eng.Config)
	s, err := newServer(ctx, eng, Options{Hub: hb, Web: &WebOptions{URL: "http://backup.test", Accounts: filepath.Join(dir, "state", "web", "accounts.json")}})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	// The attached server, with a secret in its configuration.
	adir := t.TempDir()
	acfg := config.New(filepath.Join(adir, "c.yaml"))
	acfg.StateDir = filepath.Join(adir, "state")
	acfg.Daemon.ControlListen = "127.0.0.1:1"
	acfg.Repositories = []*config.Repository{{Name: "nas", Location: filepath.Join(adir, "repo"), Password: "agent-secret"}}
	acfg.Jobs = []*config.Job{{Name: "etc", Repository: "nas", Paths: []string{"/etc/hostname"}}}
	if err := acfg.Save(); err != nil {
		t.Fatal(err)
	}
	aeng := engine.New(acfg, nil)
	code, err := hb.Store.NewEnrollment(srv.URL, "web-1", time.Hour, nil)
	if err != nil {
		t.Fatal(err)
	}
	creds, err := hub.Enroll(ctx, &code, config.Agent{}, hub.Hello{Hostname: "web-1"})
	if err != nil {
		t.Fatal("enrollment must not need a web login:", err)
	}
	if err := hub.SaveCredentials(acfg.StateDir, creds); err != nil {
		t.Fatal(err)
	}
	acfg.Agent.Hub = srv.URL
	ag := &hub.Agent{Config: aeng.Config, Version: "test", Log: aeng.Log,
		Handler: NewRemote(ctx, aeng, Options{Service: true}),
		Apply:   func([]hub.SharedRepository) (hub.SyncResult, error) { return hub.SyncResult{}, nil }}
	go ag.Run(ctx)

	c := newWebClient(t, srv)
	if code, _, _ := c.do("GET", "/api/hub", nil); code != 401 {
		t.Fatal("hub API without login:", code)
	}
	if code, _, _ := c.do("POST", "/auth/setup", map[string]string{"token": s.web.setupToken, "name": "admin", "password": "long password 1"}); code != 200 {
		t.Fatal("setup", code)
	}
	deadline := time.Now().Add(10 * time.Second)
	var servers []any
	for time.Now().Before(deadline) {
		_, m, _ := c.do("GET", "/api/hub", nil)
		servers, _ = m["servers"].([]any)
		if len(servers) == 1 && servers[0].(map[string]any)["online"] == true {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(servers) != 1 || servers[0].(map[string]any)["online"] != true {
		t.Fatal("server not online:", servers)
	}
	base := "/api/hub/servers/" + creds.ID
	if code, _, raw := c.do("GET", base+"/api/jobs", nil); code != 200 || !strings.Contains(raw, `"etc"`) {
		t.Fatal("jobs of the server:", code, raw)
	}
	if code, _, raw := c.do("GET", base+"/api/config", nil); code != 200 || !strings.Contains(raw, "agent-secret") {
		t.Fatal("admins see the server's configuration:", code)
	}
	if code, m, _ := c.do("GET", base+"/api/info", nil); code != 200 || m["hostname"] == "" {
		t.Fatal("info", code, m)
	}
	if code, _, _ := c.do("GET", "/api/hub/servers/nope/api/jobs", nil); code != 502 {
		t.Fatal("unknown server:", code)
	}
	if code, _, _ := c.do("GET", base+"/hub/hello", nil); code == 200 {
		t.Fatal("the server's internal hub routes must not be reachable from the GUI")
	}
	if code, m, _ := c.do("POST", "/api/hub/enroll", map[string]string{"name": "db"}); code != 200 || !strings.HasPrefix(m["code"].(string), hub.CodePrefix) {
		t.Fatal("enroll", code, m)
	}

	// A plain user: no secrets, no changes, no enrollment.
	if code, _, _ := c.do("POST", "/api/users", map[string]string{"name": "alex", "role": "user", "password": "alex password"}); code != 200 {
		t.Fatal("create user", code)
	}
	u := newWebClient(t, srv)
	if code, _, _ := u.do("POST", "/auth/login", map[string]string{"name": "alex", "password": "alex password"}); code != 200 {
		t.Fatal("login", code)
	}
	if code, _, raw := u.do("GET", base+"/api/config", nil); code != 200 || strings.Contains(raw, "agent-secret") {
		t.Fatal("users must get the server's configuration without secrets:", code)
	}
	if code, _, _ := u.do("PUT", base+"/api/config", map[string]any{}); code != 403 {
		t.Fatal("users must not change the server's configuration:", code)
	}
	if code, _, _ := u.do("GET", base+"/api/wizard/devices", nil); code != 403 {
		t.Fatal("users must not list the server's disks:", code)
	}
	if code, _, _ := u.do("POST", "/api/hub/enroll", map[string]string{}); code != 403 {
		t.Fatal("users must not enroll servers:", code)
	}
	if code, _, _ := u.do("DELETE", "/api/hub/servers/"+creds.ID, nil); code != 403 {
		t.Fatal("users must not remove servers:", code)
	}
	if code, _, _ := u.do("GET", base+"/api/jobs", nil); code != 200 {
		t.Fatal("users may see the server's jobs:", code)
	}

	if code, _, _ := c.do("DELETE", "/api/hub/servers/"+creds.ID, nil); code != 200 {
		t.Fatal("remove", code)
	}
	if code, _, _ := c.do("GET", base+"/api/jobs", nil); code != 502 {
		t.Fatal("removed server still reachable:", code)
	}
}
