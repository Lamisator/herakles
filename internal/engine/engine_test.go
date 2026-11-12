package engine

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lamisator/herakles/internal/archiver"
	"github.com/lamisator/herakles/internal/config"
	"github.com/lamisator/herakles/internal/crypto"
	"github.com/lamisator/herakles/internal/repository"
	"github.com/lamisator/herakles/internal/restorer"
	"github.com/lamisator/herakles/internal/retention"
	tu "github.com/lamisator/herakles/internal/testutil"
)

func setup(t *testing.T) (*Engine, string) {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	tu.MakeTree(t, src)
	cfg := config.New(filepath.Join(dir, "herakles.yaml"))
	cfg.StateDir = filepath.Join(dir, "state")
	cfg.Repositories = []*config.Repository{
		{Name: "main", Location: filepath.Join(dir, "repo"), Password: "pw", Chunker: config.ChunkerKiB{Min: 4, Avg: 16, Max: 64}},
		{Name: "mirror", Location: filepath.Join(dir, "mirror"), Password: "pw2"},
	}
	cfg.Jobs = []*config.Job{{Name: "docs", Repository: "main", Paths: []string{src}, FullInterval: "7d"}}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	e := New(cfg, nil)
	for _, rc := range cfg.Repositories {
		creds, _ := rc.Credentials()
		if _, err := e.Init(context.Background(), rc, creds); err != nil {
			t.Fatal(err)
		}
	}
	return e, src
}

func TestRunJobTypes(t *testing.T) {
	ctx := context.Background()
	e, src := setup(t)
	r1, err := e.RunJob(ctx, "docs", RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if r1.Type != repository.TypeFull || r1.Status != "success" {
		t.Fatalf("first auto run should be full: %+v", r1)
	}
	r2, err := e.RunJob(ctx, "docs", RunOptions{Type: "auto"})
	if err != nil || r2.Type != repository.TypeIncremental || r2.Summary.FilesUnmodified == 0 {
		t.Fatalf("second auto run: %+v %v", r2, err)
	}
	tu.WriteFile(t, filepath.Join(src, "new"), []byte("x"))
	r3, err := e.RunJob(ctx, "docs", RunOptions{Type: "differential"})
	if err != nil || r3.Type != repository.TypeDifferential {
		t.Fatalf("differential: %+v %v", r3, err)
	}
	repo, _, err := e.OpenByName(ctx, "main")
	if err != nil {
		t.Fatal(err)
	}
	s3, _ := repo.FindSnapshot(ctx, r3.Snapshot, repository.SnapshotFilter{})
	if s3.Parent == nil || s3.Parent.String() != r1.Snapshot {
		t.Fatal("differential parent should be the last full backup")
	}
	hist, err := e.History().Load()
	if err != nil || len(hist) != 3 || hist[0].Snapshot != r3.Snapshot {
		t.Fatalf("history: %v %d", err, len(hist))
	}
	if _, err := e.RunJob(ctx, "nope", RunOptions{}); err == nil {
		t.Fatal("unknown job accepted")
	}
}

func TestResolveType(t *testing.T) {
	now := time.Date(2024, 5, 10, 12, 0, 0, 0, time.UTC)
	job := &config.Job{Name: "j", FullInterval: "7d"}
	mk := func(daysAgo int, typ string) *repository.Snapshot {
		return &repository.Snapshot{Job: "j", Hostname: "h", Type: typ, Time: now.AddDate(0, 0, -daysAgo)}
	}
	snaps := []*repository.Snapshot{mk(10, "full"), mk(3, "incremental"), mk(1, "incremental")}
	if typ, _ := ResolveType(job, "auto", snaps, "h", now); typ != "full" {
		t.Fatalf("full older than interval: %s", typ)
	}
	snaps = append(snaps, mk(2, "full"))
	typ, parent := ResolveType(job, "auto", snaps, "h", now)
	if typ != "incremental" || parent != snaps[2] {
		t.Fatalf("auto: %s %v", typ, parent)
	}
	if _, parent := ResolveType(job, "differential", snaps, "h", now); parent != snaps[3] {
		t.Fatal("differential parent")
	}
	if typ, p := ResolveType(job, "incremental", snaps, "other-host", now); typ != "full" || p != nil {
		t.Fatal("other host must not use foreign parents; without a parent the backup is full")
	}
}

func TestRetentionAfterBackupAndPrune(t *testing.T) {
	ctx := context.Background()
	e, src := setup(t)
	job := e.Config().Job("docs")
	job.Retention = retention.Policy{KeepLast: 2}
	job.Prune = true
	var last *JobResult
	for i := 0; i < 4; i++ {
		tu.WriteFile(t, filepath.Join(src, "changing"), tu.RandBytes(int64(i), 50000))
		r, err := e.RunJob(ctx, "docs", RunOptions{})
		if err != nil {
			t.Fatal(err)
		}
		last = r
	}
	if last.Forgotten != 1 || last.Pruned == nil {
		t.Fatalf("last run: forgotten=%d pruned=%v", last.Forgotten, last.Pruned)
	}
	repo, _, _ := e.OpenByName(ctx, "main")
	snaps, _ := repo.Snapshots(ctx)
	if len(snaps) != 2 {
		t.Fatalf("%d snapshots remain", len(snaps))
	}
	res, err := e.Check(ctx, repo, 1, nil)
	if err != nil || !res.OK() {
		t.Fatalf("check: %v %v", err, res)
	}
}

func TestHooksAndNotifications(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	ctx := context.Background()
	e, _ := setup(t)
	var mu sync.Mutex
	var got []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var m map[string]any
		json.Unmarshal(body, &m)
		mu.Lock()
		got = append(got, m)
		mu.Unlock()
	}))
	defer srv.Close()
	marker := filepath.Join(t.TempDir(), "hook.out")
	cfg := e.Config()
	cfg.Notifications = []*config.Notification{{Type: "webhook", URL: srv.URL, On: []string{"success", "failure"}}}
	job := cfg.Job("docs")
	job.Hooks = config.Hooks{
		Before: []string{"echo before $HERAKLES_JOB >> " + marker},
		After:  []string{"echo after $HERAKLES_STATUS $HERAKLES_TYPE >> " + marker},
	}
	if _, err := e.RunJob(ctx, "docs", RunOptions{}); err != nil {
		t.Fatal(err)
	}
	out, _ := os.ReadFile(marker)
	if !strings.Contains(string(out), "before docs") || !strings.Contains(string(out), "after success full") {
		t.Fatalf("hook output: %q", out)
	}
	// A failing before hook aborts the job and reports failure.
	job.Hooks.Before = []string{"exit 3"}
	r, err := e.RunJob(ctx, "docs", RunOptions{})
	if err == nil || r.Status != "failure" || r.Snapshot != "" {
		t.Fatalf("failing hook: %+v %v", r, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 || !strings.Contains(got[1]["text"].(string), "FAILURE") {
		t.Fatalf("notifications: %v", got)
	}
}

func TestCopyBetweenRepositories(t *testing.T) {
	ctx := context.Background()
	e, src := setup(t)
	if _, err := e.RunJob(ctx, "docs", RunOptions{}); err != nil {
		t.Fatal(err)
	}
	srcRepo, _, _ := e.OpenByName(ctx, "main")
	dstRepo, _, _ := e.OpenByName(ctx, "mirror")
	snaps, _ := srcRepo.Snapshots(ctx)
	res, err := Copy(ctx, srcRepo, dstRepo, snaps, nil)
	if err != nil || res.Copied != 1 {
		t.Fatalf("copy: %+v %v", res, err)
	}
	res, err = Copy(ctx, srcRepo, dstRepo, snaps, nil)
	if err != nil || res.Copied != 0 || res.Skipped != 1 {
		t.Fatalf("second copy should skip: %+v %v", res, err)
	}
	copied, _ := dstRepo.Snapshots(ctx)
	dst := t.TempDir()
	if _, err := restorer.Restore(ctx, dstRepo, copied[0], src, restorer.Options{Target: dst, Verify: true}); err != nil {
		t.Fatal(err)
	}
	tu.CompareTrees(t, tu.Snapshot(t, src), tu.Snapshot(t, filepath.Join(dst, filepath.Base(src))))
}

func TestPromptAndWrongPassword(t *testing.T) {
	ctx := context.Background()
	e, _ := setup(t)
	rc := e.Config().Repo("main")
	rc.Password = ""
	t.Setenv("HERAKLES_PASSWORD", "")
	calls := 0
	e.Prompt = func(string) (crypto.Credentials, error) {
		calls++
		return crypto.Credentials{Password: "pw"}, nil
	}
	if _, err := e.Open(ctx, rc); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Open(ctx, rc); err != nil || calls != 1 {
		t.Fatalf("credentials not remembered: calls=%d %v", calls, err)
	}
	rc.Password = "wrong"
	e2 := New(e.Config(), nil)
	if _, err := e2.Open(ctx, rc); err == nil {
		t.Fatal("wrong password accepted")
	}
}

// An escaped path still matches as an exclude pattern; on Windows the
// separators used to be escaped too, so the repository was backed up into
// itself.
func TestGlobEscapeExcludes(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "re[po]*")
	f, err := archiver.NewFilter([]string{globEscape(dir)})
	if err != nil {
		t.Fatal(err)
	}
	// The directory itself: the backup then does not go into it.
	if !f.Excluded(dir) {
		t.Fatalf("%s not excluded by %s", dir, globEscape(dir))
	}
	if f.Excluded(dir + "x") {
		t.Fatal("neighbour excluded")
	}
}
