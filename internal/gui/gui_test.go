package gui

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lamisator/herakles/internal/config"
	"github.com/lamisator/herakles/internal/engine"
	tu "github.com/lamisator/herakles/internal/testutil"
	"github.com/lamisator/herakles/internal/update"
)

type client struct {
	t   *testing.T
	url string
	tok string
}

func (c *client) do(method, path string, body any) (int, []byte) {
	c.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.url+path, rd)
	req.Header.Set("Authorization", "Bearer "+c.tok)
	req.Header.Set("X-Herakles", "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, data
}

func (c *client) json(method, path string, body, out any) int {
	c.t.Helper()
	code, data := c.do(method, path, body)
	if out != nil && code < 300 {
		if err := json.Unmarshal(data, out); err != nil {
			c.t.Fatalf("%s %s: %v: %s", method, path, err, data)
		}
	}
	return code
}

func setup(t *testing.T, withPassword bool) (*client, *Server, string) {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	tu.MakeTree(t, src)
	cfg := config.New(filepath.Join(dir, "h.yaml"))
	cfg.StateDir = filepath.Join(dir, "state")
	cfg.Daemon.ControlListen = "127.0.0.1:1"
	rc := &config.Repository{Name: "vault", Location: filepath.Join(dir, "repo")}
	if withPassword {
		rc.Password = tu.Password.Password
	}
	cfg.Repositories = []*config.Repository{rc}
	cfg.Jobs = []*config.Job{{Name: "docs", Repository: "vault", Paths: []string{src}}}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	eng := engine.New(cfg, nil)
	if _, err := eng.Init(context.Background(), rc, tu.Password); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s := NewServer(ctx, eng, "test")
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return &client{t: t, url: ts.URL, tok: s.Token()}, s, src
}

func waitIdle(t *testing.T, c *client) {
	t.Helper()
	for i := 0; i < 400; i++ {
		var runs struct{ Active []any }
		c.json("GET", "/api/runs", nil, &runs)
		if len(runs.Active) == 0 {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("runs did not finish")
}

func TestGuards(t *testing.T) {
	c, _, _ := setup(t, true)
	resp, _ := http.Get(c.url + "/api/info")
	if resp.StatusCode != 401 {
		t.Fatalf("no token: %d", resp.StatusCode)
	}
	// Token in URL sets a session cookie and redirects.
	cl := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, _ = cl.Get(c.url + "/?token=" + c.tok)
	if resp.StatusCode != http.StatusFound || len(resp.Cookies()) == 0 {
		t.Fatalf("token login: %d", resp.StatusCode)
	}
	req, _ := http.NewRequest("GET", c.url+"/", nil)
	req.AddCookie(resp.Cookies()[0])
	resp, _ = http.DefaultClient.Do(req)
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !strings.Contains(string(body), "Herakles") {
		t.Fatalf("index with cookie: %d", resp.StatusCode)
	}
	// State changing requests need the CSRF header.
	req, _ = http.NewRequest("POST", c.url+"/api/jobs/docs/run", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+c.tok)
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != 403 {
		t.Fatalf("csrf: %d", resp.StatusCode)
	}
	// DNS rebinding protection.
	req, _ = http.NewRequest("GET", c.url+"/api/info", nil)
	req.Host = "evil.example.com"
	req.Header.Set("Authorization", "Bearer "+c.tok)
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != 403 {
		t.Fatalf("host check: %d", resp.StatusCode)
	}
	if err := Run(context.Background(), nil, Options{Listen: "0.0.0.0:0"}); err == nil {
		t.Fatal("non-loopback listen accepted")
	}
}

func TestBackupBrowseRestoreDownload(t *testing.T) {
	c, _, src := setup(t, true)
	if code := c.json("POST", "/api/jobs/docs/run", map[string]string{"type": "full"}, nil); code != 202 {
		t.Fatalf("run: %d", code)
	}
	waitIdle(t, c)
	var jobs []struct {
		Name string
		Last *engine.JobResult
	}
	c.json("GET", "/api/jobs", nil, &jobs)
	if jobs[0].Last == nil || jobs[0].Last.Status != "success" || jobs[0].Last.Type != "full" {
		t.Fatalf("job result: %+v", jobs[0].Last)
	}
	var snaps []struct{ ID string }
	c.json("GET", "/api/repos/vault/snapshots", nil, &snaps)
	if len(snaps) != 1 {
		t.Fatal("snapshots")
	}
	sid := snaps[0].ID
	var entries []treeEntry
	c.json("GET", "/api/repos/vault/snapshots/"+sid+"/tree?path="+src, nil, &entries)
	names := map[string]string{}
	for _, e := range entries {
		names[e.Name] = e.Type
	}
	if names["sub"] != "dir" || names["small.txt"] != "file" {
		t.Fatalf("tree: %v", names)
	}
	code, data := c.do("GET", "/api/repos/vault/snapshots/"+sid+"/download?path="+src+"/small.txt", nil)
	if code != 200 || string(data) != "hello herakles\n" {
		t.Fatalf("download: %d %q", code, data)
	}
	code, data = c.do("GET", "/api/repos/vault/snapshots/"+sid+"/download?path="+src+"/sub", nil)
	n := countTar(bytes.NewReader(data))
	if code != 200 || n < 4 {
		t.Fatalf("tar download: %d entries", n)
	}
	// zip and tar.gz of a selection; names relative to the common parent.
	q := "/api/repos/vault/snapshots/" + sid + "/download?path=" + url.QueryEscape(src+"/small.txt") + "&path=" + url.QueryEscape(src+"/sub/deeper")
	code, data = c.do("GET", q+"&format=zip", nil)
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if code != 200 || err != nil {
		t.Fatalf("zip: %d %v", code, err)
	}
	got := map[string]bool{}
	for _, f := range zr.File {
		got[f.Name] = true
	}
	if !got["small.txt"] || !got["sub/deeper/"] || !got["sub/deeper/text.md"] || len(got) != 3 {
		t.Fatalf("zip entries: %v", got)
	}
	code, data = c.do("GET", q+"&format=tar.gz", nil)
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if code != 200 || err != nil {
		t.Fatalf("tar.gz: %d %v", code, err)
	}
	if n := countTar(gz); n != 3 {
		t.Fatalf("tar.gz entries: %d", n)
	}
	if code, _ := c.do("GET", q+"&format=rar", nil); code == 200 {
		t.Fatal("unknown format accepted")
	}
	// Preview: text yes, binary no.
	code, data = c.do("GET", "/api/repos/vault/snapshots/"+sid+"/preview?path="+src+"/small.txt", nil)
	if code != 200 || string(data) != "hello herakles\n" {
		t.Fatalf("preview: %d %q", code, data)
	}
	if code, _ := c.do("GET", "/api/repos/vault/snapshots/"+sid+"/preview?path="+src+"/big.bin", nil); code == 200 {
		t.Fatal("binary previewed")
	}
	// Find by substring and glob.
	var found struct {
		Results []treeEntry
	}
	c.json("GET", "/api/repos/vault/snapshots/"+sid+"/find?q=DUP&path="+url.QueryEscape(src), nil, &found)
	if len(found.Results) != 2 {
		t.Fatalf("find: %+v", found)
	}
	c.json("GET", "/api/repos/vault/snapshots/"+sid+"/find?q=*.md&path="+url.QueryEscape(src), nil, &found)
	if len(found.Results) != 1 || found.Results[0].Path != src+"/sub/deeper/text.md" {
		t.Fatalf("find glob: %+v", found)
	}
	// Restore of a selection.
	sel := filepath.Join(t.TempDir(), "sel")
	var sres struct{ Result struct{ Files int64 } }
	if code := c.json("POST", "/api/repos/vault/snapshots/"+sid+"/restore", map[string]any{"paths": []string{src + "/small.txt", src + "/sub/deeper"}, "target": sel}, &sres); code != 200 || sres.Result.Files != 2 {
		t.Fatalf("selection restore: %d %+v", code, sres)
	}
	if b, err := os.ReadFile(filepath.Join(sel, "small.txt")); err != nil || string(b) != "hello herakles\n" {
		t.Fatal("restored selection:", err)
	}
	if _, err := os.Stat(filepath.Join(sel, "deeper", "text.md")); err != nil {
		t.Fatal("restored selection:", err)
	}
	target := filepath.Join(t.TempDir(), "out")
	var res struct {
		Result struct{ Files, Verified int64 }
		Errors []string
	}
	if code := c.json("POST", "/api/repos/vault/snapshots/"+sid+"/restore", map[string]any{"path": src, "target": target, "verify": true}, &res); code != 200 {
		t.Fatalf("restore: %d", code)
	}
	if res.Result.Files < 8 || res.Result.Verified < 6 || len(res.Errors) != 0 {
		t.Fatalf("restore result: %+v", res)
	}
	tu.CompareTrees(t, tu.Snapshot(t, src), tu.Snapshot(t, filepath.Join(target, filepath.Base(src))))
	var st map[string]any
	if c.json("GET", "/api/repos/vault/stats", nil, &st) != 200 {
		t.Fatal("stats")
	}
	if c.json("POST", "/api/repos/vault/task/check", nil, nil) != 202 {
		t.Fatal("check task")
	}
	waitIdle(t, c)
	if code := c.json("DELETE", "/api/repos/vault/snapshots/"+sid, nil, nil); code != 200 {
		t.Fatal("forget")
	}
}

func TestPasswordPromptFlow(t *testing.T) {
	c, _, _ := setup(t, false)
	t.Setenv("HERAKLES_PASSWORD", "")
	code, data := c.do("GET", "/api/repos/vault/snapshots", nil)
	var e apiError
	json.Unmarshal(data, &e)
	if code != 401 || !e.NeedsPassword || e.Repo != "vault" {
		t.Fatalf("expected needs_password: %d %s", code, data)
	}
	if code := c.json("POST", "/api/repos/vault/unlock", map[string]string{"password": "wrong"}, nil); code != 401 {
		t.Fatalf("wrong password unlock: %d", code)
	}
	if code := c.json("POST", "/api/repos/vault/unlock", map[string]string{"password": tu.Password.Password}, nil); code != 200 {
		t.Fatalf("unlock: %d", code)
	}
	if code, _ := c.do("GET", "/api/repos/vault/snapshots", nil); code != 200 {
		t.Fatalf("after unlock: %d", code)
	}
}

func TestConfigEditing(t *testing.T) {
	c, s, _ := setup(t, true)
	var cfg config.Config
	c.json("GET", "/api/config", nil, &cfg)
	cfg.Jobs[0].Retention.KeepDaily = 9
	cfg.Jobs[0].Schedules = []config.Schedule{{Cron: "0 3 * * *", Type: "full"}}
	if code := c.json("PUT", "/api/config", cfg, nil); code != 200 {
		t.Fatal("put config")
	}
	onDisk, err := config.Load(s.eng.Config().Path())
	if err != nil || onDisk.Jobs[0].Retention.KeepDaily != 9 {
		t.Fatalf("config not saved: %v", err)
	}
	cfg.Jobs[0].Schedules[0].Cron = "not a cron"
	code, data := c.do("PUT", "/api/config", cfg)
	if code != 400 || !strings.Contains(string(data), "invalid schedule") {
		t.Fatalf("invalid config accepted: %d %s", code, data)
	}
	var y struct{ YAML, Path string }
	c.json("GET", "/api/config/yaml", nil, &y)
	y.YAML = "# my comment\n" + y.YAML
	if code := c.json("PUT", "/api/config/yaml", map[string]string{"yaml": y.YAML}, nil); code != 200 {
		t.Fatal("put yaml")
	}
	raw, _ := os.ReadFile(y.Path)
	if !strings.HasPrefix(string(raw), "# my comment") {
		t.Fatal("YAML comments not preserved")
	}
	if code, _ := c.do("PUT", "/api/config/yaml", map[string]string{"yaml": "jobs: [{name: x}]"}); code != 400 {
		t.Fatal("invalid YAML saved")
	}
}

func TestAutostartOption(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("Windows uses the registry")
	}
	c, s, _ := setup(t, true)
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "cfg"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))
	entry := filepath.Join(dir, "cfg", "autostart", "herakles.desktop")

	cfg := *s.eng.Config()
	cfg.Tray.Autostart = true
	if code := c.json("PUT", "/api/config", &cfg, nil); code != 200 {
		t.Fatal(code)
	}
	if _, err := os.Stat(entry); err != nil {
		t.Fatal("saving the form did not install the autostart entry:", err)
	}
	// The raw YAML editor applies it too.
	var y struct{ YAML string }
	c.json("GET", "/api/config/yaml", nil, &y)
	y.YAML = strings.Replace(y.YAML, "autostart: true", "autostart: false", 1)
	if code := c.json("PUT", "/api/config/yaml", map[string]string{"yaml": y.YAML}, nil); code != 200 {
		t.Fatal(code)
	}
	if _, err := os.Stat(entry); !os.IsNotExist(err) {
		t.Fatal("saving YAML with autostart: false left the entry")
	}
}

func countTar(r io.Reader) int {
	tr := tar.NewReader(r)
	n := 0
	for {
		if _, err := tr.Next(); err != nil {
			return n
		}
		n++
	}
}

// TestUpdateAPI shows a new release in /api/info and starts the install only
// where the window may update Herakles.
func TestUpdateAPI(t *testing.T) {
	_, s, _ := setup(t, true)
	st := update.State{Current: "v0.10.0", Latest: "v0.11.0", Available: true, Method: "file", Hint: "replaces /x"}
	s.upd = func() update.State { return st }
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	c := &client{t: t, url: ts.URL, tok: s.Token()}
	var info struct {
		Update struct {
			State      update.State
			CanInstall bool `json:"can_install"`
		}
	}
	c.json("GET", "/api/info", nil, &info)
	if !info.Update.State.Available || info.Update.State.Latest != "v0.11.0" || info.Update.CanInstall {
		t.Fatalf("%+v", info.Update)
	}
	if code, body := c.do("POST", "/api/update/install", nil); code != 400 || !strings.Contains(string(body), "replaces") {
		t.Fatal("install without an installer:", code, string(body))
	}
	called := make(chan bool, 1)
	s.install = func() error { called <- true; return nil }
	c.json("GET", "/api/info", nil, &info)
	if !info.Update.CanInstall {
		t.Fatal("can_install missing")
	}
	if code, _ := c.do("POST", "/api/update/install", nil); code != 202 {
		t.Fatal("install:", code)
	}
	select {
	case <-called:
	case <-time.After(5 * time.Second):
		t.Fatal("install not called")
	}
}
