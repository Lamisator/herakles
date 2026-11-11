//go:build linux

package fsutil

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lamisator/herakles/internal/repository"
	"golang.org/x/sys/unix"
)

func TestMetadataRoundtrip(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	os.WriteFile(src, []byte("x"), 0o640)
	mt := time.Date(2021, 5, 6, 7, 8, 9, 123456789, time.UTC)
	os.Chtimes(src, mt, mt)
	hasXattr := unix.Setxattr(src, "user.herakles", []byte("hello"), 0) == nil
	fi, _ := os.Lstat(src)
	n := NodeFromInfo(src, fi)
	if n.Type != repository.NodeFile || n.Size != 1 || n.Mode.Perm() != 0o640 || !n.ModTime.Equal(mt) || n.Inode == 0 {
		t.Fatalf("node: %+v", n)
	}
	if hasXattr && !bytes.Equal(n.Xattrs["user.herakles"], []byte("hello")) {
		t.Fatalf("xattrs: %v", n.Xattrs)
	}
	dst := filepath.Join(dir, "dst")
	os.WriteFile(dst, []byte("x"), 0o600)
	if err := RestoreMetadata(dst, n, true); err != nil {
		t.Fatal(err)
	}
	fi2, _ := os.Lstat(dst)
	if fi2.Mode().Perm() != 0o640 || !fi2.ModTime().Equal(mt) {
		t.Fatalf("restored: %v %v", fi2.Mode(), fi2.ModTime())
	}
	if hasXattr {
		buf := make([]byte, 16)
		sz, err := unix.Getxattr(dst, "user.herakles", buf)
		if err != nil || string(buf[:sz]) != "hello" {
			t.Fatalf("xattr not restored: %v", err)
		}
	}
}

func TestSpecialFiles(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "fifo")
	if err := unix.Mkfifo(fifo, 0o600); err != nil {
		t.Skip("mkfifo not permitted")
	}
	link := filepath.Join(dir, "link")
	os.Symlink("target", link)
	fi, _ := os.Lstat(fifo)
	n := NodeFromInfo(fifo, fi)
	if n.Type != repository.NodeFIFO {
		t.Fatalf("fifo type %s", n.Type)
	}
	out := filepath.Join(dir, "fifo2")
	if err := CreateSpecial(out, n); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Lstat(out); fi.Mode()&os.ModeNamedPipe == 0 {
		t.Fatal("fifo not recreated")
	}
	fi, _ = os.Lstat(link)
	ln := NodeFromInfo(link, fi)
	if ln.Type != repository.NodeSymlink || ln.LinkTarget != "target" {
		t.Fatalf("symlink: %+v", ln)
	}
	// Symlink timestamps are set without following the link.
	if err := RestoreMetadata(link, ln, false); err != nil && !strings.Contains(err.Error(), "operation not permitted") {
		t.Fatal(err)
	}
}

func TestAlignedReaderAndDevice(t *testing.T) {
	data := bytes.Repeat([]byte("0123456789"), 1000)
	r := NewAlignedReader(bytes.NewReader(data), 512)
	var out bytes.Buffer
	buf := make([]byte, 333)
	for {
		n, err := r.Read(buf)
		out.Write(buf[:n])
		if err != nil {
			break
		}
	}
	if !bytes.Equal(out.Bytes(), data) {
		t.Fatal("aligned reader corrupted data")
	}
	p := filepath.Join(t.TempDir(), "img")
	os.WriteFile(p, data, 0o600)
	f, size, err := OpenDevice(p)
	if err != nil || size != int64(len(data)) {
		t.Fatalf("OpenDevice on image file: %v %d", err, size)
	}
	f.Close()
}
