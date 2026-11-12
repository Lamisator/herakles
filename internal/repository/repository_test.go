package repository

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lamisator/herakles/internal/backend"
	"github.com/lamisator/herakles/internal/chunker"
	"github.com/lamisator/herakles/internal/crypto"
	"github.com/lamisator/herakles/internal/id"
)

func init() { crypto.DefaultKDFParams = crypto.KDFParams{Time: 1, Memory: 1024, Threads: 1} }

var pw = crypto.Credentials{Password: "pw"}

func newTestRepo(t *testing.T) (*Repository, *backend.Mem) {
	t.Helper()
	be := backend.NewMem()
	r, err := Init(context.Background(), be, pw, chunker.DefaultParams)
	if err != nil {
		t.Fatal(err)
	}
	r.applyOptions(Options{Compression: DefaultOptions().Compression, PackSize: 64 << 10, Connections: 3})
	return r, be
}

func randBytes(seed int64, n int) []byte {
	b := make([]byte, n)
	rand.New(rand.NewSource(seed)).Read(b)
	return b
}

func TestInitOpen(t *testing.T) {
	ctx := context.Background()
	r, be := newTestRepo(t)
	if _, err := Init(ctx, be, pw, chunker.DefaultParams); err == nil {
		t.Fatal("double init succeeded")
	}
	if _, err := Open(ctx, be, crypto.Credentials{Password: "wrong"}, DefaultOptions()); !errors.Is(err, crypto.ErrWrongCredentials) {
		t.Fatalf("wrong password: %v", err)
	}
	r2, err := Open(ctx, be, pw, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if r2.Config().ID != r.Config().ID || r2.Key().ChunkerSeed != r.Key().ChunkerSeed {
		t.Fatal("reopened repo differs")
	}
	if _, err := Open(ctx, backend.NewMem(), pw, DefaultOptions()); !errors.Is(err, ErrNotARepository) {
		t.Fatalf("empty backend: %v", err)
	}
}

func TestKeyManagement(t *testing.T) {
	ctx := context.Background()
	r, be := newTestRepo(t)
	kf := crypto.Credentials{KeyFileData: randBytes(1, 64)}
	both := crypto.Credentials{Password: "two", KeyFileData: randBytes(2, 64)}
	n1, err := r.AddKey(ctx, kf, "keyfile")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.AddKey(ctx, both, "both"); err != nil {
		t.Fatal(err)
	}
	for _, c := range []crypto.Credentials{pw, kf, both} {
		if _, err := Open(ctx, be, c, DefaultOptions()); err != nil {
			t.Fatalf("open with %v: %v", c.HasKeyFile(), err)
		}
	}
	if _, err := Open(ctx, be, crypto.Credentials{Password: "two"}, DefaultOptions()); err == nil {
		t.Fatal("password alone opened a password+keyfile key")
	}
	keys, _ := r.ListKeys(ctx)
	if len(keys) != 3 {
		t.Fatalf("expected 3 keys, got %d", len(keys))
	}
	if err := r.RemoveKey(ctx, r.KeyName()); err == nil {
		t.Fatal("removed current key")
	}
	if err := r.RemoveKey(ctx, n1[:8]); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(ctx, be, kf, DefaultOptions()); err == nil {
		t.Fatal("removed key still works")
	}
	if err := r.ChangeKey(ctx, crypto.Credentials{Password: "new"}); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(ctx, be, pw, DefaultOptions()); err == nil {
		t.Fatal("old password still works after change")
	}
	if _, err := Open(ctx, be, crypto.Credentials{Password: "new"}, DefaultOptions()); err != nil {
		t.Fatal(err)
	}
}

func TestBlobsDedupAndReload(t *testing.T) {
	ctx := context.Background()
	r, be := newTestRepo(t)
	var ids []id.ID
	for i := 0; i < 50; i++ {
		data := randBytes(int64(i%25), 10000) // 25 distinct blobs
		bid, isNew, _, err := r.SaveBlob(ctx, DataBlob, data)
		if err != nil {
			t.Fatal(err)
		}
		if isNew != (i < 25) {
			t.Fatalf("blob %d: isNew=%v", i, isNew)
		}
		ids = append(ids, bid)
	}
	if _, err := r.SaveSnapshot(ctx, &Snapshot{Time: time.Now(), Type: TypeFull}); err != nil {
		t.Fatal(err)
	}
	r2, err := Open(ctx, be, pw, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := r2.Index().Count(); n != 25 {
		t.Fatalf("index has %d blobs, want 25", n)
	}
	for i, bid := range ids {
		got, err := r2.LoadBlob(ctx, bid)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, randBytes(int64(i%25), 10000)) {
			t.Fatal("blob content mismatch")
		}
	}
}

func TestSnapshotsAndFind(t *testing.T) {
	ctx := context.Background()
	r, _ := newTestRepo(t)
	tid, err := r.SaveTree(ctx, &Tree{Nodes: []*Node{{Name: "b", Type: NodeFile}, {Name: "a", Type: NodeFile}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	base := time.Now()
	for i := 0; i < 3; i++ {
		s := &Snapshot{Time: base.Add(time.Duration(i) * time.Hour), Tree: tid, Paths: []string{"/x"}, Hostname: "h", Job: fmt.Sprintf("job%d", i%2), Type: TypeFull}
		if _, err := r.SaveSnapshot(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	all, err := r.Snapshots(ctx)
	if err != nil || len(all) != 3 {
		t.Fatalf("snapshots: %v %d", err, len(all))
	}
	latest, err := r.FindSnapshot(ctx, "latest", SnapshotFilter{Job: "job0"})
	if err != nil || !latest.Time.Equal(all[2].Time) {
		t.Fatalf("latest job0: %v", err)
	}
	byPrefix, err := r.FindSnapshot(ctx, all[1].ID().String()[:8], SnapshotFilter{})
	if err != nil || byPrefix.ID() != all[1].ID() {
		t.Fatalf("prefix lookup: %v", err)
	}
	tree, err := r.LoadTree(ctx, tid)
	if err != nil || tree.Nodes[0].Name != "a" || tree.Find("b") == nil {
		t.Fatalf("tree: %v", err)
	}
}

func TestLocks(t *testing.T) {
	ctx := context.Background()
	r, _ := newTestRepo(t)
	l1, err := r.Lock(ctx, false, "backup")
	if err != nil {
		t.Fatal(err)
	}
	l2, err := r.Lock(ctx, false, "backup")
	if err != nil {
		t.Fatal("two shared locks must coexist:", err)
	}
	if _, err := r.Lock(ctx, true, "prune"); !errors.Is(err, ErrLocked) {
		t.Fatalf("exclusive lock while shared held: %v", err)
	}
	l1.Unlock()
	l2.Unlock()
	ex, err := r.Lock(ctx, true, "prune")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Lock(ctx, false, "backup"); !errors.Is(err, ErrLocked) {
		t.Fatal("shared lock while exclusive held")
	}
	ex.Unlock()
	if locks, _ := r.ListLocks(ctx); len(locks) != 0 {
		t.Fatalf("%d locks left", len(locks))
	}
	// Stale lock from a dead process on this host is ignored.
	stale := LockInfo{Time: time.Now().Add(-2 * LockStaleAfter), Exclusive: true, Hostname: "elsewhere", PID: 1}
	r.saveUnpackedAs(ctx, backend.LockFile, "stale", stale)
	l, err := r.Lock(ctx, false, "backup")
	if err != nil {
		t.Fatal("stale lock blocked:", err)
	}
	l.Unlock()
	if n, _ := r.RemoveLocks(ctx, false); n != 1 {
		t.Fatalf("removed %d stale locks", n)
	}
}

// backupOf writes one backup with a file per seed and returns its snapshot.
func backupOf(t *testing.T, r *Repository, typ string, parent *Snapshot, seeds ...int64) *Snapshot {
	t.Helper()
	ctx := context.Background()
	got, err := r.BeginBackup(ctx, BackupStart{Job: "job", Type: typ, Parent: parent})
	if err != nil {
		t.Fatal(err)
	}
	var nodes []*Node
	for _, s := range seeds {
		bid, _, _, err := r.SaveBlob(ctx, DataBlob, randBytes(s, 30000))
		if err != nil {
			t.Fatal(err)
		}
		nodes = append(nodes, &Node{Name: fmt.Sprint(s), Type: NodeFile, Content: []id.ID{bid}, Size: 30000})
	}
	tid, err := r.SaveTree(ctx, &Tree{Nodes: nodes})
	if err != nil {
		t.Fatal(err)
	}
	snap := &Snapshot{Time: time.Now(), Tree: tid, Type: typ, Job: "job"}
	if got != nil {
		p := got.ID()
		snap.Parent = &p
	} else {
		snap.Type = TypeFull
	}
	if _, err := r.SaveSnapshot(ctx, snap); err != nil {
		t.Fatal(err)
	}
	return snap
}

func files(t *testing.T, be backend.Backend, ft backend.FileType) []string {
	var out []string
	be.List(context.Background(), ft, func(fi backend.FileInfo) error { out = append(out, fi.Name); return nil })
	return out
}

func checkOK(t *testing.T, r *Repository) {
	t.Helper()
	res, err := r.Check(context.Background(), CheckOptions{ReadData: 1})
	if err != nil || !res.OK() || len(res.Warnings) != 0 {
		t.Fatalf("check: %v %v %v", err, res.Errors, res.Warnings)
	}
}

func TestOneFilePerBackup(t *testing.T) {
	ctx := context.Background()
	r, be := newTestRepo(t)
	s1 := backupOf(t, r, TypeFull, nil, 1, 2, 3, 4, 5, 6)
	s2 := backupOf(t, r, TypeIncremental, s1, 5, 6, 7, 8)
	s3 := backupOf(t, r, TypeFull, s2, 5, 6, 7, 8)
	if n := files(t, be, backend.BundleFile); len(n) != 3 {
		t.Fatalf("backup files: %v", n)
	}
	for _, ft := range []backend.FileType{backend.PackFile, backend.IndexFile, backend.SnapFile} {
		if n := files(t, be, ft); len(n) != 0 {
			t.Fatalf("%s: %v", ft, n)
		}
	}
	// The incremental stored only 7 and 8 (5 and 6 are in its parent);
	// the full backup stored everything again: it stands alone.
	count := func(s *Snapshot) int {
		all, _ := r.Bundles(ctx)
		n := 0
		for _, p := range all[s.File()].Packs {
			n += len(p.Blobs)
		}
		return n
	}
	if count(s1) != 7 || count(s2) != 3 || count(s3) != 5 {
		t.Fatalf("blobs per file: %d %d %d", count(s1), count(s2), count(s3))
	}
	if !strings.Contains(s1.File(), "_job_full_") || !strings.Contains(s2.File(), "_job_incremental_") {
		t.Fatalf("file names: %s %s", s1.File(), s2.File())
	}
	checkOK(t, r)

	// A fresh handle sees the same snapshots, from the files alone.
	r2, err := Open(ctx, be, pw, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	snaps, err := r2.Snapshots(ctx)
	if err != nil || len(snaps) != 3 || snaps[1].ID() != s2.ID() || snaps[1].File() != s2.File() {
		t.Fatalf("snapshots: %v %v", err, snaps)
	}
	if err := r2.Walk(ctx, s2.Tree, func(p string, n *Node) error {
		_, err := r2.LoadBlob(ctx, n.Content[0])
		return err
	}); err != nil {
		t.Fatal(err)
	}

	// A copy of just the files of one chain restores that chain.
	part := backend.NewMem()
	for _, n := range []string{"config"} {
		b, _ := backend.LoadAll(ctx, be, backend.Handle{Type: backend.ConfigFile, Name: n})
		part.Save(ctx, backend.Handle{Type: backend.ConfigFile}, b)
	}
	for _, ft := range []backend.FileType{backend.KeyFile} {
		for _, n := range files(t, be, ft) {
			b, _ := backend.LoadAll(ctx, be, backend.Handle{Type: ft, Name: n})
			part.Save(ctx, backend.Handle{Type: ft, Name: n}, b)
		}
	}
	for _, n := range []string{s1.File(), s2.File()} {
		b, _ := backend.LoadAll(ctx, be, backend.Handle{Type: backend.BundleFile, Name: n})
		part.Save(ctx, backend.Handle{Type: backend.BundleFile, Name: n}, b)
	}
	r3, err := Open(ctx, part, pw, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	checkOK(t, r3)
	if snaps, _ := r3.Snapshots(ctx); len(snaps) != 2 {
		t.Fatalf("partial copy: %d snapshots", len(snaps))
	}
}

func TestPruneBundles(t *testing.T) {
	ctx := context.Background()
	r, be := newTestRepo(t)
	s1 := backupOf(t, r, TypeFull, nil, 1, 2, 3, 4, 5, 6)
	s2 := backupOf(t, r, TypeIncremental, s1, 5, 6, 7, 8)
	s3 := backupOf(t, r, TypeIncremental, s2, 5, 6, 7, 8, 9)
	checkOK(t, r)
	before, _ := r.Stats(ctx)

	// Forgetting the full backup keeps its file: the incrementals need it.
	// Most of its data is unused, so prune merges what is still needed
	// into a file that replaces it.
	if err := r.RemoveSnapshot(ctx, s1.ID()); err != nil {
		t.Fatal(err)
	}
	if snaps, _ := r.Snapshots(ctx); len(snaps) != 2 {
		t.Fatalf("forgotten snapshot still listed: %d", len(snaps))
	}
	dry, err := r.Prune(ctx, PruneOptions{DryRun: true})
	if err != nil || dry.BytesFreed == 0 || dry.FilesMerged != 1 {
		t.Fatalf("dry run: %v %+v", err, dry)
	}
	if n := files(t, be, backend.BundleFile); len(n) != 3 {
		t.Fatal("dry run changed files")
	}
	st, err := r.Prune(ctx, PruneOptions{})
	if err != nil {
		t.Fatal(err)
	}
	after, _ := r.Stats(ctx)
	if after.StoredBytes >= before.StoredBytes || after.DataBlobs != 5 || st.FilesMerged != 1 {
		t.Fatalf("prune: %+v -> %+v (%+v)", before, after, st)
	}
	names := files(t, be, backend.BundleFile)
	if len(names) != 3 || strings.Contains(strings.Join(names, " "), s1.File()) {
		t.Fatalf("files after merge: %v", names)
	}
	if n := files(t, be, backend.ForgetFile); len(n) != 0 {
		t.Fatalf("marker of the deleted file left: %v", n)
	}
	checkOK(t, r)
	r2, _ := Open(ctx, be, pw, DefaultOptions())
	checkOK(t, r2)
	// A new incremental still builds on the chain through the merged file.
	s4 := backupOf(t, r2, TypeIncremental, s3, 9, 10)
	if chain, err := r2.Chain(ctx, s4); err != nil || len(chain) != 4 {
		t.Fatalf("chain through merged file: %v %d", err, len(chain))
	}
	checkOK(t, r2)

	// A new full backup; forgetting the old chain deletes all its files.
	s5 := backupOf(t, r2, TypeFull, s4, 9, 10)
	for _, s := range []*Snapshot{s2, s3, s4} {
		if err := r2.RemoveSnapshot(ctx, s.ID()); err != nil {
			t.Fatal(err)
		}
	}
	st, err = r2.Prune(ctx, PruneOptions{})
	if err != nil || st.FilesDeleted != 4 {
		t.Fatalf("prune old chain: %v %+v", err, st)
	}
	if n := files(t, be, backend.BundleFile); len(n) != 1 || n[0] != s5.File() {
		t.Fatalf("left: %v", n)
	}
	checkOK(t, r2)

	// Corruption is detected.
	all, _ := r2.Bundles(ctx)
	p := all[s5.File()].Packs[0]
	be.Corrupt(backend.Handle{Type: backend.BundleFile, Name: s5.File()}, int(p.Offset)+5)
	res, err := r2.Check(ctx, CheckOptions{ReadData: 1})
	if err != nil || res.OK() {
		t.Fatalf("corruption not detected: %v", err)
	}
}

func TestAbortedBackupLeavesNothing(t *testing.T) {
	ctx := context.Background()
	r, be := newTestRepo(t)
	if _, err := r.BeginBackup(ctx, BackupStart{Type: TypeFull}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		r.SaveBlob(ctx, DataBlob, randBytes(int64(i), 30000))
	}
	r.Flush(ctx)
	r.AbortBackup()
	if n := files(t, be, backend.BundleFile); len(n) != 0 {
		t.Fatalf("files: %v", n)
	}
	if r.HasBlob(r.BlobID(randBytes(1, 30000))) {
		t.Fatal("aborted data still counts as stored")
	}
	// The next backup works normally.
	s := backupOf(t, r, TypeFull, nil, 1, 2)
	if s.File() == "" {
		t.Fatal("no file")
	}
	checkOK(t, r)
}

// TestOlderFormat: data written by earlier versions (data/, index/,
// snapshots/) stays readable and prunable next to backup files; the first
// backup on top of it is a full backup.
func TestOlderFormat(t *testing.T) {
	ctx := context.Background()
	r, be := newTestRepo(t)
	r.legacy = true
	var nodes []*Node
	for _, s := range []int64{1, 2, 3} {
		bid, _, _, _ := r.SaveBlob(ctx, DataBlob, randBytes(s, 30000))
		nodes = append(nodes, &Node{Name: fmt.Sprint(s), Type: NodeFile, Content: []id.ID{bid}, Size: 30000})
	}
	tid, _ := r.SaveTree(ctx, &Tree{Nodes: nodes})
	if err := r.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	old := &Snapshot{Time: time.Now().Add(-time.Hour), Tree: tid, Type: TypeFull, Job: "job"}
	oid, err := r.SaveUnpacked(ctx, backend.SnapFile, old)
	if err != nil {
		t.Fatal(err)
	}
	r.legacy = false

	r2, _ := Open(ctx, be, pw, DefaultOptions())
	snaps, _ := r2.Snapshots(ctx)
	if len(snaps) != 1 || snaps[0].ID() != oid || snaps[0].File() != "" {
		t.Fatalf("old snapshot: %+v", snaps)
	}
	parent, err := r2.BeginBackup(ctx, BackupStart{Type: TypeIncremental, Parent: snaps[0]})
	r2.AbortBackup()
	if err != nil || parent != nil {
		t.Fatal("an incremental on an old-format snapshot must become a full backup")
	}
	s := backupOf(t, r2, TypeIncremental, snaps[0], 2, 3, 4)
	if s.Type != TypeFull {
		t.Fatal(s.Type)
	}
	checkOK(t, r2)
	if snaps, _ := r2.Snapshots(ctx); len(snaps) != 2 {
		t.Fatal(len(snaps))
	}
	// Forgetting the old snapshot removes its file; prune its data.
	if err := r2.RemoveSnapshot(ctx, oid); err != nil {
		t.Fatal(err)
	}
	if n := files(t, be, backend.SnapFile); len(n) != 0 {
		t.Fatal(n)
	}
	if _, err := r2.Prune(ctx, PruneOptions{}); err != nil {
		t.Fatal(err)
	}
	if n := files(t, be, backend.PackFile); len(n) != 0 {
		t.Fatalf("old packs left: %v", n)
	}
	checkOK(t, r2)
	if err := r2.Walk(ctx, s.Tree, func(p string, n *Node) error {
		_, err := r2.LoadBlob(ctx, n.Content[0])
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRebuildIndex(t *testing.T) {
	ctx := context.Background()
	r, be := newTestRepo(t)
	r.legacy = true // index files exist only in the older format
	var ids []id.ID
	for i := 0; i < 20; i++ {
		bid, _, _, _ := r.SaveBlob(ctx, DataBlob, randBytes(int64(i), 20000))
		ids = append(ids, bid)
	}
	r.Flush(ctx)
	names, _ := r.ListNames(ctx, backend.IndexFile)
	for _, n := range names {
		be.Remove(ctx, backend.Handle{Type: backend.IndexFile, Name: n})
	}
	r.LoadIndex(ctx)
	if n, _ := r.Index().Count(); n != 0 {
		t.Fatal("index not empty")
	}
	if _, err := r.RebuildIndex(ctx, nil); err != nil {
		t.Fatal(err)
	}
	for _, bid := range ids {
		if _, err := r.LoadBlob(ctx, bid); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLocalBackendRepo(t *testing.T) {
	ctx := context.Background()
	be := backend.NewLocal(t.TempDir())
	r, err := Init(ctx, be, pw, chunker.DefaultParams)
	if err != nil {
		t.Fatal(err)
	}
	bid, _, _, err := r.SaveBlob(ctx, DataBlob, []byte("local data"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.SaveSnapshot(ctx, &Snapshot{Time: time.Now(), Type: TypeFull}); err != nil {
		t.Fatal(err)
	}
	r2, err := Open(ctx, be, pw, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	got, err := r2.LoadBlob(ctx, bid)
	if err != nil || string(got) != "local data" {
		t.Fatal(err)
	}
	if n := files(t, be, backend.BundleFile); len(n) != 1 || !strings.HasSuffix(n[0], ".hbk") {
		t.Fatalf("backup files: %v", n)
	}
}

func TestSplitPath(t *testing.T) {
	cases := map[string]string{
		"/home/alice/":    "home|alice",
		`C:\Users\bob`:    "C|Users|bob",
		"C:/Users":        "C|Users",
		`\\nas\share\dir`: "nas|share|dir",
		"/":               "",
		"./x/./y":         "x|y",
		"/dev/sda1":       "dev|sda1",
		"/data/C:":        "data|C:",
	}
	for in, want := range cases {
		if got := strings.Join(splitPath(in), "|"); got != want {
			t.Errorf("splitPath(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestLooseFiles opens backup files copied out of the repository (a folder
// of .hbk files, or one file) with nothing but the password.
func TestLooseFiles(t *testing.T) {
	ctx := context.Background()
	src := t.TempDir()
	be := backend.NewLocal(src)
	r, err := Init(ctx, be, pw, chunker.DefaultParams)
	if err != nil {
		t.Fatal(err)
	}
	s1 := backupOf(t, r, TypeFull, nil, 1, 2, 3)
	s2 := backupOf(t, r, TypeIncremental, s1, 3, 4)
	backupOf(t, r, TypeFull, s2, 5)
	usb := t.TempDir()
	for _, n := range []string{s1.File(), s2.File()} {
		b, err := os.ReadFile(filepath.Join(src, "backups", n))
		if err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(usb, n), b, 0o644)
	}
	for _, loc := range []string{usb, filepath.Join(usb, s1.File())} {
		lb, err := backend.Open(loc, backend.Options{})
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := lb.(*backend.Loose); !ok {
			t.Fatalf("%s opened as %T", loc, lb)
		}
		r2, err := Open(ctx, lb, pw, DefaultOptions())
		if err != nil {
			t.Fatal(err)
		}
		snaps, err := r2.Snapshots(ctx)
		want := 2
		if loc != usb {
			want = 1
		}
		if err != nil || len(snaps) != want {
			t.Fatalf("%s: %v %d snapshots", loc, err, len(snaps))
		}
		checkOK(t, r2)
		if _, err := r2.Lock(ctx, false, "restore"); err != nil {
			t.Fatal("lock:", err)
		}
		if err := r2.RemoveSnapshot(ctx, snaps[0].ID()); err == nil {
			t.Fatal("loose files must be read-only")
		}
	}
	if _, err := Open(ctx, mustOpen(t, usb), crypto.Credentials{Password: "wrong"}, DefaultOptions()); err == nil {
		t.Fatal("wrong password accepted")
	}
}

func mustOpen(t *testing.T, loc string) backend.Backend {
	b, err := backend.Open(loc, backend.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return b
}
