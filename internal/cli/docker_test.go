package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lamisator/herakles/internal/docker"
	"github.com/lamisator/herakles/internal/docker/dockertest"
)

func TestCLIDocker(t *testing.T) {
	e := newEnv(t)
	e.setup()
	fake := dockertest.New(t)
	wd := filepath.Join(e.dir, "srv", "wiki")
	vol := filepath.Join(e.dir, "volumes", "wiki_db", "_data")
	for p, c := range map[string]string{filepath.Join(wd, "compose.yml"): "services: {}\n", filepath.Join(vol, "base", "1"): "row v1"} {
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(c), 0o644)
	}
	fake.Set(dockertest.Container{Name: "wiki-db-1", Image: "mariadb:11", Project: "wiki", Service: "db", WorkingDir: wd,
		ConfigFiles: []string{filepath.Join(wd, "compose.yml")}, Running: true,
		Mounts: []docker.Mount{{Type: "volume", Name: "wiki_db", Source: vol, Destination: "/var/lib/mysql"}}})
	e.ok("config", "set", "job", "containers", "--repository", "main",
		"--yaml", "docker: {binary: "+fake.Bin+", scan_dirs: ["+filepath.Join(e.dir, "srv")+"]}")

	if out := e.ok("docker", "ls"); !strings.Contains(out, "wiki") || !strings.Contains(out, "1/1 running") || !strings.Contains(out, "mysql") {
		t.Fatal(out)
	}
	if out := e.ok("docker", "show", "wiki"); !strings.Contains(out, "mysql database, data in "+vol) || !strings.Contains(out, "volume:     wiki_db") {
		t.Fatal(out)
	}
	if out := e.ok("docker", "backup", "containers/wiki"); !strings.Contains(out, "wiki") || !strings.Contains(out, "1 database dumps") {
		t.Fatal(out)
	}
	if out := e.ok("docker", "ls", "--last"); strings.Contains(out, "never") {
		t.Fatal("last backup not shown:", out)
	}
	if out := e.ok("snapshots", "--job", "containers/wiki"); !strings.Contains(out, "1 snapshots") {
		t.Fatal(out)
	}

	os.WriteFile(filepath.Join(vol, "base", "1"), []byte("row v2"), 0o644)
	if out := e.ok("docker", "restore", "wiki", "--dry-run"); !strings.Contains(out, "databases: dumps") || !strings.Contains(out, vol) {
		t.Fatal(out)
	}
	if _, errs, code := e.run("docker", "restore", "wiki"); code == 0 || !strings.Contains(errs, "--yes") {
		t.Fatal("restore without confirmation:", errs)
	}
	out := e.ok("docker", "restore", "wiki", "--yes")
	if !strings.Contains(out, "loaded dumps into wiki-db-1") || !strings.Contains(fake.Loaded("wiki-db-1"), "mysql dump of wiki-db-1") {
		t.Fatal(out)
	}
	if entries, _ := os.ReadDir(vol); len(entries) != 0 {
		t.Fatal("database volume should be fresh for the dump", entries)
	}
	alt := filepath.Join(e.dir, "alt")
	e.ok("docker", "restore", "wiki", "--target", alt, "--databases", "files")
	if b, _ := os.ReadFile(filepath.Join(alt, vol, "base", "1")); string(b) != "row v1" {
		t.Fatal("target restore:", string(b))
	}
	// A job selecting projects by folder (new projects included automatically).
	e.ok("config", "set", "job", "byfolder", "--repository", "main", "--docker-folder", filepath.Join(e.dir, "srv"),
		"--docker-stop", "wiki", "--yaml", "docker: {binary: "+fake.Bin+"}")
	if out := e.ok("docker", "ls", "byfolder"); !strings.Contains(out, "wiki") || !strings.Contains(out, "stop") {
		t.Fatal(out)
	}
	os.MkdirAll(filepath.Join(e.dir, "srv", "later"), 0o755)
	os.WriteFile(filepath.Join(e.dir, "srv", "later", "compose.yaml"), []byte("services: {}\n"), 0o644)
	if out := e.ok("docker", "ls", "byfolder"); !strings.Contains(out, "later") {
		t.Fatal("new project in the folder not listed:", out)
	}
	if _, errs, code := e.run("config", "set", "job", "bad", "--repository", "main", "--docker-folder", "relative"); code == 0 || !strings.Contains(errs, "absolute") {
		t.Fatal("relative folder accepted:", errs)
	}
	if _, errs, code := e.run("docker", "restore", "containers/nope", "--yes"); code == 0 || !strings.Contains(errs, "nope") {
		t.Fatal(errs)
	}
}
