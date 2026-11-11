package tray

import (
	"bytes"
	"encoding/binary"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lamisator/herakles/internal/archiver"
	"github.com/lamisator/herakles/internal/config"
	"github.com/lamisator/herakles/internal/daemon"
	"github.com/lamisator/herakles/internal/engine"
)

func TestSummarize(t *testing.T) {
	now := time.Now()
	sched := []daemon.Entry{
		{Kind: daemon.KindPrune, Target: "nas", Next: now.Add(time.Minute)},
		{Kind: daemon.KindBackup, Target: "home", Next: now.Add(3 * time.Hour)},
		{Kind: daemon.KindBackup, Target: "docs", Next: now.Add(90 * time.Minute)},
	}
	running := daemon.RunView{Run: &daemon.Run{Kind: daemon.KindBackup, Target: "home", State: "running"},
		ProgressStats: &archiver.ProgressStats{ScanDone: true, BytesTotal: 200, BytesDone: 84}}
	queued := daemon.RunView{Run: &daemon.Run{Kind: daemon.KindPrune, Target: "nas", State: "queued"}}
	failed := map[string]*engine.JobResult{"home": {Status: "failure"}, "docs": {Status: "success"}}
	cases := []struct {
		name   string
		st     daemon.Status
		locked []string
		want   Status
	}{
		{"nothing", daemon.Status{}, nil, Status{Idle, "No backups scheduled"}},
		{"next", daemon.Status{Schedule: sched}, nil, Status{Idle, "Next backup: docs in 1.5 h"}},
		{"running", daemon.Status{Schedule: sched, Active: []daemon.RunView{running, queued}}, []string{"x"}, Status{Busy, "Backing up home 42 % (+1 more)"}},
		{"queued", daemon.Status{Active: []daemon.RunView{queued}}, nil, Status{Busy, "Pruning nas (queued)"}},
		{"locked", daemon.Status{Schedule: sched, Last: failed}, []string{"nas", "usb"}, Status{Locked, "Locked: nas, usb (open Herakles to unlock)"}},
		{"failed", daemon.Status{Schedule: sched, Last: failed}, nil, Status{Failed, "Last backup failed: home"}},
	}
	for _, c := range cases {
		if got := Summarize(c.st, c.locked, now); got != c.want {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestIcons(t *testing.T) {
	for _, s := range []State{Idle, Busy, Locked, Failed} {
		img, err := png.Decode(bytes.NewReader(PNG(64, s)))
		if err != nil {
			t.Fatal(err)
		}
		if b := img.Bounds(); b.Dx() != 64 || b.Dy() != 64 {
			t.Fatal(b)
		}
		if _, _, _, a := img.At(0, 0).RGBA(); a != 0 {
			t.Error("corner should be transparent")
		}
		if r, g, b, _ := img.At(12, 22).RGBA(); r>>8 != 0xd4 || g>>8 != 0xa0 || b>>8 != 0x17 {
			t.Errorf("state %d: shield colour %x %x %x", s, r>>8, g>>8, b>>8)
		}
		if r, g, b, _ := img.At(22, 30).RGBA(); r>>8 != 0xff || g>>8 != 0xff || b>>8 != 0xff {
			t.Error("H should be white")
		}
		r, g, b, _ := img.At(50, 50).RGBA()
		want, badge := badgeColors[s]
		if badge && (uint8(r>>8) != want.R || uint8(g>>8) != want.G || uint8(b>>8) != want.B) {
			t.Errorf("state %d: badge %x %x %x", s, r>>8, g>>8, b>>8)
		}
		if !badge {
			for _, c := range badgeColors {
				if uint8(r>>8) == c.R && uint8(g>>8) == c.G && uint8(b>>8) == c.B {
					t.Errorf("idle icon should have no badge")
				}
			}
		}
	}
}

func TestICO(t *testing.T) {
	ico := ICO(Busy)
	le := binary.LittleEndian
	if le.Uint16(ico[0:]) != 0 || le.Uint16(ico[2:]) != 1 || le.Uint16(ico[4:]) != 3 {
		t.Fatal("bad ICONDIR")
	}
	for i, n := range []int{16, 32, 48} {
		e := ico[6+16*i:]
		size, off := int(le.Uint32(e[8:])), int(le.Uint32(e[12:]))
		if int(e[0]) != n || int(e[1]) != n || le.Uint16(e[6:]) != 32 || off+size > len(ico) {
			t.Fatalf("entry %d: %v", i, e[:16])
		}
		h := ico[off:]
		if le.Uint32(h) != 40 || int(int32(le.Uint32(h[4:]))) != n || int(int32(le.Uint32(h[8:]))) != 2*n {
			t.Fatalf("entry %d: bad BITMAPINFOHEADER", i)
		}
		mask := ((n + 31) / 32) * 4 * n
		if size != 40+n*n*4+mask {
			t.Fatalf("entry %d: size %d", i, size)
		}
		// Rows are stored bottom-up: the last one is the image's top row.
		top := h[40+(n-1)*n*4:]
		if top[3] != 0 {
			t.Fatalf("entry %d: top-left pixel should be transparent", i)
		}
	}
}

func TestDesktopEntry(t *testing.T) {
	for in, want := range map[string]string{
		"/usr/bin/herakles":  "/usr/bin/herakles",
		"/home/a b/herakles": `"/home/a b/herakles"`,
		`/x/"q"$HOME\path`:   `"/x/\"q\"\$HOME\\path"`,
		"/data/100%":         "/data/100%%",
		"tray":               "tray",
		"--config=/a.yaml":   `"--config=/a.yaml"`,
	} {
		if got := desktopQuote(in); got != want {
			t.Errorf("desktopQuote(%q) = %s, want %s", in, got, want)
		}
	}
	e := desktopEntry(Command{Exe: "/opt/my apps/herakles", Args: []string{"--config", "/home/u/h.yaml", "tray"}}, "/i.png", true)
	for _, l := range []string{"[Desktop Entry]", `Exec="/opt/my apps/herakles" --config /home/u/h.yaml tray`, "Icon=/i.png", "Terminal=false", "X-GNOME-Autostart-enabled=true"} {
		if !strings.Contains(e, l+"\n") {
			t.Errorf("missing %q in\n%s", l, e)
		}
	}
}

func TestInstallUninstall(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("Windows uses the registry")
	}
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "cfg"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))
	written, err := Install(Command{Exe: "/usr/bin/herakles", Args: []string{"tray"}})
	if err != nil || len(written) != 3 {
		t.Fatal(written, err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "cfg", "autostart", "herakles.desktop"))
	if err != nil || !strings.Contains(string(b), "Exec=/usr/bin/herakles tray\n") {
		t.Fatal(string(b), err)
	}
	b, err = os.ReadFile(filepath.Join(dir, "data", "applications", "herakles.desktop"))
	if err != nil || !strings.Contains(string(b), "Exec=/usr/bin/herakles tray --open\n") {
		t.Fatal(string(b), err)
	}
	removed, err := Uninstall()
	if err != nil || len(removed) != 3 {
		t.Fatal(removed, err)
	}
	if removed, err = Uninstall(); err != nil || len(removed) != 0 {
		t.Fatal("second uninstall", removed, err)
	}
}

func TestApply(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("Windows uses the registry")
	}
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "cfg"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))
	entry := filepath.Join(dir, "cfg", "autostart", "herakles.desktop")
	cfg := config.New(filepath.Join(dir, "my config.yaml"))

	if changed, err := Apply(cfg); err != nil || changed != nil {
		t.Fatal("disabled and not installed must be a no-op", changed, err)
	}
	cfg.Tray.Autostart = true
	if changed, err := Apply(cfg); err != nil || len(changed) != 3 {
		t.Fatal(changed, err)
	}
	b, _ := os.ReadFile(entry)
	if !strings.Contains(string(b), `--config "`+filepath.Join(dir, "my config.yaml")+`" tray`+"\n") {
		t.Fatal(string(b))
	}
	if changed, err := Apply(cfg); err != nil || changed != nil {
		t.Fatal("already installed must be a no-op", changed, err)
	}

	// Disabling in another configuration leaves this one's entry alone.
	other := config.New(filepath.Join(dir, "other.yaml"))
	if changed, err := Apply(other); err != nil || changed != nil {
		t.Fatal(changed, err)
	}
	// Enabling it there takes the entry over.
	other.Tray.Autostart = true
	if changed, err := Apply(other); err != nil || len(changed) != 3 {
		t.Fatal(changed, err)
	}
	other.Tray.Autostart = false
	if changed, err := Apply(other); err != nil || len(changed) != 3 {
		t.Fatal("disabling removes its own entry", changed, err)
	}
	if _, err := os.Stat(entry); !os.IsNotExist(err) {
		t.Fatal("entry still there")
	}
}
