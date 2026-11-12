package imagefs

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The tests build real images with the system's tools (e2fsprogs,
// dosfstools, mtools, ntfs-3g, fdisk, xfsprogs) and skip what is missing.

func need(t *testing.T, tools ...string) {
	t.Helper()
	for _, tool := range tools {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " not installed")
		}
	}
}

func run(t *testing.T, name string, args ...string) {
	t.Helper()
	if out, err := exec.Command(name, args...).CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
}

// tree is the content put into every file system.
type tree struct {
	dir string
	big []byte
}

func makeTree(t *testing.T) *tree {
	dir := t.TempDir()
	tr := &tree{dir: dir, big: make([]byte, 3_000_000)}
	rand.Read(tr.big)
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(dir, "home/alice/Documents"), 0o755))
	must(os.MkdirAll(filepath.Join(dir, "many"), 0o755))
	must(os.WriteFile(filepath.Join(dir, "home/alice/Documents/notes.txt"), []byte("Hello from the image\n"), 0o644))
	must(os.WriteFile(filepath.Join(dir, "home/alice/big.bin"), tr.big, 0o644))
	must(os.Symlink("Documents/notes.txt", filepath.Join(dir, "home/alice/link")))
	must(os.Symlink(strings.Repeat("long/", 20)+"target", filepath.Join(dir, "home/alice/longlink")))
	must(os.Symlink("alice", filepath.Join(dir, "home/bob")))                       // to a folder
	must(os.Symlink("/home/alice/Documents", filepath.Join(dir, "home/alice/abs"))) // absolute: from the file system root
	must(os.Symlink("../../home/alice/../alice/Documents/notes.txt", filepath.Join(dir, "home/alice/up")))
	for i := 0; i < 400; i++ { // large enough for an indexed (htree) directory
		must(os.WriteFile(filepath.Join(dir, "many", fmt.Sprintf("file-%03d.txt", i)), []byte(fmt.Sprint(i)), 0o644))
	}
	return tr
}

func readAll(t *testing.T, f FS, p string) []byte {
	t.Helper()
	r, size, err := f.Open(p)
	if err != nil {
		t.Fatalf("open %s: %v", p, err)
	}
	b, err := io.ReadAll(io.NewSectionReader(r, 0, size))
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return b
}

func names(ents []Entry) map[string]Entry {
	m := map[string]Entry{}
	for _, e := range ents {
		m[e.Name] = e
	}
	return m
}

func openImage(t *testing.T, path string) []*Volume {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	st, _ := f.Stat()
	vols, err := Open(f, st.Size())
	if err != nil {
		t.Fatal(err)
	}
	return vols
}

// checkTree reads the test tree back from f.
func checkTree(t *testing.T, f FS, tr *tree, symlinks, many bool) {
	t.Helper()
	root, err := f.ReadDir("")
	if err != nil {
		t.Fatal(err)
	}
	if e, ok := names(root)["home"]; !ok || !e.IsDir() {
		t.Fatalf("root: %v", root)
	}
	if b := readAll(t, f, "home/alice/Documents/notes.txt"); string(b) != "Hello from the image\n" {
		t.Fatalf("notes.txt: %q", b)
	}
	if b := readAll(t, f, "/home/alice/big.bin"); !bytes.Equal(b, tr.big) {
		t.Fatalf("big.bin differs (%d bytes)", len(b))
	}
	// Random access in the middle of the large file.
	r, _, _ := f.Open("home/alice/big.bin")
	buf := make([]byte, 5000)
	if n, err := r.ReadAt(buf, 1_234_567); n != len(buf) || (err != nil && err != io.EOF) || !bytes.Equal(buf, tr.big[1_234_567:1_234_567+5000]) {
		t.Fatalf("ReadAt: %d %v", n, err)
	}
	if st, err := f.Stat("home/alice/big.bin"); err != nil || st.Size != int64(len(tr.big)) || st.Type != "file" {
		t.Fatalf("stat: %+v %v", st, err)
	}
	if _, err := f.Stat("home/nobody"); err == nil {
		t.Fatal("missing path found")
	}
	if symlinks {
		alice := names(must(f.ReadDir("home/alice")))
		if l := alice["link"]; l.Type != "symlink" || l.Link != "Documents/notes.txt" {
			t.Fatalf("fast symlink: %+v", l)
		}
		if l := alice["longlink"]; l.Type != "symlink" || !strings.HasSuffix(l.Link, "/target") {
			t.Fatalf("slow symlink: %+v", l)
		}
		// Links are followed inside the file system, never out of it.
		for _, p := range []string{"home/alice/link", "home/bob/Documents/notes.txt", "home/alice/abs/notes.txt", "home/alice/up"} {
			if b := readAll(t, f, p); string(b) != "Hello from the image\n" {
				t.Fatalf("%s: %q", p, b)
			}
		}
		if ents, err := f.ReadDir("home/bob"); err != nil || len(ents) != len(alice) {
			t.Fatalf("folder link: %v %v", ents, err)
		}
		if st, err := f.Stat("home/bob"); err != nil || st.Type != "symlink" {
			t.Fatalf("stat of a link describes the link: %+v %v", st, err)
		}
	}
	if many {
		ents := must(f.ReadDir("many"))
		if len(ents) != 400 {
			t.Fatalf("many: %d entries", len(ents))
		}
		if b := readAll(t, f, "many/file-399.txt"); string(b) != "399" {
			t.Fatalf("file-399: %q", b)
		}
	}
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func TestExt(t *testing.T) {
	need(t, "mkfs.ext4")
	tr := makeTree(t)
	for _, c := range []struct {
		name, kind string
		args       []string
	}{
		{"ext4", "ext4", []string{"-t", "ext4"}},
		{"ext4-old", "ext4", []string{"-t", "ext4", "-O", "^metadata_csum,^64bit"}},
		{"ext4-1k", "ext4", []string{"-t", "ext4", "-b", "1024"}},
		{"ext3", "ext3", []string{"-t", "ext3"}},
		{"ext2-1k", "ext2", []string{"-t", "ext2", "-b", "1024"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			img := filepath.Join(t.TempDir(), "fs.img")
			run(t, "mkfs.ext4", append(append([]string{"-q", "-F", "-L", "data"}, c.args...), "-d", tr.dir, img, "64M")...)
			vols := openImage(t, img)
			if len(vols) != 1 || vols[0].Type != c.kind || vols[0].Label != "data" || !vols[0].Browsable() {
				t.Fatalf("%+v", vols[0])
			}
			checkTree(t, vols[0].FS, tr, true, true)
		})
	}
}

func TestFAT(t *testing.T) {
	need(t, "mkfs.vfat", "mcopy", "mmd")
	tr := makeTree(t)
	for _, c := range []struct {
		kind, size string
		args       []string
	}{{"fat12", "4M", []string{"-F", "12"}}, {"fat16", "40M", []string{"-F", "16"}}, {"fat32", "300M", []string{"-F", "32"}}} {
		t.Run(c.kind, func(t *testing.T) {
			img := filepath.Join(t.TempDir(), "fs.img")
			run(t, "truncate", "-s", c.size, img)
			run(t, "mkfs.vfat", append(c.args, "-n", "BOOT", img)...)
			run(t, "mmd", "-i", img, "::/home", "::/home/alice", "::/home/alice/Documents")
			run(t, "mcopy", "-i", img, filepath.Join(tr.dir, "home/alice/Documents/notes.txt"), "::/home/alice/Documents/notes.txt")
			if c.kind != "fat12" {
				run(t, "mcopy", "-i", img, filepath.Join(tr.dir, "home/alice/big.bin"), "::/home/alice/big.bin")
			}
			vols := openImage(t, img)
			if len(vols) != 1 || vols[0].Type != c.kind || vols[0].Label != "BOOT" || !vols[0].Browsable() {
				t.Fatalf("%+v", vols[0])
			}
			f := vols[0].FS
			if b := readAll(t, f, "home/alice/Documents/notes.txt"); string(b) != "Hello from the image\n" {
				t.Fatalf("%q", b)
			}
			if st, err := f.Stat("home/alice"); err != nil || !st.IsDir() {
				t.Fatalf("%+v %v", st, err)
			}
			if c.kind != "fat12" {
				if b := readAll(t, f, "home/alice/big.bin"); !bytes.Equal(b, tr.big) {
					t.Fatal("big.bin differs")
				}
			}
		})
	}
}

func TestNTFS(t *testing.T) {
	need(t, "mkfs.ntfs", "ntfscp")
	tr := makeTree(t)
	img := filepath.Join(t.TempDir(), "fs.img")
	run(t, "truncate", "-s", "64M", img)
	run(t, "mkfs.ntfs", "-q", "-F", "-f", "-L", "Windows", img)
	run(t, "ntfscp", "-f", img, filepath.Join(tr.dir, "home/alice/Documents/notes.txt"), "notes.txt")
	run(t, "ntfscp", "-f", img, filepath.Join(tr.dir, "home/alice/big.bin"), "big.bin")
	vols := openImage(t, img)
	if len(vols) != 1 || vols[0].Type != "ntfs" || !vols[0].Browsable() {
		t.Fatalf("%+v", vols[0])
	}
	f := vols[0].FS
	root := names(must(f.ReadDir("")))
	if _, ok := root["notes.txt"]; !ok || len(root) != 2 {
		t.Fatalf("root (metadata files must be hidden): %v", root)
	}
	if b := readAll(t, f, "notes.txt"); string(b) != "Hello from the image\n" {
		t.Fatalf("%q", b)
	}
	if b := readAll(t, f, "big.bin"); !bytes.Equal(b, tr.big) {
		t.Fatal("big.bin differs")
	}
}

func TestPartitions(t *testing.T) {
	need(t, "mkfs.ext4", "mkfs.vfat", "sfdisk")
	tr := makeTree(t)
	dir := t.TempDir()
	ext := filepath.Join(dir, "ext.img")
	fat := filepath.Join(dir, "fat.img")
	run(t, "mkfs.ext4", "-q", "-F", "-L", "root", "-d", tr.dir, ext, "64M")
	run(t, "truncate", "-s", "40M", fat)
	run(t, "mkfs.vfat", "-n", "EFI", fat)
	place := func(disk, src string, sector int) {
		run(t, "dd", "if="+src, "of="+disk, "bs=512", fmt.Sprintf("seek=%d", sector), "conv=notrunc", "status=none")
	}

	gpt := filepath.Join(dir, "gpt.img")
	run(t, "truncate", "-s", "160M", gpt)
	sf := exec.Command("sfdisk", "-q", gpt)
	sf.Stdin = strings.NewReader("label: gpt\nstart=2048, size=40M, type=C12A7328-F81F-11D2-BA4B-00A0C93EC93B, name=\"EFI\"\nstart=83968, size=64M, type=0FC63DAF-8483-4772-8E79-3D69D8477DE4, name=\"root\"\nsize=8M, type=0657FD6D-A4AB-43C4-84E5-0933C84B4F4F\n")
	if out, err := sf.CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	place(gpt, fat, 2048)
	place(gpt, ext, 83968)
	vols := openImage(t, gpt)
	if len(vols) != 3 {
		t.Fatalf("%d volumes", len(vols))
	}
	if v := vols[0]; v.Name != "part1" || v.Type != "fat16" || v.Part != "EFI" || v.Label != "EFI" || !v.Browsable() {
		t.Fatalf("part1: %+v", v)
	}
	if v := vols[1]; v.Name != "part2" || v.Type != "ext4" || v.Part != "root" || !v.Browsable() {
		t.Fatalf("part2: %+v", v)
	}
	if v := vols[2]; v.Browsable() || v.Part != "Linux swap" || v.Note == "" {
		t.Fatalf("part3: %+v", v)
	}
	checkTree(t, vols[1].FS, tr, true, true)

	// MBR with an extended partition holding a logical one (part5).
	mbr := filepath.Join(dir, "mbr.img")
	run(t, "truncate", "-s", "120M", mbr)
	sf = exec.Command("sfdisk", "-q", mbr)
	sf.Stdin = strings.NewReader("label: dos\nstart=2048, size=40M, type=c\nstart=83968, size=70M, type=5\nstart=86016, size=64M, type=83\n")
	if out, err := sf.CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	place(mbr, fat, 2048)
	place(mbr, ext, 86016)
	vols = openImage(t, mbr)
	if len(vols) != 2 || vols[0].Name != "part1" || vols[0].Type != "fat16" || vols[1].Name != "part5" || vols[1].Type != "ext4" {
		for _, v := range vols {
			t.Logf("%+v", v)
		}
		t.Fatal("MBR partitions")
	}
	checkTree(t, vols[1].FS, tr, true, false)
}

func TestNotBrowsable(t *testing.T) {
	dir := t.TempDir()
	if _, err := exec.LookPath("mkfs.xfs"); err == nil {
		img := filepath.Join(dir, "xfs.img")
		run(t, "truncate", "-s", "300M", img)
		run(t, "mkfs.xfs", "-q", "-L", "data", img)
		vols := openImage(t, img)
		if vols[0].Type != "xfs" || vols[0].Browsable() || vols[0].Note == "" {
			t.Fatalf("%+v", vols[0])
		}
	}
	luks := filepath.Join(dir, "luks.img")
	b := make([]byte, 1<<20)
	copy(b, "LUKS\xba\xbe")
	os.WriteFile(luks, b, 0o644)
	if v := openImage(t, luks)[0]; v.Type != "luks" || v.Browsable() || !strings.Contains(v.Note, "encrypted") {
		t.Fatalf("%+v", v)
	}
	empty := filepath.Join(dir, "empty.img")
	os.WriteFile(empty, make([]byte, 1<<20), 0o644)
	if v := openImage(t, empty)[0]; v.Browsable() || v.Note == "" {
		t.Fatalf("%+v", v)
	}
}

// TestCorrupt feeds damaged file systems: errors, never panics.
func TestCorrupt(t *testing.T) {
	need(t, "mkfs.ext4")
	tr := makeTree(t)
	img := filepath.Join(t.TempDir(), "fs.img")
	run(t, "mkfs.ext4", "-q", "-F", "-d", tr.dir, img, "32M")
	data, _ := os.ReadFile(img)
	for i := 0; i < 30; i++ {
		bad := bytes.Clone(data)
		// Scribble over metadata past the superblock (group descriptors,
		// inode tables, directories).
		noise := make([]byte, 64<<10)
		rand.Read(noise)
		off := 4096 + i*(len(bad)/64)
		copy(bad[off:], noise)
		vols, err := Open(bytes.NewReader(bad), int64(len(bad)))
		if err != nil || len(vols) == 0 || !vols[0].Browsable() {
			continue
		}
		f := vols[0].FS
		var walk func(p string, depth int)
		walk = func(p string, depth int) {
			ents, err := f.ReadDir(p)
			if err != nil || depth > 6 {
				return
			}
			for _, e := range ents {
				q := strings.TrimPrefix(p+"/"+e.Name, "/")
				if e.IsDir() {
					walk(q, depth+1)
				} else if e.Type == "file" {
					if r, size, err := f.Open(q); err == nil {
						io.Copy(io.Discard, io.NewSectionReader(r, 0, min(size, 1<<20)))
					}
				}
			}
		}
		walk("", 0)
	}
}
