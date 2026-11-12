package gui

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lamisator/herakles/internal/config"
	"github.com/lamisator/herakles/internal/engine"
	tu "github.com/lamisator/herakles/internal/testutil"
)

// TestDiskImageBrowse backs up a partitioned disk image and reads its files
// through the explorer API: partitions, folders, preview, search, download
// and restore.
func TestDiskImageBrowse(t *testing.T) {
	for _, tool := range []string{"mkfs.ext4", "mkfs.vfat", "sfdisk"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " not installed")
		}
	}
	dir := t.TempDir()
	run := func(name string, stdin string, args ...string) {
		cmd := exec.Command(name, args...)
		cmd.Stdin = strings.NewReader(stdin)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s: %v\n%s", name, err, out)
		}
	}
	tree := filepath.Join(dir, "tree")
	os.MkdirAll(filepath.Join(tree, "home/alice/Documents"), 0o755)
	os.WriteFile(filepath.Join(tree, "home/alice/Documents/notes.txt"), []byte("Hello from the disk\n"), 0o644)
	big := make([]byte, 2_500_000)
	rand.Read(big)
	os.WriteFile(filepath.Join(tree, "home/alice/big.bin"), big, 0o644)
	ext := filepath.Join(dir, "ext.img")
	fat := filepath.Join(dir, "fat.img")
	disk := filepath.Join(dir, "disk.img")
	run("mkfs.ext4", "", "-q", "-F", "-L", "root", "-d", tree, ext, "64M")
	run("truncate", "", "-s", "40M", fat)
	run("mkfs.vfat", "", "-n", "EFI", fat)
	run("truncate", "", "-s", "130M", disk)
	run("sfdisk", "label: gpt\nstart=2048, size=40M, type=C12A7328-F81F-11D2-BA4B-00A0C93EC93B, name=\"EFI\"\nstart=83968, size=64M, type=0FC63DAF-8483-4772-8E79-3D69D8477DE4, name=\"root\"\nsize=8M, type=0657FD6D-A4AB-43C4-84E5-0933C84B4F4F\n", "-q", disk)
	run("dd", "", "if="+fat, "of="+disk, "bs=512", "seek=2048", "conv=notrunc", "status=none")
	run("dd", "", "if="+ext, "of="+disk, "bs=512", "seek=83968", "conv=notrunc", "status=none")

	cfg := config.New(filepath.Join(dir, "h.yaml"))
	cfg.StateDir = filepath.Join(dir, "state")
	cfg.Daemon.ControlListen = "127.0.0.1:1"
	rc := &config.Repository{Name: "vault", Location: filepath.Join(dir, "repo"), Password: tu.Password.Password}
	cfg.Repositories = []*config.Repository{rc}
	cfg.Jobs = []*config.Job{{Name: "disk", Repository: "vault", Devices: []string{disk}}}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	eng := engine.New(cfg, nil)
	if _, err := eng.Init(context.Background(), rc, tu.Password); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.RunJob(context.Background(), "disk", engine.RunOptions{}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := NewServer(ctx, eng, "test")
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	c := &client{t: t, url: ts.URL, tok: s.Token()}

	var snaps []struct{ ID string }
	c.json("GET", "/api/repos/vault/snapshots", nil, &snaps)
	if len(snaps) != 1 {
		t.Fatal("snapshots:", snaps)
	}
	base := "/api/repos/vault/snapshots/" + snaps[0].ID
	q := func(p string) string { return url.QueryEscape(p) }
	img := disk // the image's path in the snapshot

	var vols []treeEntry
	if code := c.json("GET", base+"/tree?path="+q(img), nil, &vols); code != 200 || len(vols) != 3 {
		t.Fatalf("partitions: %d %+v", code, vols)
	}
	if v := vols[0]; v.Type != "volume" || v.Name != "part1" || !v.Browsable || !strings.Contains(v.Label, "fat16") {
		t.Fatalf("part1: %+v", v)
	}
	if v := vols[1]; v.Name != "part2" || !v.Browsable || !strings.Contains(v.Label, "ext4") || v.Path != img+"/part2" {
		t.Fatalf("part2: %+v", v)
	}
	if v := vols[2]; v.Browsable || v.Note == "" {
		t.Fatalf("part3 (swap): %+v", v)
	}
	if code, body := c.do("GET", base+"/tree?path="+q(img+"/part3"), nil); code != 400 || !strings.Contains(string(body), "swap") {
		t.Fatalf("swap must explain itself: %d %s", code, body)
	}

	var alice []treeEntry
	if code := c.json("GET", base+"/tree?path="+q(img+"/part2/home/alice"), nil, &alice); code != 200 || len(alice) != 2 {
		t.Fatalf("alice: %d %+v", code, alice)
	}
	if code, body := c.do("GET", base+"/preview?path="+q(img+"/part2/home/alice/Documents/notes.txt"), nil); code != 200 || string(body) != "Hello from the disk\n" {
		t.Fatalf("preview: %d %q", code, body)
	}
	if code, body := c.do("GET", base+"/download?path="+q(img+"/part2/home/alice/big.bin"), nil); code != 200 || !bytes.Equal(body, big) {
		t.Fatalf("download: %d, %d bytes", code, len(body))
	}
	var found struct{ Results []treeEntry }
	if code := c.json("GET", base+"/find?q=notes&path="+q(img+"/part2"), nil, &found); code != 200 || len(found.Results) != 1 || found.Results[0].Path != img+"/part2/home/alice/Documents/notes.txt" {
		t.Fatalf("find: %d %+v", code, found)
	}

	// A folder as zip.
	code, body := c.do("GET", base+"/download?format=zip&path="+q(img+"/part2/home"), nil)
	if code != 200 {
		t.Fatal("zip:", code, string(body))
	}
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for _, f := range zr.File {
		got[f.Name] = int(f.UncompressedSize64)
	}
	if got["home/alice/Documents/notes.txt"] != 20 || got["home/alice/big.bin"] != len(big) {
		t.Fatalf("zip content: %v", got)
	}

	// Restore a folder from the image.
	target := filepath.Join(dir, "restored")
	var res struct {
		Result struct{ Files int }
		Errors []string
	}
	reqBody := map[string]any{"paths": []string{img + "/part2/home/alice/Documents"}, "target": target}
	if code := c.json("POST", base+"/restore", reqBody, &res); code != 200 || res.Result.Files != 1 || len(res.Errors) != 0 {
		raw, _ := json.Marshal(res)
		t.Fatalf("restore: %d %s", code, raw)
	}
	if b, err := os.ReadFile(filepath.Join(target, "Documents", "notes.txt")); err != nil || string(b) != "Hello from the disk\n" {
		t.Fatal("restored file:", string(b), err)
	}
	if n := len(s.images.items); n != 1 {
		t.Fatal(fmt.Sprint("the image must be opened once and kept: ", n))
	}
}
