package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lamisator/herakles/internal/config"
	"github.com/lamisator/herakles/internal/crypto"
	tu "github.com/lamisator/herakles/internal/testutil"
)

func init() { crypto.DefaultKDFParams = crypto.KDFParams{Time: 1, Memory: 1024, Threads: 1} }

type env struct {
	t   *testing.T
	dir string
	cfg string
	src string
}

func newEnv(t *testing.T) *env {
	dir := t.TempDir()
	e := &env{t: t, dir: dir, cfg: filepath.Join(dir, "h.yaml"), src: filepath.Join(dir, "src")}
	tu.MakeTree(t, e.src)
	os.WriteFile(filepath.Join(dir, "pw"), []byte("secret\n"), 0o600)
	t.Setenv("HERAKLES_CONFIG", "")
	t.Setenv("HERAKLES_PASSWORD", "")
	t.Setenv("HERAKLES_REPOSITORY", "")
	return e
}

// run executes the CLI and returns stdout, stderr and the exit code.
func (e *env) run(args ...string) (string, string, int) {
	e.t.Helper()
	var out, errb bytes.Buffer
	app := &App{out: &out, err: &errb}
	root := app.rootCommand()
	root.SetArgs(append([]string{"--config", e.cfg}, args...))
	code := 0
	if err := root.ExecuteContext(context.Background()); err != nil {
		code = 1
		if ee, ok := err.(exitError); ok {
			code = ee.code
		}
		errb.WriteString("error: " + err.Error())
	}
	return out.String(), errb.String(), code
}

func (e *env) ok(args ...string) string {
	e.t.Helper()
	out, errs, code := e.run(args...)
	if code != 0 {
		e.t.Fatalf("herakles %v failed (%d):\n%s\n%s", args, code, out, errs)
	}
	return out
}

func (e *env) setup() {
	e.ok("config", "set", "repo", "main", filepath.Join(e.dir, "repo"), "--password-file-path", filepath.Join(e.dir, "pw"),
		"--yaml", "chunker: {min_kib: 4, avg_kib: 16, max_kib: 64}")
	e.ok("config", "set", "job", "docs", "--repository", "main", "--path", e.src, "--exclude", "*.tmp",
		"--schedule", "0 2 * * 0=full", "--schedule", "0 2 * * 1-6=incremental", "--full-interval", "7d",
		"--yaml", "retention: {keep_last: 2}")
	e.ok("init")
}

func TestCLIWorkflow(t *testing.T) {
	e := newEnv(t)
	e.setup()
	if out := e.ok("config", "validate"); !strings.Contains(out, "1 repositories, 1 jobs") {
		t.Fatal(out)
	}
	out := e.ok("backup", "docs")
	if !strings.Contains(out, "full backup") {
		t.Fatalf("first backup should be full: %s", out)
	}
	tu.WriteFile(t, filepath.Join(e.src, "new.txt"), []byte("new"))
	out = e.ok("backup", "docs")
	if !strings.Contains(out, "incremental backup") || !strings.Contains(out, "1 new") {
		t.Fatalf("second backup: %s", out)
	}
	var snaps []SnapshotView
	if err := json.Unmarshal([]byte(e.ok("snapshots", "--json")), &snaps); err != nil || len(snaps) != 2 {
		t.Fatalf("snapshots json: %v %d", err, len(snaps))
	}
	if !strings.Contains(e.ok("diff", snaps[0].ID[:8], snaps[1].ID[:8]), "+  "+filepath.ToSlash(e.src)+"/new.txt") {
		t.Fatal("diff misses added file")
	}
	if !strings.Contains(e.ok("ls", "latest", e.src), "new.txt") {
		t.Fatal("ls")
	}
	if strings.Contains(e.ok("ls", "latest", e.src, "-R"), "junk.tmp") {
		t.Fatal("excluded file in snapshot")
	}
	if got := e.ok("dump", "latest", filepath.Join(e.src, "small.txt")); got != "hello herakles\n" {
		t.Fatalf("dump: %q", got)
	}
	if !strings.Contains(e.ok("find", "*.md"), "text.md") {
		t.Fatal("find")
	}
	target := filepath.Join(e.dir, "restore")
	e.ok("restore", "latest:"+e.src, "--target", target, "--verify")
	want := tu.Snapshot(t, e.src)
	delete(want, "cache/junk.tmp") // excluded by the job
	tu.CompareTrees(t, want, tu.Snapshot(t, filepath.Join(target, filepath.Base(e.src))))
	// third backup triggers retention keep_last 2
	tu.WriteFile(t, filepath.Join(e.src, "third.txt"), []byte("3"))
	if out := e.ok("backup", "docs"); !strings.Contains(out, "forgot 1 snapshots") {
		t.Fatalf("retention not applied: %s", out)
	}
	e.ok("forget", "--keep-last", "1", "--prune")
	out = e.ok("check", "--read-data", "100%")
	if !strings.Contains(out, "no errors found") {
		t.Fatal(out)
	}
	if !strings.Contains(e.ok("stats"), "snapshots:        1") {
		t.Fatal("stats")
	}
	jobs := e.ok("jobs")
	if !strings.Contains(jobs, "docs") || !strings.Contains(jobs, "success") {
		t.Fatalf("jobs: %s", jobs)
	}
}

func TestCLIErrors(t *testing.T) {
	e := newEnv(t)
	if _, errs, code := e.run("snapshots", "--no-prompt"); code == 0 || !strings.Contains(errs, "repository") {
		t.Fatalf("snapshots without config: %d %s", code, errs)
	}
	e.setup()
	if _, errs, code := e.run("backup", "nope"); code == 0 || !strings.Contains(errs, "unknown job") {
		t.Fatalf("unknown job: %s", errs)
	}
	if _, _, code := e.run("backup", "docs", "--type", "weird"); code == 0 {
		t.Fatal("invalid type accepted")
	}
	if _, errs, code := e.run("forget"); code == 0 || !strings.Contains(errs, "refusing") {
		t.Fatalf("forget without policy: %s", errs)
	}
	os.WriteFile(filepath.Join(e.dir, "pw"), []byte("wrong\n"), 0o600)
	if _, errs, code := e.run("snapshots"); code == 0 || !strings.Contains(errs, "wrong password") {
		t.Fatalf("wrong password: %s", errs)
	}
	os.WriteFile(e.cfg, []byte("jobs: [{name: x}]\n"), 0o600)
	if _, errs, code := e.run("jobs"); code == 0 || !strings.Contains(errs, "unknown repository") {
		t.Fatalf("invalid config: %s", errs)
	}
}

func TestCLIAdhocBackupWithKeyFile(t *testing.T) {
	e := newEnv(t)
	kf := filepath.Join(e.dir, "key")
	repo := filepath.Join(e.dir, "adhoc")
	e.ok("init", "--repo", repo, "--generate-key-file", kf, "--chunk-avg-kib", "16")
	// Key file alone unlocks the repository.
	out := e.ok("backup", "--repo", repo, "--key-file", kf, "--path", e.src, "--tag", "manual")
	if !strings.Contains(out, "full backup") {
		t.Fatal(out)
	}
	out = e.ok("backup", "--repo", repo, "--key-file", kf, "--path", e.src)
	if !strings.Contains(out, "incremental backup") {
		t.Fatalf("second ad-hoc backup should use the first as parent: %s", out)
	}
	out = e.ok("snapshots", "--repo", repo, "--key-file", kf, "--tag", "manual")
	if !strings.Contains(out, "1 snapshots") {
		t.Fatal(out)
	}
	if _, _, code := e.run("snapshots", "--repo", repo, "--no-prompt"); code == 0 {
		t.Fatal("opened without key file")
	}
	e.ok("key", "list", "--repo", repo, "--key-file", kf)
}

func TestCLIDevice(t *testing.T) {
	e := newEnv(t)
	e.setup()
	img := filepath.Join(e.dir, "disk.img")
	data := append(tu.RandBytes(5, 300<<10), make([]byte, 200<<10)...)
	os.WriteFile(img, data, 0o600)
	e.ok("backup", "--repo", "main", "--device", img)
	out := filepath.Join(e.dir, "restored.img")
	e.ok("restore-device", "latest:"+img, "--to", out)
	got, _ := os.ReadFile(out)
	if !bytes.Equal(got, data) {
		t.Fatal("device restore differs")
	}
}

func TestCLIConfigCommands(t *testing.T) {
	e := newEnv(t)
	e.ok("config", "init")
	if _, _, code := e.run("config", "init"); code == 0 {
		t.Fatal("config init overwrote existing file")
	}
	c, err := config.Load(e.cfg)
	if err != nil || len(c.Jobs) != 2 {
		t.Fatalf("example config: %v", err)
	}
	e.ok("config", "set", "remove", "job", "system-disk")
	show := e.ok("config", "show")
	if strings.Contains(show, "system-disk") || !strings.Contains(show, "home") {
		t.Fatal("remove job")
	}
	if !strings.Contains(e.ok("config", "path"), "h.yaml") {
		t.Fatal("path")
	}
	if !strings.Contains(e.ok("config", "example"), "repositories:") {
		t.Fatal("example")
	}
	if !strings.Contains(e.ok("version"), "herakles") {
		t.Fatal("version")
	}
	h := e.ok("server", "hash-password", "x")
	if !strings.HasPrefix(h, "$2a$") {
		t.Fatal("hash")
	}
}

func TestParseSnapSpec(t *testing.T) {
	cases := map[string][2]string{
		"latest":                {"latest", ""},
		"latest:/etc":           {"latest", "/etc"},
		"abcd1234:C:\\Users":    {"abcd1234", "C:\\Users"},
		"C:\\Users":             {"C:\\Users", ""},
		"/dev/sda1":             {"/dev/sda1", ""},
		"latest:/dev/nvme0n1p1": {"latest", "/dev/nvme0n1p1"},
	}
	for in, want := range cases {
		a, b := parseSnapSpec(in)
		if a != want[0] || b != want[1] {
			t.Errorf("%q -> %q %q", in, a, b)
		}
	}
}
