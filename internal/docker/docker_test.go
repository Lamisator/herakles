package docker_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
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

// Docker backups read the host side of Linux bind mounts; Docker Desktop
// on Windows keeps them inside its VM, so these only apply on Linux.
func linuxOnly(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Docker backups need a Linux Docker host")
	}
}

func TestDataDir(t *testing.T) {
	linuxOnly(t)
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
	ps, err := c.Projects(context.Background(), docker.Search{Dirs: []string{filepath.Join(dir, "srv")}})
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
	ps, vs, err := c.ProjectsAndVolumes(context.Background(), docker.Search{Dirs: []string{filepath.Join(dir, "srv")}})
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

func TestScanTreeAndDirs(t *testing.T) {
	dir := t.TempDir()
	for _, p := range []string{"docker/app/compose.yml", "docker/app/nested/compose.yml", "apps/x/y/docker-compose.yaml", ".hidden/z/compose.yml", "a/b/c/d/compose.yml"} {
		os.MkdirAll(filepath.Join(dir, filepath.Dir(p)), 0o755)
		os.WriteFile(filepath.Join(dir, p), []byte("services: {}\n"), 0o644)
	}
	var names []string
	for _, p := range docker.ScanTree([]string{dir}, 3) {
		names = append(names, p.Name)
	}
	// Not below a project, not into hidden dirs, at most 3 levels.
	if strings.Join(names, ",") != "y,app" && strings.Join(names, ",") != "app,y" {
		t.Fatalf("found %v", names)
	}
	sa := docker.Project{Standalone: true, Containers: []docker.Container{{Mounts: []docker.Mount{{Type: "bind", Source: "/srv/x"}, {Type: "volume", Source: "/var/lib/docker/v"}}}}}
	if d := sa.Dirs(); len(d) != 1 || d[0] != "/srv/x" {
		t.Fatal(d)
	}
}

func TestScanSkipsDirsInsideProjects(t *testing.T) {
	dir := t.TempDir()
	wd := filepath.Join(dir, "srv", "pyxis")
	os.MkdirAll(filepath.Join(wd, "repo"), 0o755)
	os.WriteFile(filepath.Join(wd, "repo", "docker-compose.yml"), []byte("services: {}\n"), 0o644) // compose file of a source checkout
	os.MkdirAll(filepath.Join(dir, "srv", "archive", "old"), 0o755)
	os.WriteFile(filepath.Join(dir, "srv", "archive", "old", "compose.yml"), []byte("services: {}\n"), 0o644)
	fake := dockertest.New(t)
	// A checkout that contains the compose folder of a running project.
	os.MkdirAll(filepath.Join(dir, "srv", "app", "repo", "deploy"), 0o755)
	os.WriteFile(filepath.Join(dir, "srv", "app", "repo", "compose.yml"), []byte("services: {}\n"), 0o644)
	fake.Set(dockertest.Container{Name: "pyxis-app", Image: "x", Project: "pyxis", WorkingDir: wd, Running: true},
		dockertest.Container{Name: "app-1", Image: "x", Project: "app", WorkingDir: filepath.Join(dir, "srv", "app", "repo", "deploy"), Running: true})
	ps, err := (&docker.Client{Binary: fake.Bin}).Projects(context.Background(), docker.Search{Trees: []string{filepath.Join(dir, "srv")}})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, p := range ps {
		names = append(names, p.Name)
	}
	if strings.Join(names, ",") != "app,old,pyxis" {
		t.Fatal("a directory inside a project became a project:", names)
	}
}

func TestProjectHomeGrowsToBindMounts(t *testing.T) {
	linuxOnly(t)
	dir := t.TempDir()
	bw := filepath.Join(dir, "srv", "docker", "bitwarden", "bwdata")
	for _, d := range []string{"docker", "core", "mssql/data", "env", "logs/api"} {
		os.MkdirAll(filepath.Join(bw, d), 0o755)
	}
	os.WriteFile(filepath.Join(bw, "config.yml"), []byte("x"), 0o600)
	other := filepath.Join(dir, "srv", "docker", "wiki")
	os.MkdirAll(other, 0o755)
	mk := func(src string) docker.Mount { return docker.Mount{Type: "bind", Source: src, Destination: "/x"} }
	bitwarden := docker.Project{Name: "docker", WorkingDir: filepath.Join(bw, "docker"), Containers: []docker.Container{
		{Name: "mssql", Mounts: []docker.Mount{mk(filepath.Join(bw, "mssql", "data")), mk(filepath.Join(bw, "core"))}},
		{Name: "api", Mounts: []docker.Mount{mk(filepath.Join(bw, "logs", "api"))}},
	}}
	wiki := docker.Project{Name: "wiki", WorkingDir: other, Containers: []docker.Container{{Name: "w", Mounts: []docker.Mount{mk(filepath.Join(bw, "core"))}}}}
	sets := docker.DataSets([]docker.Project{bitwarden, wiki}, docker.DataOptions{Roots: []string{filepath.Join(dir, "srv")}})
	if ds := sets["docker"]; ds.Home != bw || len(ds.Paths) != 1 || ds.Paths[0] != bw {
		t.Fatalf("bitwarden: home %s, paths %v (config.yml and env/ must be included)", ds.Home, ds.Paths)
	}
	// wiki's bind into bwdata would make its home /srv/docker, which holds
	// other projects: it stays in its own directory.
	if ds := sets["wiki"]; ds.Home != other {
		t.Fatalf("wiki home %s", ds.Home)
	}
	// Never grow to a configured folder.
	solo := docker.Project{Name: "app", WorkingDir: filepath.Join(dir, "srv", "app"), Containers: []docker.Container{{Name: "a", Mounts: []docker.Mount{mk(filepath.Join(dir, "srv", "app-data"))}}}}
	os.MkdirAll(filepath.Join(dir, "srv", "app"), 0o755)
	os.MkdirAll(filepath.Join(dir, "srv", "app-data"), 0o755)
	if ds := docker.DataSets([]docker.Project{solo}, docker.DataOptions{Roots: []string{filepath.Join(dir, "srv")}})["app"]; ds.Home != filepath.Join(dir, "srv", "app") || len(ds.Paths) != 2 {
		t.Fatalf("solo: %+v", ds)
	}
}
