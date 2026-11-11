package restorer_test

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/lamisator/herakles/internal/backend"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lamisator/herakles/internal/archiver"
	"github.com/lamisator/herakles/internal/repository"
	"github.com/lamisator/herakles/internal/restorer"
	tu "github.com/lamisator/herakles/internal/testutil"
)

// snapRel maps an absolute path to its location inside a full restore
// ("C:\\x" is stored as "C/x").
func snapRel(p string) string {
	p = filepath.ToSlash(p)
	if len(p) >= 2 && p[1] == ':' {
		p = p[:1] + p[2:]
	}
	return filepath.FromSlash(strings.TrimPrefix(p, "/"))
}

func backup(t *testing.T, repo *repository.Repository, opts archiver.Options) *repository.Snapshot {
	t.Helper()
	a, err := archiver.New(repo, opts)
	if err != nil {
		t.Fatal(err)
	}
	s, err := a.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestBackupRestoreRoundtrip(t *testing.T) {
	ctx := context.Background()
	repo, be := tu.NewRepo(t)
	src := t.TempDir()
	tu.MakeTree(t, src)
	snap := backup(t, repo, archiver.Options{Paths: []string{src}, Job: "test", Type: repository.TypeFull})
	if snap.Summary.TotalFiles < 8 || snap.Summary.Errors != 0 {
		t.Fatalf("summary: %+v", snap.Summary)
	}

	// Restore from a fresh repository handle.
	repo2 := tu.Reopen(t, be)
	dst := t.TempDir()
	res, err := restorer.Restore(ctx, repo2, snap, src, restorer.Options{Target: dst, Verify: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Errors != 0 || res.Verified == 0 {
		t.Fatalf("restore result %+v", res)
	}
	tu.CompareTrees(t, tu.Snapshot(t, src), tu.Snapshot(t, filepath.Join(dst, filepath.Base(src))))

	// Full restore keeps the absolute path layout.
	dst2 := t.TempDir()
	if _, err := restorer.Restore(ctx, repo2, snap, "/", restorer.Options{Target: dst2}); err != nil {
		t.Fatal(err)
	}
	full := filepath.Join(dst2, snapRel(src))
	tu.CompareTrees(t, tu.Snapshot(t, src), tu.Snapshot(t, full))
}

func TestIncrementalAndDedup(t *testing.T) {
	ctx := context.Background()
	repo, _ := tu.NewRepo(t)
	src := t.TempDir()
	tu.MakeTree(t, src)
	s1 := backup(t, repo, archiver.Options{Paths: []string{src}, Type: repository.TypeFull})

	// No changes: incremental reads nothing and stores (almost) nothing.
	s2 := backup(t, repo, archiver.Options{Paths: []string{src}, Type: repository.TypeIncremental, Parent: s1})
	if s2.Summary.FilesUnmodified != s2.Summary.TotalFiles-0 || s2.Summary.BlobsAdded != 0 {
		t.Fatalf("unchanged incremental: %+v", s2.Summary)
	}
	// Compare the source directory's own subtree: the root tree also holds
	// metadata of parent directories such as /tmp, which other tests running
	// in parallel may modify between the two backups.
	n1, err1 := repo.FindNode(ctx, s1.Tree, src)
	n2, err2 := repo.FindNode(ctx, s2.Tree, src)
	if err1 != nil || err2 != nil || *n1.Subtree != *n2.Subtree {
		t.Fatal("identical data produced a different tree")
	}

	// Modify one file and add another.
	tu.WriteFile(t, filepath.Join(src, "small.txt"), []byte("changed content"))
	tu.WriteFile(t, filepath.Join(src, "sub", "new.txt"), []byte("new file"))
	s3 := backup(t, repo, archiver.Options{Paths: []string{src}, Type: repository.TypeIncremental, Parent: s2})
	if s3.Summary.FilesChanged != 1 || s3.Summary.FilesNew != 1 {
		t.Fatalf("incremental after change: %+v", s3.Summary)
	}
	if s3.Summary.BytesAdded > 1000 {
		t.Fatalf("incremental stored %d bytes for a tiny change", s3.Summary.BytesAdded)
	}
	// A full backup re-reads everything but dedup still stores nothing new.
	s4 := backup(t, repo, archiver.Options{Paths: []string{src}, Type: repository.TypeFull, Parent: s3})
	if s4.Summary.FilesUnmodified != 0 || s4.Summary.BlobsAdded != 0 || s4.Parent != nil {
		t.Fatalf("full backup: %+v parent=%v", s4.Summary, s4.Parent)
	}
	// Each snapshot restores its own state.
	dst := t.TempDir()
	if _, err := restorer.Restore(ctx, repo, s1, filepath.Join(src, "small.txt"), restorer.Options{Target: dst}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "small.txt")); string(b) != "hello herakles\n" {
		t.Fatalf("old version not restored: %q", b)
	}
	changes, err := restorer.Diff(ctx, repo, s1.Tree, s3.Tree)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]string{}
	for _, c := range changes {
		kinds[filepath.Base(c.Path)] = c.Kind
	}
	if kinds["small.txt"] != "modified" || kinds["new.txt"] != "added" || len(changes) != 2 {
		t.Fatalf("diff: %+v", changes)
	}
}

func TestExcludes(t *testing.T) {
	repo, _ := tu.NewRepo(t)
	src := t.TempDir()
	tu.MakeTree(t, src)
	tu.WriteFile(t, filepath.Join(src, "skipme", ".nobackup"), nil)
	tu.WriteFile(t, filepath.Join(src, "skipme", "secret"), []byte("x"))
	tu.WriteFile(t, filepath.Join(src, "cachedir", "CACHEDIR.TAG"), []byte("Signature: 8a477f597d28d172789f06886806bc55\n"))
	tu.WriteFile(t, filepath.Join(src, "cachedir", "c"), []byte("x"))
	snap := backup(t, repo, archiver.Options{
		Paths: []string{src}, Excludes: []string{"*.tmp", "**/deeper", filepath.Join(src, "big.bin")},
		ExcludeIfPresent: []string{".nobackup"}, ExcludeCaches: true, ExcludeLargerThan: 250 << 10,
	})
	var got []string
	repo.Walk(context.Background(), snap.Tree, func(p string, n *repository.Node) error {
		if strings.HasPrefix(p, filepath.ToSlash(src)) || true {
			got = append(got, filepath.Base(p))
		}
		return nil
	})
	joined := strings.Join(got, ",")
	for _, bad := range []string{"junk.tmp", "deeper", "text.md", "big.bin", "skipme", "cachedir", "zeros.img"} {
		if strings.Contains(","+joined+",", ","+bad+",") {
			t.Errorf("%s should have been excluded: %s", bad, joined)
		}
	}
	for _, good := range []string{"small.txt", "dup1.bin", "dup2.bin"} {
		if !strings.Contains(joined, good) {
			t.Errorf("%s missing: %s", good, joined)
		}
	}
}

func TestDeviceImageBackup(t *testing.T) {
	ctx := context.Background()
	repo, _ := tu.NewRepo(t)
	dir := t.TempDir()
	img := filepath.Join(dir, "disk.img")
	data := append(tu.RandBytes(9, 500<<10), make([]byte, 1<<20)...)
	tu.WriteFile(t, img, data)
	snap := backup(t, repo, archiver.Options{Devices: []string{img}, Type: repository.TypeFull})
	node, err := repo.FindNode(ctx, snap.Tree, img)
	if err != nil {
		t.Fatal(err)
	}
	if node.Type != repository.NodeDevice || node.Size != uint64(len(data)) || node.Source != img {
		t.Fatalf("device node: %+v", node)
	}
	out := filepath.Join(dir, "restored.img")
	if err := restorer.RestoreDevice(ctx, repo, node, out, nil); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(out)
	if !bytes.Equal(got, data) {
		t.Fatal("device image differs")
	}
	// Regular restore yields an image file (sparse for the zero tail).
	dst := t.TempDir()
	if _, err := restorer.Restore(ctx, repo, snap, img, restorer.Options{Target: dst, Verify: true}); err != nil {
		t.Fatal(err)
	}
	got, _ = os.ReadFile(filepath.Join(dst, "disk.img"))
	if !bytes.Equal(got, data) {
		t.Fatal("image file differs")
	}
}

func TestRestoreFiltersAndOverwrite(t *testing.T) {
	ctx := context.Background()
	repo, _ := tu.NewRepo(t)
	src := t.TempDir()
	tu.MakeTree(t, src)
	snap := backup(t, repo, archiver.Options{Paths: []string{src}})
	dst := t.TempDir()
	res, err := restorer.Restore(ctx, repo, snap, src, restorer.Options{Target: dst, Includes: []string{"*.bin"}, Excludes: []string{"dup2.bin"}})
	if err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(dst, filepath.Base(src))
	if _, err := os.Stat(filepath.Join(base, "big.bin")); err != nil {
		t.Fatal("big.bin not restored")
	}
	for _, n := range []string{"small.txt", "sub/dup2.bin", "sub/deeper"} {
		if _, err := os.Stat(filepath.Join(base, n)); err == nil {
			t.Fatalf("%s restored despite filters", n)
		}
	}
	if res.Files != 2 {
		t.Fatalf("restored %d files", res.Files)
	}
	// never: existing files are kept
	os.WriteFile(filepath.Join(base, "big.bin"), []byte("local"), 0o644)
	res, _ = restorer.Restore(ctx, repo, snap, src, restorer.Options{Target: dst, Includes: []string{"big.bin"}, Overwrite: restorer.OverwriteNever})
	if b, _ := os.ReadFile(filepath.Join(base, "big.bin")); string(b) != "local" || res.Skipped != 1 {
		t.Fatal("overwrite=never replaced a file")
	}
	// if-changed restores the differing file, then skips it the second time.
	restorer.Restore(ctx, repo, snap, src, restorer.Options{Target: dst, Includes: []string{"big.bin"}, Overwrite: restorer.OverwriteIfChanged})
	res, _ = restorer.Restore(ctx, repo, snap, src, restorer.Options{Target: dst, Includes: []string{"big.bin"}, Overwrite: restorer.OverwriteIfChanged})
	if res.Skipped != 1 {
		t.Fatalf("if-changed did not skip identical file: %+v", res)
	}
	// Dry run writes nothing.
	dry := t.TempDir()
	res, _ = restorer.Restore(ctx, repo, snap, src, restorer.Options{Target: dry, DryRun: true})
	if entries, _ := os.ReadDir(dry); len(entries) != 0 || res.Files == 0 {
		t.Fatal("dry run wrote files")
	}
}

func TestDumpAndFind(t *testing.T) {
	ctx := context.Background()
	repo, _ := tu.NewRepo(t)
	src := t.TempDir()
	tu.MakeTree(t, src)
	snap := backup(t, repo, archiver.Options{Paths: []string{src}})
	n, err := repo.FindNode(ctx, snap.Tree, filepath.Join(src, "small.txt"))
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := restorer.DumpFile(ctx, repo, n, &buf); err != nil || buf.String() != "hello herakles\n" {
		t.Fatalf("dump: %v %q", err, buf.String())
	}
	dir, _ := repo.FindNode(ctx, snap.Tree, filepath.Join(src, "sub"))
	buf.Reset()
	if err := restorer.DumpTar(ctx, repo, dir, &buf); err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(&buf)
	names := map[string]int64{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names[h.Name] = h.Size
	}
	if names["sub/dup2.bin"] != 200<<10 || len(names) < 5 {
		t.Fatalf("tar: %v", names)
	}
	m, err := restorer.Find(ctx, repo, []*repository.Snapshot{snap}, "*.bin", 0)
	if err != nil || len(m) != 3 {
		t.Fatalf("find: %v %d", err, len(m))
	}
}

func TestMultipleSourcesAndOverlap(t *testing.T) {
	ctx := context.Background()
	repo, _ := tu.NewRepo(t)
	a, b := t.TempDir(), t.TempDir()
	tu.WriteFile(t, filepath.Join(a, "x", "1"), []byte("1"))
	tu.WriteFile(t, filepath.Join(b, "2"), []byte("2"))
	snap := backup(t, repo, archiver.Options{Paths: []string{a, filepath.Join(a, "x"), b, filepath.Join(b, "2")}})
	if len(snap.Paths) != 4 {
		t.Fatalf("paths %v", snap.Paths)
	}
	for _, p := range []string{filepath.Join(a, "x", "1"), filepath.Join(b, "2")} {
		if _, err := repo.FindNode(ctx, snap.Tree, p); err != nil {
			t.Fatal(err)
		}
	}
	if snap.Summary.TotalFiles != 2 {
		t.Fatalf("overlapping sources counted twice: %d", snap.Summary.TotalFiles)
	}
}

func TestUnreadableFileIsReportedNotFatal(t *testing.T) {
	if os.Geteuid() == 0 || runtime.GOOS == "windows" {
		t.Skip("root can read everything")
	}
	repo, _ := tu.NewRepo(t)
	src := t.TempDir()
	tu.WriteFile(t, filepath.Join(src, "ok"), []byte("ok"))
	tu.WriteFile(t, filepath.Join(src, "locked"), []byte("no"))
	os.Chmod(filepath.Join(src, "locked"), 0)
	snap := backup(t, repo, archiver.Options{Paths: []string{src}})
	if snap.Summary.Errors != 1 || snap.Summary.TotalFiles != 1 {
		t.Fatalf("summary %+v", snap.Summary)
	}
}

// failingBackend makes pack uploads fail after a few writes.
type failingBackend struct {
	backend.Backend
	left atomic.Int32
}

func (f *failingBackend) Save(ctx context.Context, h backend.Handle, data []byte) error {
	if h.Type == backend.PackFile && f.left.Add(-1) < 0 {
		return errors.New("disk full")
	}
	return f.Backend.Save(ctx, h, data)
}

func TestRepositoryErrorsAbortBackup(t *testing.T) {
	_, mem := tu.NewRepo(t)
	fb := &failingBackend{Backend: mem}
	fb.left.Store(1)
	repo := tu.Reopen(t, fb)
	src := t.TempDir()
	for i := 0; i < 20; i++ {
		tu.WriteFile(t, filepath.Join(src, fmt.Sprint(i)), tu.RandBytes(int64(i), 100<<10))
	}
	a, _ := archiver.New(repo, archiver.Options{Paths: []string{src}, FileWorkers: 4})
	if _, err := a.Run(context.Background()); err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("expected disk full error, got %v", err)
	}
	snaps, _ := repo.Snapshots(context.Background())
	if len(snaps) != 0 {
		t.Fatal("snapshot saved despite storage failure")
	}
}

func TestCancelledBackupLeavesUsableRepository(t *testing.T) {
	repo, be := tu.NewRepo(t)
	src := t.TempDir()
	for i := 0; i < 40; i++ {
		tu.WriteFile(t, filepath.Join(src, fmt.Sprint(i)), tu.RandBytes(int64(i), 200<<10))
	}
	ctx, cancel := context.WithCancel(context.Background())
	prog := &archiver.Progress{}
	a, _ := archiver.New(repo, archiver.Options{Paths: []string{src}, Progress: prog})
	go func() {
		for prog.Stats().FilesDone < 5 {
			time.Sleep(time.Millisecond)
		}
		cancel()
	}()
	if _, err := a.Run(ctx); err == nil {
		t.Skip("backup finished before cancellation")
	}
	// A new backup on a fresh handle succeeds and restores correctly.
	repo2 := tu.Reopen(t, be)
	snap := backup(t, repo2, archiver.Options{Paths: []string{src}})
	dst := t.TempDir()
	if _, err := restorer.Restore(context.Background(), repo2, snap, src, restorer.Options{Target: dst, Verify: true}); err != nil {
		t.Fatal(err)
	}
	tu.CompareTrees(t, tu.Snapshot(t, src), tu.Snapshot(t, filepath.Join(dst, filepath.Base(src))))
	res, err := repo2.Check(context.Background(), repository.CheckOptions{ReadData: 1})
	if err != nil || !res.OK() {
		t.Fatalf("check after cancelled backup: %v %+v", err, res)
	}
	if _, err := repo2.Prune(context.Background(), repository.PruneOptions{}); err != nil {
		t.Fatal(err)
	}
	if res, _ := repo2.Check(context.Background(), repository.CheckOptions{ReadData: 1}); !res.OK() || len(res.Warnings) != 0 {
		t.Fatalf("after prune: %+v", res)
	}
}

func TestExcludedSourcePathIsSkipped(t *testing.T) {
	repo, _ := tu.NewRepo(t)
	src := t.TempDir()
	tu.WriteFile(t, filepath.Join(src, "app", "data.txt"), []byte("keep"))
	tu.WriteFile(t, filepath.Join(src, "logs", "access.log"), []byte("growing"))
	logs := filepath.Join(src, "logs")
	// A source that matches an exclude pattern (e.g. a bind-mounted log
	// directory of a Docker project) is left out entirely.
	snap := backup(t, repo, archiver.Options{Paths: []string{filepath.Join(src, "app"), logs}, Excludes: []string{logs}})
	var got []string
	repo.Walk(context.Background(), snap.Tree, func(p string, n *repository.Node) error {
		got = append(got, filepath.Base(p))
		return nil
	})
	joined := strings.Join(got, ",")
	if strings.Contains(joined, "access.log") || !strings.Contains(joined, "data.txt") {
		t.Fatal(joined)
	}
}
