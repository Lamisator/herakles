package repository

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lamisator/herakles/internal/backend"
	"github.com/lamisator/herakles/internal/compress"
	"github.com/lamisator/herakles/internal/id"
)

// Every backup is stored as one file in backups/ (a "bundle"):
//
//	"HERAKLES-BACKUP1"                16 bytes
//	header length, header              4 bytes + JSON: repository config and
//	                                   key envelopes, so the file opens alone
//	pack, pack, ...                    blobs + encrypted pack header, as in data/
//	encrypted trailer                  JSON: packs with offsets, parent, snapshot
//	trailer length                     8 bytes little endian
//	"HKBEND01"                         8 bytes
//
// A full backup deduplicates only against itself, an incremental or
// differential backup only against the backups of its chain (its parent,
// the parent's parent, ... up to the full backup). So a full backup file is
// enough to restore that backup, and a chain of files is enough for every
// backup in it: the files can be copied anywhere together.
//
// Prune deletes files no live backup needs. When the oldest backups of a
// chain were forgotten while newer ones still need their data, it merges
// them into one file without a snapshot that "replaces" them.

const (
	bundleMagic    = backend.BundleMagic
	bundleEndMagic = "HKBEND01"
	bundleExt      = ".hbk"
)

// bundleTrailer is the encrypted table of contents of a bundle.
type bundleTrailer struct {
	Version  int        `json:"version"`
	Created  time.Time  `json:"created"`
	Parent   string     `json:"parent,omitempty"`
	Replaces []string   `json:"replaces,omitempty"`
	Packs    []PackInfo `json:"packs"`
	// Snapshot is encrypted like a snapshot file; its hash is the
	// snapshot ID.
	Snapshot []byte `json:"snapshot,omitempty"`
}

// Bundle describes a stored backup file.
type Bundle struct {
	Name     string
	Size     int64
	Created  time.Time
	Parent   string
	Replaces []string
	Packs    []PackInfo
	Snapshot *Snapshot // nil for files written by prune
}

// bundleWriter streams the backup file of a running backup.
type bundleWriter struct {
	mu       sync.Mutex // serializes writes; not r.bmu, which dedup checks need
	name     string
	parent   string
	replaces []string
	pw       *io.PipeWriter
	done     chan error
	off      int64
	packs    []PackInfo
	err      error
}

var errAborted = errors.New("backup aborted")

// BackupStart describes a backup about to be written.
type BackupStart struct {
	Job, Hostname string
	Type          string
	// Parent is the snapshot the backup builds on (incremental,
	// differential). Without one, or when the parent is not stored in a
	// backup file, the backup is a full backup.
	Parent *Snapshot
}

// BeginBackup starts the file of a new backup. It returns the parent the
// backup may build on: nil means it must be a full backup. Every blob saved
// until SaveSnapshot goes into this file, deduplicated only against the
// parent's chain. Call AbortBackup when the backup fails.
func (r *Repository) BeginBackup(ctx context.Context, b BackupStart) (*Snapshot, error) {
	r.bmu.Lock()
	defer r.bmu.Unlock()
	if r.bw != nil {
		return nil, errors.New("another backup is being written to this repository handle")
	}
	parent := b.Parent
	scope := id.Set{}
	parentFile := ""
	if parent != nil && b.Type != TypeFull {
		chain, err := r.chainLocked(ctx, parent.bundle)
		if parent.bundle == "" || err != nil {
			parent = nil
		} else {
			parentFile = chain[0].Name
			for _, c := range chain {
				for _, p := range c.Packs {
					for _, e := range p.Blobs {
						scope.Insert(e.ID)
					}
				}
			}
		}
	} else {
		parent = nil
	}
	typ := b.Type
	if parent == nil {
		typ = TypeFull
	}
	label := b.Job
	if label == "" {
		label = b.Hostname
	}
	return parent, r.openBundleLocked(ctx, bundleName(time.Now(), label, typ), parentFile, nil, scope)
}

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9.-]+`)

// bundleName is readable: when, what and which kind of backup.
func bundleName(t time.Time, label, typ string) string {
	label = strings.Trim(unsafeName.ReplaceAllString(label, "-"), "-.")
	if len(label) > 40 {
		label = label[:40]
	}
	if label == "" {
		label = "backup"
	}
	rnd := id.Random()
	return fmt.Sprintf("%s_%s_%s_%s%s", t.UTC().Format("20060102-150405"), label, typ, hex.EncodeToString(rnd[:6]), bundleExt)
}

// bundleHeader returns the plaintext start of a backup file.
func (r *Repository) bundleHeader(ctx context.Context) ([]byte, error) {
	if r.header != nil {
		return r.header, nil
	}
	cfg, err := backend.LoadAll(ctx, r.be, backend.Handle{Type: backend.ConfigFile})
	if err != nil {
		return nil, err
	}
	h := &backend.BundleHeader{Config: cfg, Keys: map[string]json.RawMessage{}}
	err = r.be.List(ctx, backend.KeyFile, func(fi backend.FileInfo) error {
		raw, err := backend.LoadAll(ctx, r.be, backend.Handle{Type: backend.KeyFile, Name: fi.Name})
		if err == nil {
			h.Keys[fi.Name] = raw
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	r.header, err = backend.EncodeBundleHeader(h)
	return r.header, err
}

func (r *Repository) openBundleLocked(ctx context.Context, name, parent string, replaces []string, scope id.Set) error {
	head, err := r.bundleHeader(ctx)
	if err != nil {
		return fmt.Errorf("start backup file: %w", err)
	}
	pr, pw := io.Pipe()
	w := &bundleWriter{name: name, parent: parent, replaces: replaces, pw: pw, done: make(chan error, 1)}
	go func() {
		// SaveStream succeeds only after reading everything up to Close.
		err := r.be.SaveStream(ctx, backend.Handle{Type: backend.BundleFile, Name: name}, pr)
		pr.CloseWithError(errOr(err, io.ErrClosedPipe))
		w.done <- err
	}()
	if _, err := pw.Write(head); err != nil {
		pw.CloseWithError(errAborted)
		return fmt.Errorf("start backup file: %w", <-w.done)
	}
	w.off = int64(len(head))
	r.bw, r.scope = w, scope
	return nil
}

func errOr(err, def error) error {
	if err != nil {
		return err
	}
	return def
}

// writePack appends a finished pack to the open backup file.
func (r *Repository) writePack(data []byte, info PackInfo) error {
	r.bmu.Lock()
	w := r.bw
	r.bmu.Unlock()
	if w == nil {
		return errors.New("no backup file is open")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err != nil {
		return w.err
	}
	if _, err := w.pw.Write(data); err != nil {
		w.err = fmt.Errorf("write backup file: %w", err)
		return w.err
	}
	info.Offset = w.off
	info.File = w.name
	w.off += int64(len(data))
	w.packs = append(w.packs, info)
	return nil
}

// AbortBackup discards the backup file being written (nothing when none
// is open). Its data is not stored.
func (r *Repository) AbortBackup() {
	r.bmu.Lock()
	w := r.bw
	r.bw, r.scope = nil, nil
	r.bmu.Unlock()
	if w == nil {
		return
	}
	w.pw.CloseWithError(errAborted)
	<-w.done
	r.pmu.Lock()
	r.pending = id.Set{}
	r.pmu.Unlock()
}

// commitBundle finishes the open backup file with an optional encrypted
// snapshot and makes its data part of the index.
func (r *Repository) commitBundle(ctx context.Context, snapCT []byte) (*Bundle, error) {
	if err := r.Flush(ctx); err != nil {
		r.AbortBackup()
		return nil, err
	}
	r.bmu.Lock()
	w := r.bw
	r.bw, r.scope = nil, nil
	r.bmu.Unlock()
	if w == nil {
		return nil, errors.New("no backup file is open")
	}
	w.mu.Lock() // wait for a write still in progress
	defer w.mu.Unlock()
	tr := bundleTrailer{Version: 1, Created: time.Now().UTC(), Parent: w.parent, Replaces: w.replaces, Packs: w.packs, Snapshot: snapCT}
	raw, err := json.Marshal(tr)
	if err == nil && w.err != nil {
		err = w.err
	}
	if err != nil {
		w.pw.CloseWithError(errAborted)
		<-w.done
		return nil, err
	}
	enc := r.key.Seal(compress.Compress(compress.Mode{Algorithm: compress.Zstd, Level: 3}, raw))
	foot := make([]byte, 8, 16)
	binary.LittleEndian.PutUint64(foot, uint64(len(enc)))
	foot = append(foot, bundleEndMagic...)
	if _, err := w.pw.Write(enc); err == nil {
		_, err = w.pw.Write(foot)
	}
	w.pw.Close()
	if err := <-w.done; err != nil {
		r.pmu.Lock()
		r.pending = id.Set{}
		r.pmu.Unlock()
		return nil, fmt.Errorf("store backup file: %w", err)
	}
	b, err := r.bundleFromTrailer(w.name, w.off+int64(len(enc))+16, &tr)
	if err != nil {
		return nil, err
	}
	r.bmu.Lock()
	if r.bundles == nil {
		r.bundles = map[string]*Bundle{}
	}
	r.bundles[w.name] = b
	r.bmu.Unlock()
	r.pmu.Lock()
	for _, p := range b.Packs {
		r.idx.AddPack(p)
		for _, e := range p.Blobs {
			r.pending.Delete(e.ID)
		}
	}
	r.pmu.Unlock()
	return b, nil
}

func (r *Repository) bundleFromTrailer(name string, size int64, tr *bundleTrailer) (*Bundle, error) {
	b := &Bundle{Name: name, Size: size, Created: tr.Created, Parent: tr.Parent, Replaces: tr.Replaces, Packs: tr.Packs}
	for i := range b.Packs {
		b.Packs[i].File = name
	}
	if len(tr.Snapshot) > 0 {
		s := &Snapshot{}
		if err := r.decodeUnpacked(tr.Snapshot, s); err != nil {
			return nil, fmt.Errorf("%s: snapshot: %w", name, err)
		}
		s.id = id.Hash(tr.Snapshot)
		s.bundle = name
		b.Snapshot = s
	}
	return b, nil
}

// loadBundle reads the trailer of a backup file.
func (r *Repository) loadBundle(ctx context.Context, name string, size int64) (*Bundle, error) {
	h := backend.Handle{Type: backend.BundleFile, Name: name}
	if size < int64(len(bundleMagic))+20 {
		return nil, fmt.Errorf("%s: too short for a backup file", name)
	}
	tail := min(int64(1<<20), size)
	buf, err := backend.LoadRange(ctx, r.be, h, size-tail, tail)
	if err != nil {
		return nil, err
	}
	if string(buf[len(buf)-8:]) != bundleEndMagic {
		return nil, fmt.Errorf("%s: incomplete or not a backup file", name)
	}
	tl := int64(binary.LittleEndian.Uint64(buf[len(buf)-16:]))
	if tl <= 0 || tl+16+int64(len(bundleMagic)) > size {
		return nil, fmt.Errorf("%s: invalid table of contents length", name)
	}
	if tl+16 > tail {
		if buf, err = backend.LoadRange(ctx, r.be, h, size-tl-16, tl+16); err != nil {
			return nil, err
		}
	}
	enc := buf[len(buf)-16-int(tl) : len(buf)-16]
	var tr bundleTrailer
	if err := r.decodeUnpacked(enc, &tr); err != nil {
		return nil, fmt.Errorf("%s: table of contents: %w", name, err)
	}
	end := size - tl - 16
	for _, p := range tr.Packs {
		if p.Offset < int64(len(bundleMagic)) || p.Offset+p.Size > end {
			return nil, fmt.Errorf("%s: pack %s lies outside the file", name, p.ID.Short())
		}
		for _, e := range p.Blobs {
			if int64(e.Offset)+int64(e.Length) > p.Size {
				return nil, fmt.Errorf("%s: blob %s lies outside its pack", name, e.ID.Short())
			}
		}
	}
	return r.bundleFromTrailer(name, size, &tr)
}

// decodeUnpacked decrypts, decompresses and parses an object.
func (r *Repository) decodeUnpacked(ct []byte, obj any) error {
	pt, err := r.key.Open(ct)
	if err != nil {
		return err
	}
	raw, err := compress.Decompress(pt)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, obj)
}

// Bundles returns the backup files, reading the tables of contents of
// files not seen before (they never change).
func (r *Repository) Bundles(ctx context.Context) (map[string]*Bundle, error) {
	r.bmu.Lock()
	defer r.bmu.Unlock()
	return r.bundlesLocked(ctx)
}

func (r *Repository) bundlesLocked(ctx context.Context) (map[string]*Bundle, error) {
	var files []backend.FileInfo
	if err := r.be.List(ctx, backend.BundleFile, func(fi backend.FileInfo) error {
		if strings.HasSuffix(fi.Name, bundleExt) {
			files = append(files, fi)
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("list backup files: %w", err)
	}
	if r.bundles == nil {
		r.bundles = map[string]*Bundle{}
	}
	present := map[string]bool{}
	var todo []backend.FileInfo
	for _, fi := range files {
		present[fi.Name] = true
		if _, ok := r.bundles[fi.Name]; !ok {
			todo = append(todo, fi)
		}
	}
	for n := range r.bundles {
		if !present[n] {
			delete(r.bundles, n)
		}
	}
	var mu sync.Mutex
	var firstErr error
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for _, fi := range todo {
		wg.Add(1)
		go func(fi backend.FileInfo) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			b, err := r.loadBundle(ctx, fi.Name, fi.Size)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				return
			}
			r.bundles[fi.Name] = b
		}(fi)
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	out := make(map[string]*Bundle, len(r.bundles))
	for k, v := range r.bundles {
		out[k] = v
	}
	return out, nil
}

// resolveBundle follows "replaces" records: a file merged by prune stands
// in for the files it replaced.
func resolveBundle(all map[string]*Bundle, replacedBy map[string]string, name string) (*Bundle, bool) {
	for range 64 {
		if nb, ok := replacedBy[name]; ok && nb != name {
			name = nb
			continue
		}
		b, ok := all[name]
		return b, ok
	}
	return nil, false
}

func replacements(all map[string]*Bundle) map[string]string {
	m := map[string]string{}
	names := make([]string, 0, len(all))
	for n := range all {
		names = append(names, n)
	}
	sort.Strings(names) // deterministic when two files replace the same one
	for _, n := range names {
		for _, x := range all[n].Replaces {
			m[x] = n
		}
	}
	return m
}

// chain returns the files a backup needs, starting with its own file and
// ending with the full backup (or merged file) at the root.
func chainOf(all map[string]*Bundle, replacedBy map[string]string, name string) ([]*Bundle, error) {
	var out []*Bundle
	seen := map[string]bool{}
	for name != "" {
		b, ok := resolveBundle(all, replacedBy, name)
		if !ok {
			return out, fmt.Errorf("backup file %s is missing", name)
		}
		if seen[b.Name] {
			return out, fmt.Errorf("backup file %s refers to itself", b.Name)
		}
		seen[b.Name] = true
		out = append(out, b)
		name = b.Parent
	}
	return out, nil
}

func (r *Repository) chainLocked(ctx context.Context, name string) ([]*Bundle, error) {
	if name == "" {
		return nil, errors.New("not stored in a backup file")
	}
	all, err := r.bundlesLocked(ctx)
	if err != nil {
		return nil, err
	}
	return chainOf(all, replacements(all), name)
}

// ChainIn is Chain on a set of files returned by Bundles.
func ChainIn(all map[string]*Bundle, s *Snapshot) ([]*Bundle, error) {
	if s.bundle == "" {
		return nil, nil
	}
	return chainOf(all, replacements(all), s.bundle)
}

// Chain returns the backup files a snapshot needs, its own first. It is
// empty for snapshots of the older repository format.
func (r *Repository) Chain(ctx context.Context, s *Snapshot) ([]*Bundle, error) {
	if s.bundle == "" {
		return nil, nil
	}
	r.bmu.Lock()
	defer r.bmu.Unlock()
	return r.chainLocked(ctx, s.bundle)
}

// forgottenSet lists the snapshots marked as forgotten.
func (r *Repository) forgottenSet(ctx context.Context) (id.Set, error) {
	s := id.Set{}
	err := r.be.List(ctx, backend.ForgetFile, func(fi backend.FileInfo) error {
		if i, err := id.Parse(fi.Name); err == nil {
			s.Insert(i)
		}
		return nil
	})
	return s, err
}
