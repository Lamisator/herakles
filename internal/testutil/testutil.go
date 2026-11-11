// Package testutil provides helpers shared by tests.
package testutil

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"
	"time"

	"github.com/lamisator/herakles/internal/backend"
	"github.com/lamisator/herakles/internal/chunker"
	"github.com/lamisator/herakles/internal/crypto"
	"github.com/lamisator/herakles/internal/repository"
)

func init() { crypto.DefaultKDFParams = crypto.KDFParams{Time: 1, Memory: 1024, Threads: 1} }

// SmallChunks makes tests exercise multi-chunk files without large data.
var SmallChunks = chunker.Params{Min: 4 << 10, Avg: 16 << 10, Max: 64 << 10}

// Password is the test repository password.
var Password = crypto.Credentials{Password: "test-password"}

// NewRepo creates an in-memory repository.
func NewRepo(t testing.TB) (*repository.Repository, *backend.Mem) {
	t.Helper()
	be := backend.NewMem()
	_, err := repository.Init(context.Background(), be, Password, SmallChunks)
	if err != nil {
		t.Fatal(err)
	}
	return Reopen(t, be), be
}

// Reopen opens the repository on be with small packs.
func Reopen(t testing.TB, be backend.Backend) *repository.Repository {
	t.Helper()
	opts := repository.DefaultOptions()
	opts.PackSize = 256 << 10
	r, err := repository.Open(context.Background(), be, Password, opts)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// RandBytes returns deterministic random data.
func RandBytes(seed int64, n int) []byte {
	b := make([]byte, n)
	rand.New(rand.NewSource(seed)).Read(b)
	return b
}

// WriteFile writes data creating parent directories.
func WriteFile(t testing.TB, p string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// MakeTree creates a sample tree under dir.
func MakeTree(t testing.TB, dir string) {
	t.Helper()
	WriteFile(t, filepath.Join(dir, "small.txt"), []byte("hello herakles\n"))
	WriteFile(t, filepath.Join(dir, "empty"), nil)
	WriteFile(t, filepath.Join(dir, "big.bin"), RandBytes(1, 700<<10))
	WriteFile(t, filepath.Join(dir, "dup1.bin"), RandBytes(2, 200<<10))
	WriteFile(t, filepath.Join(dir, "sub", "dup2.bin"), RandBytes(2, 200<<10))
	WriteFile(t, filepath.Join(dir, "sub", "deeper", "text.md"), bytes.Repeat([]byte("compressible text "), 5000))
	WriteFile(t, filepath.Join(dir, "sub", "zeros.img"), make([]byte, 300<<10))
	WriteFile(t, filepath.Join(dir, "cache", "junk.tmp"), []byte("junk"))
	if err := os.MkdirAll(filepath.Join(dir, "emptydir"), 0o750); err != nil {
		t.Fatal(err)
	}
	os.Chmod(filepath.Join(dir, "small.txt"), 0o600)
	if runtime.GOOS != "windows" {
		if err := os.Symlink("small.txt", filepath.Join(dir, "link")); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	os.Chtimes(filepath.Join(dir, "big.bin"), old, old)
}

// Entry describes a file for comparison.
type Entry struct {
	Mode    fs.FileMode
	Size    int64
	Hash    [32]byte
	Link    string
	ModTime time.Time
}

// Snapshot captures a directory tree for comparison.
func Snapshot(t testing.TB, dir string) map[string]Entry {
	t.Helper()
	out := map[string]Entry{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		if rel == "." {
			return nil
		}
		fi, err := os.Lstat(p)
		if err != nil {
			return err
		}
		e := Entry{Mode: fi.Mode()}
		switch {
		case fi.Mode().IsRegular():
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			e.Size, e.Hash, e.ModTime = fi.Size(), sha256.Sum256(data), fi.ModTime()
		case fi.Mode()&fs.ModeSymlink != 0:
			e.Link, _ = os.Readlink(p)
		}
		out[filepath.ToSlash(rel)] = e
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// CompareTrees fails if two directory snapshots differ.
func CompareTrees(t testing.TB, want, got map[string]Entry) {
	t.Helper()
	var keys []string
	for k := range want {
		keys = append(keys, k)
	}
	for k := range got {
		if _, ok := want[k]; !ok {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var diffs []string
	for _, k := range keys {
		w, ok1 := want[k]
		g, ok2 := got[k]
		switch {
		case !ok1:
			diffs = append(diffs, "unexpected "+k)
		case !ok2:
			diffs = append(diffs, "missing "+k)
		case w.Mode != g.Mode:
			diffs = append(diffs, fmt.Sprintf("%s: mode %v != %v", k, g.Mode, w.Mode))
		case w.Size != g.Size || w.Hash != g.Hash:
			diffs = append(diffs, k+": content differs")
		case w.Link != g.Link:
			diffs = append(diffs, k+": link target differs")
		case !w.ModTime.Equal(g.ModTime):
			diffs = append(diffs, fmt.Sprintf("%s: mtime %v != %v", k, g.ModTime, w.ModTime))
		}
	}
	if len(diffs) > 0 {
		t.Fatalf("trees differ:\n%v", diffs)
	}
}
