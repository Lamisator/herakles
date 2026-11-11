package repository

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/rand"
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
	if err := r.Flush(ctx); err != nil {
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

func TestPruneAndCheck(t *testing.T) {
	ctx := context.Background()
	r, be := newTestRepo(t)
	mk := func(seeds ...int64) id.ID {
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
		if err := r.Flush(ctx); err != nil {
			t.Fatal(err)
		}
		return tid
	}
	t1 := mk(1, 2, 3, 4, 5, 6)
	t2 := mk(5, 6, 7, 8)
	s1 := &Snapshot{Time: time.Now(), Tree: t1, Type: TypeFull}
	s2 := &Snapshot{Time: time.Now(), Tree: t2, Type: TypeFull}
	r.SaveSnapshot(ctx, s1)
	r.SaveSnapshot(ctx, s2)
	res, err := r.Check(ctx, CheckOptions{ReadData: 1})
	if err != nil || !res.OK() {
		t.Fatalf("check: %v %v", err, res.Errors)
	}
	before, _ := r.Stats(ctx)
	if err := r.RemoveSnapshot(ctx, s1.ID()); err != nil {
		t.Fatal(err)
	}
	dry, err := r.Prune(ctx, PruneOptions{DryRun: true})
	if err != nil || dry.BytesFreed == 0 {
		t.Fatalf("dry run: %v %+v", err, dry)
	}
	st, err := r.Prune(ctx, PruneOptions{})
	if err != nil {
		t.Fatal(err)
	}
	after, _ := r.Stats(ctx)
	if after.StoredBytes >= before.StoredBytes || after.DataBlobs != 4 {
		t.Fatalf("prune did not free space: %+v -> %+v (%+v)", before, after, st)
	}
	res, err = r.Check(ctx, CheckOptions{ReadData: 1})
	if err != nil || !res.OK() || len(res.Warnings) != 0 {
		t.Fatalf("check after prune: %v %v %v", err, res.Errors, res.Warnings)
	}
	// Everything still readable from a fresh handle.
	r2, err := Open(ctx, be, pw, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if err := r2.Walk(ctx, t2, func(p string, n *Node) error {
		_, err := r2.LoadBlob(ctx, n.Content[0])
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// Corruption is detected.
	pk := r2.Index().Packs()[0]
	be.Corrupt(backend.Handle{Type: backend.PackFile, Name: pk.ID.String()}, 5)
	res, err = r2.Check(ctx, CheckOptions{ReadData: 1})
	if err != nil || res.OK() {
		t.Fatalf("corruption not detected: %v", err)
	}
}

func TestRebuildIndex(t *testing.T) {
	ctx := context.Background()
	r, be := newTestRepo(t)
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
	r.Flush(ctx)
	r2, err := Open(ctx, be, pw, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	got, err := r2.LoadBlob(ctx, bid)
	if err != nil || string(got) != "local data" {
		t.Fatal(err)
	}
	hdr, _, err := r2.LoadPackHeader(ctx, r2.Index().Packs()[0].ID)
	if err != nil || len(hdr) != 1 {
		t.Fatalf("pack header: %v", err)
	}
}
