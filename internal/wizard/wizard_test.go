package wizard

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"testing"

	"github.com/lamisator/herakles/internal/config"
)

func TestPlans(t *testing.T) {
	cfg := config.New(filepath.Join(t.TempDir(), "h.yaml"))
	p := &Plan{Kind: Files, Paths: []string{"/home/a", " "}, SkipCaches: true, SkipBuild: true, Extra: []string{"*.iso"},
		New:         &NewRepo{Name: DefaultRepoName(SMB, cfg), Type: SMB, Host: "nas.local", Share: "/backups/", Dir: "herakles", User: "back up", Pass: "x"},
		Maintenance: true, Schedule: "weekly", Time: "03:15", Keep: "long", Name: DefaultName(Files, cfg)}
	r, err := p.Repository()
	if err != nil {
		t.Fatal(err)
	}
	if r.Name != "nas" || r.Location != "smb://back%20up@nas.local/backups/herakles" || r.Remote.Password != "x" || r.Maintenance.PruneSchedule == "" {
		t.Fatalf("%+v", r)
	}
	j, err := p.Job(r.Name)
	if err != nil {
		t.Fatal(err)
	}
	if j.Name != "files" || len(j.Paths) != 1 || !j.ExcludeCaches || len(j.Exclude) != 8 || j.Schedules[0].Cron != "15 3 * * 0" ||
		j.Schedules[1].Type != "incremental" || j.Retention.KeepYearly != 5 || !j.CatchUp {
		t.Fatalf("%+v", j)
	}
	cfg.Repositories = []*config.Repository{r}
	r.PasswordFile = "/x"
	cfg.Jobs = []*config.Job{j}
	if err := cfg.Validate(); err != nil {
		t.Fatal("wizard output must be a valid configuration:", err)
	}
	if DefaultName(Files, cfg) != "files-2" || DefaultRepoName(SMB, cfg) != "nas-2" {
		t.Fatal("names must not clash")
	}

	for _, c := range []struct {
		n    NewRepo
		want string
	}{
		{NewRepo{Name: "r", Type: Local, Path: " /mnt/usb/herakles "}, "/mnt/usb/herakles"},
		{NewRepo{Name: "r", Type: SFTP, Host: "h", User: "u", Dir: "/srv/herakles"}, "sftp://u@h/srv/herakles"},
		{NewRepo{Name: "r", Type: Server, URL: "https://backup.example.com:8443/", ServerRepo: "laptop"}, "herakles://backup.example.com:8443/laptop"},
	} {
		p := &Plan{New: &c.n}
		if r, err := p.Repository(); err != nil || r.Location != c.want {
			t.Errorf("%+v: %v %v", c.n, r, err)
		}
	}
	d := &Plan{Kind: Docker, Docker: &config.DockerJob{Folders: []string{"/srv"}}, Schedule: "hourly", Keep: "all", Name: "c"}
	if j, err := d.Job("r"); err != nil || j.Docker == nil || j.FullInterval != "7d" || !j.Retention.Empty() {
		t.Fatal(j, err)
	}
	for _, bad := range []*Plan{{Kind: Files, Name: "x"}, {Kind: Disk, Name: "x"}, {Kind: Files, Paths: []string{"/a"}, Name: "bad name"},
		{Kind: Files, Paths: []string{"/a"}, Name: "x", Time: "25:00"}, {Kind: "", Name: "x"}} {
		if _, err := bad.Job("r"); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}

func TestSystemPlan(t *testing.T) {
	cfg := config.New(filepath.Join(t.TempDir(), "h.yaml"))
	p := &Plan{Kind: System, OneFS: true, Repo: "r", Schedule: "nightly", Keep: "standard", Name: DefaultName(System, cfg)}
	j, err := p.Job("r")
	if err != nil {
		t.Fatal(err)
	}
	if j.Name != "container" || len(j.Paths) != 1 || j.Paths[0] != "/" || !j.OneFileSystem || !j.ExcludeCaches || len(j.Exclude) != len(SystemExcludes) {
		t.Fatalf("%+v", j)
	}
}

func TestPasswords(t *testing.T) {
	pw := GeneratePassword()
	if !regexp.MustCompile(`^[A-Za-z2-9]{6}(-[A-Za-z2-9]{6}){3}$`).MatchString(pw) || pw == GeneratePassword() {
		t.Fatal(pw)
	}
	dir := t.TempDir()
	p, err := StorePassword(dir, "nas", "secret")
	if err != nil {
		t.Fatal(err)
	}
	// Windows has no file modes: the folder's ACL protects the file.
	if st, _ := os.Stat(p); runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
		t.Fatal(st.Mode())
	}
	// A retry with the same password reuses the file.
	if p2, err := StorePassword(dir, "nas", "secret"); err != nil || p2 != p {
		t.Fatal(p2, err)
	}
	// A different password never overwrites it but goes to the next free name.
	p3, err := StorePassword(dir, "nas", "other")
	if err != nil || filepath.Base(p3) != "nas-2.password" {
		t.Fatal(p3, err)
	}
	if b, _ := os.ReadFile(p); string(b) != "secret\n" {
		t.Fatal("overwrote an existing password file")
	}
	if _, err := StorePassword(dir, "../x", "s"); err == nil {
		t.Fatal("path in repository name")
	}
}
