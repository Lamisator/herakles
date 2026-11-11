package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestExampleParses(t *testing.T) {
	c, err := Parse([]byte(Example))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Repositories) != 2 || len(c.Jobs) != 3 {
		t.Fatalf("parsed %d repos %d jobs", len(c.Repositories), len(c.Jobs))
	}
	if d := c.Job("containers").Docker; d == nil || !d.Selects("wiki") || d.Selects("portainer") || len(d.ScanDirs) != 1 {
		t.Fatalf("docker job: %+v", d)
	}
	j := c.Job("home")
	if j.Retention.KeepMonthly != 12 || j.Retention.KeepWithin.Days != 2 || len(j.Schedules) != 3 {
		t.Fatalf("job: %+v", j)
	}
	// Round trip through Marshal keeps everything.
	out, err := c.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	c2, err := Parse(out)
	if err != nil {
		t.Fatalf("re-parse: %v\n%s", err, out)
	}
	if !reflect.DeepEqual(c2.Job("home").Retention, j.Retention) || c2.Repo("local").Maintenance != c.Repo("local").Maintenance {
		t.Fatal("round trip changed values")
	}
}

func TestValidation(t *testing.T) {
	bad := map[string]string{
		"unknown field":   "repositories: []\nbogus: 1\n",
		"dup repo":        "repositories:\n- {name: a, location: /x}\n- {name: a, location: /y}\n",
		"missing repo":    "repositories: []\njobs:\n- {name: j, repository: nope, paths: [/x]}\n",
		"no paths":        "repositories:\n- {name: a, location: /x}\njobs:\n- {name: j, repository: a}\n",
		"bad cron":        "repositories:\n- {name: a, location: /x}\njobs:\n- {name: j, repository: a, paths: [/x], schedule: [{cron: 'nope'}]}\n",
		"bad type":        "repositories:\n- {name: a, location: /x}\njobs:\n- {name: j, repository: a, paths: [/x], schedule: [{cron: '@daily', type: weird}]}\n",
		"bad compression": "repositories:\n- {name: a, location: /x, compression: lzma}\n",
		"bad retention":   "repositories:\n- {name: a, location: /x}\njobs:\n- {name: j, repository: a, paths: [/x], retention: {keep_within: 5x}}\n",
		"bad size":        "repositories:\n- {name: a, location: /x}\njobs:\n- {name: j, repository: a, paths: [/x], exclude_larger_than: lots}\n",
		"bad notify":      "repositories: []\nnotifications:\n- {type: pigeon}\n",
		"tls half":        "repositories: []\nserver: {tls_cert: /a}\n",
	}
	for name, y := range bad {
		if _, err := Parse([]byte(y)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if c, err := Parse(nil); err != nil || len(c.Jobs) != 0 {
		t.Fatalf("empty config: %v", err)
	}
}

func TestSaveLoad(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "h.yaml")
	c := New(p)
	c.Repositories = append(c.Repositories, &Repository{Name: "r", Location: "/tmp/r", Password: "x"})
	c.Jobs = append(c.Jobs, &Job{Name: "j", Repository: "r", Paths: []string{"/etc"}})
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(p)
	if fi.Mode().Perm() != 0o600 && os.PathSeparator == '/' {
		t.Fatalf("config mode %v", fi.Mode())
	}
	c2, err := Load(p)
	if err != nil || c2.Job("j") == nil || c2.Path() != p {
		t.Fatal(err)
	}
	c2.Jobs[0].Repository = "missing"
	if err := c2.Save(); err == nil {
		t.Fatal("invalid config saved")
	}
}

func TestCredentials(t *testing.T) {
	dir := t.TempDir()
	pf := filepath.Join(dir, "pw")
	os.WriteFile(pf, []byte("from-file\n"), 0o600)
	r := &Repository{PasswordFile: pf}
	c, err := r.Credentials()
	if err != nil || c.Password != "from-file" {
		t.Fatalf("%v %q", err, c.Password)
	}
	t.Setenv("HK_TEST_PW", "from-env")
	c, _ = (&Repository{PasswordEnv: "HK_TEST_PW"}).Credentials()
	if c.Password != "from-env" {
		t.Fatal("env")
	}
	if os.PathSeparator == '/' {
		c, _ = (&Repository{PasswordCommand: "echo from-cmd"}).Credentials()
		if c.Password != "from-cmd" {
			t.Fatal("command")
		}
	}
	t.Setenv("HERAKLES_PASSWORD", "")
	if _, err := (&Repository{}).Credentials(); err != ErrNoPassword {
		t.Fatalf("no source: %v", err)
	}
	kf := filepath.Join(dir, "key")
	os.WriteFile(kf, []byte(strings.Repeat("k", 64)), 0o600)
	c, err = (&Repository{KeyFile: kf}).Credentials()
	if err != nil || !c.HasKeyFile() || c.HasPassword() {
		t.Fatal("key file only")
	}
}

func TestParseHelpers(t *testing.T) {
	for in, want := range map[string]int64{"": 0, "100": 100, "1k": 1024, "2M": 2 << 20, "1.5G": 3 << 29, "1TiB": 1 << 40, "10MB": 10 << 20} {
		got, err := ParseSize(in)
		if err != nil || got != want {
			t.Errorf("ParseSize(%q) = %d, %v", in, got, err)
		}
	}
	if f, _ := ParsePercent("25%"); f != 0.25 {
		t.Error("percent")
	}
	if _, err := ParsePercent("150%"); err == nil {
		t.Error("150% accepted")
	}
	if _, err := ParseCron("@every 6h"); err != nil {
		t.Error(err)
	}
}
