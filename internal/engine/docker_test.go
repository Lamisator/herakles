package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lamisator/herakles/internal/config"
	"github.com/lamisator/herakles/internal/docker"
	"github.com/lamisator/herakles/internal/docker/dockertest"
)

type dockerEnv struct {
	e      *Engine
	fake   *dockertest.Fake
	wd     string // project directory of "shop"
	pgdata string // postgres data dir (bind mount inside the project dir)
	vol    string // named volume of "shop"
	extra  string // bind mount outside the project dir
}

func write(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func setupDocker(t *testing.T) *dockerEnv {
	t.Helper()
	e, _ := setup(t)
	dir := t.TempDir()
	d := &dockerEnv{e: e, fake: dockertest.New(t),
		wd: filepath.Join(dir, "srv", "shop"), vol: filepath.Join(dir, "volumes", "shop_uploads", "_data"), extra: filepath.Join(dir, "etc", "shop")}
	d.pgdata = filepath.Join(d.wd, "pgdata")
	write(t, filepath.Join(d.wd, "compose.yml"), "services: {}\n")
	write(t, filepath.Join(d.wd, ".env"), "SECRET=1\n")
	write(t, filepath.Join(d.pgdata, "PG_VERSION"), "16\n")
	write(t, filepath.Join(d.vol, "a.jpg"), "image v1")
	write(t, filepath.Join(d.extra, "app.conf"), "conf v1")
	write(t, filepath.Join(dir, "srv", "blog", "compose.yaml"), "services: {}\n")
	d.fake.Set(
		dockertest.Container{Name: "shop-db-1", Image: "postgres:16", Project: "shop", Service: "db", WorkingDir: d.wd,
			ConfigFiles: []string{filepath.Join(d.wd, "compose.yml")}, Running: true, Env: []string{"POSTGRES_USER=shop"},
			Mounts: []docker.Mount{{Type: "bind", Source: d.pgdata, Destination: "/var/lib/postgresql/data"}}},
		dockertest.Container{Name: "shop-web-1", Image: "nginx", Project: "shop", Service: "web", WorkingDir: d.wd,
			ConfigFiles: []string{filepath.Join(d.wd, "compose.yml")}, Running: true,
			Mounts: []docker.Mount{
				{Type: "volume", Name: "shop_uploads", Source: d.vol, Destination: "/uploads"},
				{Type: "bind", Source: d.extra, Destination: "/etc/shop"},
			}},
		dockertest.Container{Name: "cache", Image: "redis:7", Running: true},
	)
	cfg := e.Config()
	cfg.Jobs = append(cfg.Jobs, &config.Job{Name: "containers", Repository: "main",
		Docker: &config.DockerJob{Binary: d.fake.Bin, ScanDirs: []string{filepath.Join(dir, "srv")}, ExcludeProjects: []string{"cache"}}})
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestDockerBackupAndRestore(t *testing.T) {
	ctx := context.Background()
	d := setupDocker(t)
	e := d.e

	res, err := e.RunJob(ctx, "containers", RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "success" || len(res.Projects) != 2 {
		t.Fatalf("result: %+v", res)
	}
	byName := map[string]ProjectResult{}
	for _, p := range res.Projects {
		byName[p.Project] = p
	}
	if byName["shop"].Dumps != 1 || byName["blog"].Status != "success" || byName["shop"].Snapshot == "" {
		t.Fatalf("projects: %+v", res.Projects)
	}
	if _, ok := byName["cache"]; ok {
		t.Fatal("excluded project backed up")
	}
	var added int64
	for _, p := range res.Projects {
		added += p.Summary.BytesAdded
	}
	if added != res.Summary.BytesAdded || byName["blog"].Summary.BytesAdded >= byName["shop"].Summary.BytesAdded {
		t.Fatalf("per-project figures must not accumulate: %d vs %d", added, res.Summary.BytesAdded)
	}
	snaps, err := e.ProjectSnapshots(ctx, "containers/shop")
	if err != nil || len(snaps) != 1 {
		t.Fatal(snaps, err)
	}
	info := snaps[0].Docker
	if info == nil || info.Project != "shop" || info.WorkingDir != d.wd || info.Volumes["shop_uploads"] != d.vol ||
		len(info.Dumps) != 1 || info.Dumps[0].DataDir != d.pgdata || info.Dumps[0].File != "shop-db-1.sql" {
		t.Fatalf("docker info: %+v", info)
	}
	if read(t, filepath.Join(info.MetaDir, "shop-db-1.sql")) != "-- postgres dump of shop-db-1\n" {
		t.Fatal("dump content")
	}
	if snaps[0].Job != "containers/shop" {
		t.Fatal(snaps[0].Job)
	}

	// Damage everything, then restore with dumps (the default for
	// snapshots taken while running).
	write(t, filepath.Join(d.vol, "a.jpg"), "image v2")
	write(t, filepath.Join(d.vol, "junk"), "new file")
	write(t, filepath.Join(d.extra, "app.conf"), "conf v2")
	write(t, filepath.Join(d.wd, ".env"), "SECRET=2\n")
	write(t, filepath.Join(d.pgdata, "PG_VERSION"), "broken\n")
	d.fake.ResetCalls()
	var steps []string
	rr, err := e.RestoreProject(ctx, "containers/shop", ProjectRestoreOptions{Progress: func(m string) { steps = append(steps, m) }})
	if err != nil {
		t.Fatal(err, steps)
	}
	if rr.Databases != DBDumps || !rr.Started || len(rr.Loaded) != 1 || len(rr.OldCopies) != 0 {
		t.Fatalf("restore result: %+v", rr)
	}
	if read(t, filepath.Join(d.vol, "a.jpg")) != "image v1" || read(t, filepath.Join(d.extra, "app.conf")) != "conf v1" ||
		read(t, filepath.Join(d.wd, ".env")) != "SECRET=1\n" {
		t.Fatal("files not restored")
	}
	if _, err := os.Stat(filepath.Join(d.vol, "junk")); !os.IsNotExist(err) {
		t.Fatal("files created after the snapshot must be gone")
	}
	if entries, err := os.ReadDir(d.pgdata); err != nil || len(entries) != 0 {
		t.Fatalf("dumps mode: the database directory must start empty: %v %v", entries, err)
	}
	if got := d.fake.Loaded("shop-db-1"); got != "-- postgres dump of shop-db-1\n" {
		t.Fatalf("dump not loaded: %q", got)
	}
	if !d.fake.Called("compose -p shop stop") || !d.fake.Called("compose -p shop start") {
		t.Fatal("project not stopped/started:", d.fake.Calls())
	}
	if matches, _ := filepath.Glob(filepath.Join(filepath.Dir(d.wd), "*.herakles-old-*")); len(matches) > 0 {
		t.Fatal("old copies left behind:", matches)
	}

	// Files mode restores the raw database files and keeps old copies on request.
	write(t, filepath.Join(d.pgdata, "PG_VERSION"), "broken\n")
	rr, err = e.RestoreProject(ctx, "containers/shop", ProjectRestoreOptions{Databases: DBFiles, KeepOld: true})
	if err != nil {
		t.Fatal(err)
	}
	if read(t, filepath.Join(d.pgdata, "PG_VERSION")) != "16\n" || len(rr.Loaded) != 0 || len(rr.OldCopies) == 0 {
		t.Fatalf("files mode: %+v", rr)
	}
	for _, old := range rr.OldCopies {
		if !strings.Contains(old, ".herakles-old-") {
			t.Fatal(old)
		}
		os.RemoveAll(old)
	}

	// Restoring below another directory does not touch Docker.
	d.fake.ResetCalls()
	alt := t.TempDir()
	if _, err := e.RestoreProject(ctx, "containers/shop", ProjectRestoreOptions{Target: alt}); err != nil {
		t.Fatal(err)
	}
	if read(t, filepath.Join(alt, d.vol, "a.jpg")) != "image v1" || d.fake.Called("compose") {
		t.Fatal("target restore")
	}

	// A single project can be backed up on its own.
	res, err = e.RunJob(ctx, "containers/blog", RunOptions{})
	if err != nil || len(res.Projects) != 1 || res.Projects[0].Project != "blog" {
		t.Fatalf("single project: %+v %v", res, err)
	}
	if _, err := e.RunJob(ctx, "containers/nope", RunOptions{}); err == nil {
		t.Fatal("unknown project accepted")
	}
	if _, err := e.RunJob(ctx, "docs/x", RunOptions{}); err == nil {
		t.Fatal("project of a non-docker job accepted")
	}
}

func TestDockerStopModeAndFailures(t *testing.T) {
	ctx := context.Background()
	d := setupDocker(t)
	d.e.Config().Job("containers").Docker.Stop = []string{"shop"}
	res, err := d.e.RunJob(ctx, "containers/shop", RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	p := res.Projects[0]
	if !p.Stopped || p.Dumps != 0 || !d.fake.Called("compose -p shop stop") || !d.fake.Called("start shop-db-1 shop-web-1") {
		t.Fatalf("stop mode: %+v %v", p, d.fake.Calls())
	}
	snaps, _ := d.e.ProjectSnapshots(ctx, "containers/shop")
	if !snaps[0].Docker.Stopped {
		t.Fatal("snapshot not marked stopped")
	}
	// Auto mode restores files for stopped snapshots.
	rr, err := d.e.RestoreProject(ctx, "containers/shop", ProjectRestoreOptions{DryRun: true})
	if err != nil || rr.Databases != DBFiles {
		t.Fatalf("auto mode for stopped snapshot: %+v %v", rr, err)
	}

	// A failing dump makes the project (and the job) a warning, not a failure.
	d.e.Config().Job("containers").Docker.Stop = nil
	d.fake.FailDump("shop-db-1")
	res, err = d.e.RunJob(ctx, "containers", RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "warning" || !strings.Contains(res.Error, "shop") {
		t.Fatalf("dump failure: %+v", res)
	}
	// A database container without a server (a backup sidecar) is skipped quietly.
	d.fake.Fail("shop-db-1")
	res, err = d.e.RunJob(ctx, "containers/shop", RunOptions{})
	if err != nil || res.Status != "success" {
		t.Fatalf("sidecar: %+v %v", res, err)
	}
}

func TestDockerRestoreRollback(t *testing.T) {
	ctx := context.Background()
	d := setupDocker(t)
	if _, err := d.e.RunJob(ctx, "containers/shop", RunOptions{}); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(d.wd, ".env"), "SECRET=current\n")
	// Make the restore of the volume fail: its parent becomes a file.
	parent := filepath.Dir(d.vol)
	if err := os.RemoveAll(parent); err != nil {
		t.Fatal(err)
	}
	write(t, parent, "not a directory")
	d.fake.ResetCalls()
	if _, err := d.e.RestoreProject(ctx, "containers/shop", ProjectRestoreOptions{Databases: DBFiles}); err == nil {
		t.Fatal("restore should fail")
	}
	if read(t, filepath.Join(d.wd, ".env")) != "SECRET=current\n" {
		t.Fatal("rollback did not put the previous project directory back")
	}
	if !d.fake.Called("compose -p shop start") {
		t.Fatal("project not restarted after rollback")
	}
}
