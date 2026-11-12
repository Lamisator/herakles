package server

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lamisator/herakles/internal/archiver"
	"github.com/lamisator/herakles/internal/backend"
	"github.com/lamisator/herakles/internal/config"
	"github.com/lamisator/herakles/internal/repository"
	"github.com/lamisator/herakles/internal/restorer"
	tu "github.com/lamisator/herakles/internal/testutil"
)

func newServer(t *testing.T, cfg config.Server) *httptest.Server {
	t.Helper()
	return newServerQuota(t, cfg, 0)
}

func newServerQuota(t *testing.T, cfg config.Server, quota int64) *httptest.Server {
	t.Helper()
	cfg.DataDir = t.TempDir()
	s, err := New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.quota = quota
	ts := httptest.NewTLSServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func user(t *testing.T, name, pw string, appendOnly bool, repos ...string) *config.ServerUser {
	h, err := HashPassword(pw)
	if err != nil {
		t.Fatal(err)
	}
	return &config.ServerUser{Name: name, PasswordHash: h, AppendOnly: appendOnly, Repositories: repos}
}

func client(t *testing.T, ts *httptest.Server, repo, name, pw string) backend.Backend {
	t.Helper()
	b, err := backend.Open("rest:"+ts.URL+"/"+repo, backend.Options{Username: name, Password: pw, InsecureSkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestServerBackupRestore(t *testing.T) {
	ctx := context.Background()
	ts := newServer(t, config.Server{Users: []*config.ServerUser{user(t, "alice", "secret", false)}})
	be := client(t, ts, "alice-laptop", "alice", "secret")
	if _, err := repository.Init(ctx, be, tu.Password, tu.SmallChunks); err != nil {
		t.Fatal(err)
	}
	repo := tu.Reopen(t, be)
	src := t.TempDir()
	tu.MakeTree(t, src)
	a, _ := archiver.New(repo, archiver.Options{Paths: []string{src}})
	snap, err := a.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	repo = tu.Reopen(t, client(t, ts, "alice-laptop", "alice", "secret"))
	dst := t.TempDir()
	if _, err := restorer.Restore(ctx, repo, snap, src, restorer.Options{Target: dst, Verify: true}); err != nil {
		t.Fatal(err)
	}
	tu.CompareTrees(t, tu.Snapshot(t, src), tu.Snapshot(t, filepath.Join(dst, filepath.Base(src))))
	// Full maintenance works through the server.
	repo.RemoveSnapshot(ctx, snap.ID())
	if _, err := repo.Prune(ctx, repository.PruneOptions{}); err != nil {
		t.Fatal(err)
	}
	res, err := repo.Check(ctx, repository.CheckOptions{ReadData: 1})
	if err != nil || !res.OK() {
		t.Fatalf("check: %v %+v", err, res)
	}
}

func TestServerAuthAndACL(t *testing.T) {
	ctx := context.Background()
	ts := newServer(t, config.Server{Users: []*config.ServerUser{
		user(t, "alice", "secret", false),
		user(t, "bob", "pw", true, "shared"),
	}})
	if _, err := repository.Init(ctx, client(t, ts, "alice", "alice", "wrong"), tu.Password, tu.SmallChunks); err == nil || !strings.Contains(err.Error(), "authentication") {
		t.Fatalf("wrong password: %v", err)
	}
	if _, err := repository.Init(ctx, client(t, ts, "bob-repo", "bob", "pw"), tu.Password, tu.SmallChunks); err == nil || !strings.Contains(err.Error(), "denied") {
		t.Fatalf("ACL: %v", err)
	}
	if _, err := repository.Init(ctx, client(t, ts, "..", "alice", "secret"), tu.Password, tu.SmallChunks); err == nil {
		t.Fatal("path traversal accepted")
	}
	// Append-only: backups work, deleting data does not, locks still work.
	be := client(t, ts, "shared", "bob", "pw")
	if _, err := repository.Init(ctx, be, tu.Password, tu.SmallChunks); err != nil {
		t.Fatal(err)
	}
	repo := tu.Reopen(t, be)
	src := t.TempDir()
	tu.WriteFile(t, filepath.Join(src, "f"), []byte("data"))
	a, _ := archiver.New(repo, archiver.Options{Paths: []string{src}})
	l, err := repo.Lock(ctx, false, "backup")
	if err != nil {
		t.Fatal(err)
	}
	snap, err := a.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Unlock(); err != nil {
		t.Fatal("lock removal must be allowed in append-only mode:", err)
	}
	if err := repo.RemoveSnapshot(ctx, snap.ID()); err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("append-only delete: %v", err)
	}
	// Overwriting existing objects is refused.
	if err := be.Save(ctx, backend.Handle{Type: backend.ConfigFile}, []byte("{}")); err == nil {
		t.Fatal("config overwrite accepted")
	}
	// Health endpoint needs no auth.
	resp, err := ts.Client().Get(ts.URL + "/_health")
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("health: %v", err)
	}
}

func TestServerQuota(t *testing.T) {
	ctx := context.Background()
	ts := newServerQuota(t, config.Server{NoAuth: true}, 64<<10)
	be := client(t, ts, "q", "", "")
	if _, err := repository.Init(ctx, be, tu.Password, tu.SmallChunks); err != nil {
		t.Fatal(err)
	}
	if err := be.Save(ctx, backend.Handle{Type: backend.PackFile, Name: strings.Repeat("ab", 32)}, make([]byte, 30<<10)); err != nil {
		t.Fatal("upload within quota rejected:", err)
	}
	err := be.Save(ctx, backend.Handle{Type: backend.PackFile, Name: strings.Repeat("cd", 32)}, make([]byte, 40<<10))
	if err == nil || !strings.Contains(err.Error(), "quota") {
		t.Fatalf("quota not enforced: %v", err)
	}
	// Backup files are streamed; the quota stops them too, and nothing
	// of the rejected file stays.
	err = be.SaveStream(ctx, backend.Handle{Type: backend.BundleFile, Name: "x_full_1.hbk"}, bytes.NewReader(make([]byte, 100<<10)))
	if err == nil || !strings.Contains(err.Error(), "quota") {
		t.Fatalf("quota not enforced for streams: %v", err)
	}
	if err := be.SaveStream(ctx, backend.Handle{Type: backend.BundleFile, Name: "x_full_2.hbk"}, bytes.NewReader(make([]byte, 10<<10))); err != nil {
		t.Fatal("stream within quota:", err)
	}
	var names []string
	be.List(ctx, backend.BundleFile, func(fi backend.FileInfo) error { names = append(names, fi.Name); return nil })
	if len(names) != 1 || names[0] != "x_full_2.hbk" {
		t.Fatalf("backup files: %v", names)
	}
	// The client sees the quota as the repository's capacity.
	sp, err := backend.SpaceOf(ctx, be)
	if err != nil || !sp.Quota || sp.Total != 64<<10 || sp.Free == 0 || sp.Free >= 64<<10-40<<10 {
		t.Fatalf("space with quota: %+v %v", sp, err)
	}
	plain := client(t, newServer(t, config.Server{NoAuth: true}), "p", "", "")
	if sp, err := backend.SpaceOf(ctx, plain); err != nil || sp.Quota || sp.Total == 0 || sp.Free > sp.Total {
		t.Fatalf("space without quota: %+v %v", sp, err)
	}
}

func TestNewValidation(t *testing.T) {
	if _, err := New(config.Server{}, nil); err == nil {
		t.Fatal("missing data dir accepted")
	}
	if _, err := New(config.Server{DataDir: t.TempDir()}, nil); err == nil {
		t.Fatal("server without users accepted")
	}
	dir := t.TempDir()
	if err := GenerateSelfSigned(filepath.Join(dir, "c.pem"), filepath.Join(dir, "k.pem"), []string{"localhost", "127.0.0.1"}); err != nil {
		t.Fatal(err)
	}
}
