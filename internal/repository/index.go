package repository

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/lamisator/herakles/internal/backend"
	"github.com/lamisator/herakles/internal/id"
)

// BlobType distinguishes file content from directory metadata.
type BlobType uint8

// Blob types.
const (
	DataBlob BlobType = 0
	TreeBlob BlobType = 1
)

func (t BlobType) String() string {
	if t == TreeBlob {
		return "tree"
	}
	return "data"
}

// PackEntry describes one blob inside a pack.
type PackEntry struct {
	ID        id.ID    `json:"id"`
	Type      BlobType `json:"type"`
	Offset    uint32   `json:"offset"`
	Length    uint32   `json:"length"`     // encrypted length in the pack
	RawLength uint32   `json:"raw_length"` // plaintext (uncompressed) length
}

// PackInfo lists the content of a pack.
type PackInfo struct {
	ID    id.ID       `json:"id"`
	Size  int64       `json:"size"`
	Blobs []PackEntry `json:"blobs"`
	// Offset is where the pack starts inside its backup file.
	Offset int64 `json:"offset,omitempty"`
	// File is the backup file holding the pack; empty for a pack file in
	// data/ (older repository format).
	File string `json:"-"`
}

// Location identifies where a blob is stored.
type Location struct {
	Pack id.ID
	File string // backup file, or "" for data/<Pack>
	Base int64  // offset of the pack in File
	PackEntry
}

// handle returns the file holding the blob and the blob's offset in it.
func (l Location) handle() (backend.Handle, int64) {
	if l.File != "" {
		return backend.Handle{Type: backend.BundleFile, Name: l.File}, l.Base + int64(l.Offset)
	}
	return backend.Handle{Type: backend.PackFile, Name: l.Pack.String()}, int64(l.Offset)
}

// indexFile is the serialized form of an index file.
type indexFile struct {
	Packs []PackInfo `json:"packs"`
}

// Index maps blob IDs to pack locations.
type Index struct {
	mu    sync.RWMutex
	blobs map[id.ID]Location
	packs map[id.ID]*PackInfo
	files id.Set
}

// NewIndex returns an empty index.
func NewIndex() *Index {
	return &Index{blobs: map[id.ID]Location{}, packs: map[id.ID]*PackInfo{}, files: id.Set{}}
}

// AddPack registers all blobs of a pack.
func (x *Index) AddPack(p PackInfo) {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.addPackLocked(p)
}

func (x *Index) addPackLocked(p PackInfo) {
	if _, ok := x.packs[p.ID]; ok {
		return
	}
	pc := p
	x.packs[p.ID] = &pc
	for _, b := range p.Blobs {
		if _, ok := x.blobs[b.ID]; !ok {
			x.blobs[b.ID] = Location{Pack: p.ID, File: p.File, Base: p.Offset, PackEntry: b}
		}
	}
}

// Has reports whether a blob is indexed.
func (x *Index) Has(i id.ID) bool {
	x.mu.RLock()
	defer x.mu.RUnlock()
	_, ok := x.blobs[i]
	return ok
}

// Lookup returns the location of a blob.
func (x *Index) Lookup(i id.ID) (Location, bool) {
	x.mu.RLock()
	defer x.mu.RUnlock()
	l, ok := x.blobs[i]
	return l, ok
}

// Packs returns a copy of all pack infos.
func (x *Index) Packs() []PackInfo {
	x.mu.RLock()
	defer x.mu.RUnlock()
	out := make([]PackInfo, 0, len(x.packs))
	for _, p := range x.packs {
		out = append(out, *p)
	}
	return out
}

// Pack returns info about one pack.
func (x *Index) Pack(i id.ID) (PackInfo, bool) {
	x.mu.RLock()
	defer x.mu.RUnlock()
	p, ok := x.packs[i]
	if !ok {
		return PackInfo{}, false
	}
	return *p, true
}

// Count returns the number of blobs and packs.
func (x *Index) Count() (blobs, packs int) {
	x.mu.RLock()
	defer x.mu.RUnlock()
	return len(x.blobs), len(x.packs)
}

// Files returns the names of loaded index files.
func (x *Index) Files() id.Set {
	x.mu.RLock()
	defer x.mu.RUnlock()
	s := id.Set{}
	for k := range x.files {
		s.Insert(k)
	}
	return s
}

// LoadIndex (re)loads all index files and the tables of contents of the
// backup files.
func (r *Repository) LoadIndex(ctx context.Context) error {
	idx := NewIndex()
	var names []string
	if err := r.be.List(ctx, backend.IndexFile, func(fi backend.FileInfo) error {
		names = append(names, fi.Name)
		return nil
	}); err != nil {
		return fmt.Errorf("list index: %w", err)
	}
	type result struct {
		name string
		f    indexFile
		err  error
	}
	results := make(chan result, len(names))
	sem := make(chan struct{}, 8)
	for _, n := range names {
		go func(n string) {
			sem <- struct{}{}
			defer func() { <-sem }()
			var f indexFile
			err := r.LoadUnpacked(ctx, backend.IndexFile, n, &f)
			results <- result{n, f, err}
		}(n)
	}
	for range names {
		res := <-results
		if res.err != nil {
			return fmt.Errorf("load index %s: %w", res.name, res.err)
		}
		fid, err := id.Parse(res.name)
		if err != nil {
			return err
		}
		idx.mu.Lock()
		idx.files.Insert(fid)
		for _, p := range res.f.Packs {
			idx.addPackLocked(p)
		}
		idx.mu.Unlock()
	}
	bundles, err := r.Bundles(ctx)
	if err != nil {
		return err
	}
	names = names[:0]
	for n := range bundles {
		names = append(names, n)
	}
	sort.Strings(names)
	idx.mu.Lock()
	for _, n := range names {
		for _, p := range bundles[n].Packs {
			idx.addPackLocked(p)
		}
	}
	idx.mu.Unlock()
	r.idx = idx
	return nil
}

// maxPacksPerIndexFile bounds the size of index files.
const maxPacksPerIndexFile = 2000

// writeIndex stores packs into one or more new index files.
func (r *Repository) writeIndex(ctx context.Context, packs []PackInfo) ([]id.ID, error) {
	var written []id.ID
	for len(packs) > 0 {
		n := len(packs)
		if n > maxPacksPerIndexFile {
			n = maxPacksPerIndexFile
		}
		name, err := r.SaveUnpacked(ctx, backend.IndexFile, indexFile{Packs: packs[:n]})
		if err != nil {
			return written, err
		}
		r.idx.mu.Lock()
		r.idx.files.Insert(name)
		r.idx.mu.Unlock()
		written = append(written, name)
		packs = packs[n:]
	}
	return written, nil
}
