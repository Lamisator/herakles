package docker_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/lamisator/herakles/internal/docker"
	"github.com/lamisator/herakles/internal/docker/dockertest"
)

func TestDetectDB(t *testing.T) {
	for image, want := range map[string]docker.DBKind{
		"postgres:16": docker.Postgres, "postgres:18-alpine": docker.Postgres, "docker.io/library/postgres@sha256:abc": docker.Postgres,
		"postgis/postgis:16-3.4": docker.Postgres, "zammad/zammad-docker-compose:zammad-postgresql-5.0.2-1": docker.Postgres,
		"mariadb:11": docker.MySQL, "mysql:8.4": docker.MySQL, "mongo:4.4": docker.Mongo,
		"registry.example.com:5000/mongo:7": docker.Mongo, "redis:7": "", "mongo-express:1": "", "nginx:1.27": "",
		"prometheuscommunity/postgres-exporter:v0.15": "", "dpage/pgadmin4": "", "zammad/zammad-docker-compose:zammad-6": "",
	} {
		got, ok := docker.DetectDB(docker.Container{Image: image})
		if got != want || ok != (want != "") {
			t.Errorf("%s: got %q %v, want %q", image, got, ok, want)
		}
	}
}

func TestDataDir(t *testing.T) {
	pg := docker.Container{Mounts: []docker.Mount{
		{Type: "bind", Source: "/srv/app/conf", Destination: "/etc/app"},
		{Type: "bind", Source: "/srv/app/pgdata", Destination: "/var/lib/postgresql/data"},
	}}
	if got := docker.DataDir(pg, docker.Postgres); got != "/srv/app/pgdata" {
		t.Fatal(got)
	}
	pg18 := docker.Container{Mounts: []docker.Mount{{Type: "volume", Name: "db", Source: "/var/lib/docker/volumes/db/_data", Destination: "/var/lib/postgresql"}}}
	if got := docker.DataDir(pg18, docker.Postgres); got != "/var/lib/docker/volumes/db/_data" {
		t.Fatal("postgres 18 layout:", got)
	}
	custom := docker.Container{Env: []string{"PGDATA=/data/pg"}, Mounts: []docker.Mount{{Type: "bind", Source: "/x", Destination: "/data"}}}
	if got := docker.DataDir(custom, docker.Postgres); got != "/x" {
		t.Fatal("PGDATA:", got)
	}
	if got := docker.DataDir(docker.Container{}, docker.MySQL); got != "" {
		t.Fatal("no mount:", got)
	}
}

func TestGroupAndData(t *testing.T) {
	dir := t.TempDir()
	mk := func(p string) string {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	wd := mk(filepath.Join(dir, "srv", "app"))
	vol := mk(filepath.Join(dir, "volumes", "app_db", "_data"))
	ext := mk(filepath.Join(dir, "etc", "app"))
	fake := dockertest.New(t)
	fake.Set(
		dockertest.Container{Name: "app-web-1", Image: "nginx", Project: "app", Service: "web", WorkingDir: wd, ConfigFiles: []string{wd + "/compose.yml"}, Running: true,
			Mounts: []docker.Mount{
				{Type: "bind", Source: wd + "/html", Destination: "/usr/share/nginx/html"},
				{Type: "bind", Source: ext, Destination: "/etc/app"},
				{Type: "bind", Source: "/var/run/docker.sock", Destination: "/var/run/docker.sock"},
				{Type: "bind", Source: "/etc/localtime", Destination: "/etc/localtime"},
				{Type: "bind", Source: filepath.Join(dir, "missing"), Destination: "/m"},
			}},
		dockertest.Container{Name: "app-db-1", Image: "postgres:16", Project: "app", Service: "db", WorkingDir: wd, Running: false,
			Mounts: []docker.Mount{{Type: "volume", Name: "app_db", Source: vol, Destination: "/var/lib/postgresql/data"}}},
		dockertest.Container{Name: "portainer", Image: "portainer/portainer-ce", Running: true},
	)
	mk(filepath.Join(dir, "srv", "Old_Stack"))
	if err := os.WriteFile(filepath.Join(dir, "srv", "Old_Stack", "docker-compose.yml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wd, "compose.yml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := &docker.Client{Binary: fake.Bin}
	ps, err := c.Projects(context.Background(), []string{filepath.Join(dir, "srv")})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, p := range ps {
		names = append(names, p.Name)
	}
	if !reflect.DeepEqual(names, []string{"app", "old_stack", "portainer"}) {
		t.Fatalf("projects: %v", names)
	}
	app := ps[0]
	if app.WorkingDir != wd || len(app.Containers) != 2 || app.Running() != 1 || app.Standalone {
		t.Fatalf("app: %+v", app)
	}
	if !ps[2].Standalone || len(ps[1].Containers) != 0 || ps[1].WorkingDir == "" {
		t.Fatalf("standalone / directory-only projects: %+v", ps)
	}
	ds := app.Data(docker.DataOptions{})
	if want := []string{ext, wd, vol}; !reflect.DeepEqual(sorted(ds.Paths), sorted(want)) {
		t.Fatalf("paths %v, want %v (bind inside the project dir, system paths and missing paths dropped)", ds.Paths, want)
	}
	if ds.Volumes["app_db"] != vol || !reflect.DeepEqual(ds.Binds, []string{ext}) {
		t.Fatalf("volumes %v binds %v", ds.Volumes, ds.Binds)
	}
	if ds := app.Data(docker.DataOptions{SkipVolumes: true, SkipBindMounts: true}); !reflect.DeepEqual(ds.Paths, []string{wd}) {
		t.Fatalf("skip options: %v", ds.Paths)
	}
}

func sorted(s []string) []string {
	c := append([]string(nil), s...)
	for i := range c {
		for j := i + 1; j < len(c); j++ {
			if c[j] < c[i] {
				c[i], c[j] = c[j], c[i]
			}
		}
	}
	return c
}

func TestDumpLoadAndControl(t *testing.T) {
	fake := dockertest.New(t)
	c := &docker.Client{Binary: fake.Bin}
	ctx := context.Background()
	var b strings.Builder
	pg := docker.Container{Name: "db", Image: "postgres:16", Env: []string{"POSTGRES_USER=app"}}
	if err := c.Dump(ctx, pg, docker.Postgres, &b); err != nil || !strings.Contains(b.String(), "postgres dump of db") {
		t.Fatal(b.String(), err)
	}
	if !fake.Called("exec db pg_dumpall -U app --clean --if-exists") {
		t.Fatal(fake.Calls())
	}
	if err := c.Load(ctx, pg, docker.Postgres, strings.NewReader("SQL")); err != nil || fake.Loaded("db") != "SQL" {
		t.Fatal(err, fake.Loaded("db"))
	}
	my := docker.Container{Name: "my", Image: "mariadb:11"}
	b.Reset()
	if err := c.Dump(ctx, my, docker.MySQL, &b); err != nil || !strings.Contains(b.String(), "mysql dump of my") {
		t.Fatal(b.String(), err)
	}
	if err := c.Ready(ctx, my, docker.MySQL); err != nil {
		t.Fatal(err)
	}
	for _, call := range fake.Calls() {
		if strings.Contains(call, "MYSQL_ROOT_PASSWORD=") || strings.Contains(call, "secret") {
			t.Fatal("password on the command line:", call)
		}
	}
	fake.Fail("my")
	if err := c.Dump(ctx, my, docker.MySQL, &b); err == nil || !strings.Contains(err.Error(), "simulated failure") {
		t.Fatal("failure not reported:", err)
	}
	if err := c.Stop(ctx, docker.Project{Name: "app"}); err != nil || !fake.Called("compose -p app stop") {
		t.Fatal(err)
	}
	if err := c.Start(ctx, docker.Project{Name: "web", Standalone: true}); err != nil || !fake.Called("start web") {
		t.Fatal(err)
	}
	if err := c.Up(ctx, docker.Project{Name: "x"}); err == nil {
		t.Fatal("up without compose files must fail")
	}
	if err := (&docker.Client{Binary: "/nonexistent/docker"}).Run(ctx, nil, nil, "ps"); err == nil {
		t.Fatal("missing binary not reported")
	}
}

func TestProjectName(t *testing.T) {
	for in, want := range map[string]string{"/srv/docker/My App": "myapp", "/x/_web.1": "web1", "/a/fundus": "fundus"} {
		if got := docker.ProjectName(in); got != want {
			t.Errorf("%s: %s, want %s", in, got, want)
		}
	}
}

func TestLabelledAndOrphanVolumes(t *testing.T) {
	dir := t.TempDir()
	fake := dockertest.New(t)
	wd := filepath.Join(dir, "srv", "kimai")
	vol := filepath.Join(dir, "volumes", "kimai_mysql", "_data")
	for _, d := range []string{wd, vol, filepath.Join(dir, "volumes", "lost", "_data")} {
		os.MkdirAll(d, 0o755)
	}
	os.WriteFile(filepath.Join(wd, "docker-compose.yml"), []byte("services: {}\n"), 0o644)
	fake.Set()
	fake.SetVolumes(docker.Volume{Name: "kimai_mysql", Mountpoint: vol, Project: "kimai"},
		docker.Volume{Name: "lost", Mountpoint: filepath.Join(dir, "volumes", "lost", "_data")})
	c := &docker.Client{Binary: fake.Bin}
	ps, vs, err := c.ProjectsAndVolumes(context.Background(), []string{filepath.Join(dir, "srv")})
	if err != nil || len(ps) != 1 {
		t.Fatal(ps, err)
	}
	ds := ps[0].Data(docker.DataOptions{})
	if ds.Volumes["kimai_mysql"] != vol || len(ds.Paths) != 2 {
		t.Fatalf("a stopped project keeps its labelled volumes: %+v", ds)
	}
	orphans := docker.Orphans(ps, vs)
	if len(orphans) != 1 || orphans[0].Name != "lost" {
		t.Fatalf("orphans: %+v", orphans)
	}
}
