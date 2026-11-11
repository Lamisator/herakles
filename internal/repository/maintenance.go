package repository

import (
	"context"
	"fmt"
	"math/rand/v2"
	"sort"

	"github.com/lamisator/herakles/internal/backend"
	"github.com/lamisator/herakles/internal/id"
)

// UsedBlobs collects all tree and data blobs referenced by snapshots.
func (r *Repository) UsedBlobs(ctx context.Context, snaps []*Snapshot) (id.Set, error) {
	used := id.Set{}
	var visit func(tid id.ID) error
	visit = func(tid id.ID) error {
		if used.Has(tid) {
			return nil
		}
		used.Insert(tid)
		t, err := r.LoadTree(ctx, tid)
		if err != nil {
			return err
		}
		for _, n := range t.Nodes {
			for _, c := range n.Content {
				used.Insert(c)
			}
			if n.Subtree != nil {
				if err := visit(*n.Subtree); err != nil {
					return err
				}
			}
		}
		return nil
	}
	for _, s := range snaps {
		if err := visit(s.Tree); err != nil {
			return nil, fmt.Errorf("snapshot %s: %w", s.ID().Short(), err)
		}
	}
	return used, nil
}

// PruneOptions control garbage collection.
type PruneOptions struct {
	// RepackThreshold: packs whose unused fraction exceeds this are repacked.
	RepackThreshold float64
	DryRun          bool
	Progress        func(msg string)
}

// PruneStats reports what prune did (or would do).
type PruneStats struct {
	PacksTotal        int   `json:"packs_total"`
	PacksDeleted      int   `json:"packs_deleted"`
	PacksRepacked     int   `json:"packs_repacked"`
	UnreferencedPacks int   `json:"unreferenced_packs"`
	BlobsUsed         int   `json:"blobs_used"`
	BlobsRemoved      int   `json:"blobs_removed"`
	BytesBefore       int64 `json:"bytes_before"`
	BytesFreed        int64 `json:"bytes_freed"`
	BytesRepacked     int64 `json:"bytes_repacked"`
}

// Prune removes data no longer referenced by any snapshot. The caller must
// hold an exclusive lock.
func (r *Repository) Prune(ctx context.Context, opts PruneOptions) (PruneStats, error) {
	var st PruneStats
	logf := func(f string, a ...any) {
		if opts.Progress != nil {
			opts.Progress(fmt.Sprintf(f, a...))
		}
	}
	if opts.RepackThreshold <= 0 {
		opts.RepackThreshold = 0.2
	}
	if err := r.LoadIndex(ctx); err != nil {
		return st, err
	}
	snaps, err := r.Snapshots(ctx)
	if err != nil {
		return st, err
	}
	logf("finding data referenced by %d snapshots", len(snaps))
	used, err := r.UsedBlobs(ctx, snaps)
	if err != nil {
		return st, err
	}
	st.BlobsUsed = len(used)

	// Decide for every pack which blobs are kept in it.
	packs := r.idx.Packs()
	sort.Slice(packs, func(i, j int) bool { return packs[i].ID.String() < packs[j].ID.String() })
	keptIn := map[id.ID]id.ID{} // blob -> pack holding the kept copy
	type plan struct {
		info      PackInfo
		usedBytes int64
		kept      []PackEntry
	}
	var plans []*plan
	for _, p := range packs {
		pl := &plan{info: p}
		for _, b := range p.Blobs {
			if used.Has(b.ID) {
				if _, dup := keptIn[b.ID]; !dup {
					keptIn[b.ID] = p.ID
					pl.kept = append(pl.kept, b)
					pl.usedBytes += int64(b.Length)
					continue
				}
			}
			st.BlobsRemoved++
		}
		plans = append(plans, pl)
		st.BytesBefore += p.Size
	}
	st.PacksTotal = len(plans)
	for b := range used {
		if _, ok := keptIn[b]; !ok {
			return st, fmt.Errorf("blob %s is referenced but missing from the index; run 'herakles check' / 'herakles repair index'", b.Short())
		}
	}

	// Packs on the backend that no index references are leftovers of
	// interrupted operations.
	indexed := id.Set{}
	for _, p := range packs {
		indexed.Insert(p.ID)
	}
	var unref []backend.FileInfo
	if err := r.be.List(ctx, backend.PackFile, func(fi backend.FileInfo) error {
		pid, err := id.Parse(fi.Name)
		if err != nil || !indexed.Has(pid) {
			unref = append(unref, fi)
		}
		return nil
	}); err != nil {
		return st, err
	}
	st.UnreferencedPacks = len(unref)
	for _, fi := range unref {
		st.BytesFreed += fi.Size
	}

	var toDelete, toRepack []*plan
	var keep []*plan
	for _, pl := range plans {
		switch {
		case len(pl.kept) == 0:
			toDelete = append(toDelete, pl)
			st.BytesFreed += pl.info.Size
		case float64(pl.info.Size-pl.usedBytes)/float64(pl.info.Size) > opts.RepackThreshold:
			toRepack = append(toRepack, pl)
			st.BytesRepacked += pl.usedBytes
			st.BytesFreed += pl.info.Size - pl.usedBytes
		default:
			keep = append(keep, pl)
		}
	}
	st.PacksDeleted = len(toDelete)
	st.PacksRepacked = len(toRepack)
	logf("%d packs: %d to delete, %d to repack, %d unchanged, %d unreferenced", len(plans), len(toDelete), len(toRepack), len(keep), len(unref))
	if opts.DryRun {
		return st, nil
	}
	if len(toDelete) == 0 && len(toRepack) == 0 && len(unref) == 0 && len(r.idx.Files()) <= 1 {
		return st, nil
	}

	// Repack: copy kept encrypted blobs verbatim (after verification).
	before := id.Set{}
	for _, p := range packs {
		before.Insert(p.ID)
	}
	for i, pl := range toRepack {
		logf("repacking %d/%d", i+1, len(toRepack))
		data, err := backend.LoadAll(ctx, r.be, backend.Handle{Type: backend.PackFile, Name: pl.info.ID.String()})
		if err != nil {
			return st, err
		}
		for _, e := range pl.kept {
			ct := data[e.Offset : e.Offset+e.Length]
			if _, err := r.DecodeBlob(e.ID, ct); err != nil {
				return st, fmt.Errorf("repack %s: %w", pl.info.ID.Short(), err)
			}
			cp := append([]byte(nil), ct...)
			if err := r.addToPacker(ctx, e.Type, e.ID, cp, int(e.RawLength)); err != nil {
				return st, err
			}
		}
	}
	if err := r.Flush(ctx); err != nil {
		return st, err
	}

	// Write a consolidated index containing only live data.
	var final []PackInfo
	for _, pl := range keep {
		final = append(final, PackInfo{ID: pl.info.ID, Size: pl.info.Size, Blobs: pl.kept})
	}
	for _, p := range r.idx.Packs() {
		if !before.Has(p.ID) {
			final = append(final, p)
		}
	}
	oldIndex := r.idx.Files()
	logf("writing new index (%d packs)", len(final))
	written, err := r.writeIndex(ctx, final)
	if err != nil {
		return st, err
	}
	newSet := id.NewSet(written...)
	for f := range oldIndex {
		if newSet.Has(f) {
			continue
		}
		if err := r.be.Remove(ctx, backend.Handle{Type: backend.IndexFile, Name: f.String()}); err != nil && !backend.IsNotFound(err) {
			return st, err
		}
	}
	logf("deleting obsolete packs")
	for _, pl := range append(toDelete, toRepack...) {
		if err := r.be.Remove(ctx, backend.Handle{Type: backend.PackFile, Name: pl.info.ID.String()}); err != nil && !backend.IsNotFound(err) {
			return st, err
		}
	}
	for _, fi := range unref {
		if err := r.be.Remove(ctx, backend.Handle{Type: backend.PackFile, Name: fi.Name}); err != nil && !backend.IsNotFound(err) {
			return st, err
		}
	}
	return st, r.LoadIndex(ctx)
}

// CheckOptions control repository verification.
type CheckOptions struct {
	// ReadData downloads and verifies the given fraction (0..1] of packs.
	ReadData float64
	Progress func(msg string)
}

// CheckResult summarises a check.
type CheckResult struct {
	Errors    []string `json:"errors"`
	Warnings  []string `json:"warnings"`
	Snapshots int      `json:"snapshots"`
	Trees     int      `json:"trees"`
	Packs     int      `json:"packs"`
	PacksRead int      `json:"packs_read"`
	BlobsRead int      `json:"blobs_read"`
	BytesRead int64    `json:"bytes_read"`
}

// OK reports whether no errors were found.
func (c *CheckResult) OK() bool { return len(c.Errors) == 0 }

// Check verifies repository consistency.
func (r *Repository) Check(ctx context.Context, opts CheckOptions) (*CheckResult, error) {
	res := &CheckResult{}
	errf := func(f string, a ...any) { res.Errors = append(res.Errors, fmt.Sprintf(f, a...)) }
	logf := func(f string, a ...any) {
		if opts.Progress != nil {
			opts.Progress(fmt.Sprintf(f, a...))
		}
	}
	if err := r.LoadIndex(ctx); err != nil {
		return nil, err
	}
	logf("checking packs")
	stored := map[id.ID]int64{}
	if err := r.be.List(ctx, backend.PackFile, func(fi backend.FileInfo) error {
		pid, err := id.Parse(fi.Name)
		if err != nil {
			res.Warnings = append(res.Warnings, "unexpected file data/"+fi.Name)
			return nil
		}
		stored[pid] = fi.Size
		return nil
	}); err != nil {
		return nil, err
	}
	packs := r.idx.Packs()
	res.Packs = len(packs)
	indexed := id.Set{}
	for _, p := range packs {
		indexed.Insert(p.ID)
		sz, ok := stored[p.ID]
		if !ok {
			errf("pack %s is referenced by the index but missing", p.ID.Short())
			continue
		}
		if sz != p.Size {
			errf("pack %s has size %d, index says %d", p.ID.Short(), sz, p.Size)
		}
	}
	for pid := range stored {
		if !indexed.Has(pid) {
			res.Warnings = append(res.Warnings, fmt.Sprintf("pack %s is not referenced by any index (run prune to remove)", pid.Short()))
		}
	}

	logf("checking snapshots and trees")
	names, err := r.ListNames(ctx, backend.SnapFile)
	if err != nil {
		return nil, err
	}
	seenTrees := id.Set{}
	var walkTree func(tid id.ID, path string)
	walkTree = func(tid id.ID, path string) {
		if seenTrees.Has(tid) {
			return
		}
		seenTrees.Insert(tid)
		res.Trees++
		t, err := r.LoadTree(ctx, tid)
		if err != nil {
			errf("tree %s (%s): %v", tid.Short(), path, err)
			return
		}
		for _, n := range t.Nodes {
			for _, c := range n.Content {
				if !r.idx.Has(c) {
					errf("%s/%s: data blob %s missing", path, n.Name, c.Short())
				}
			}
			if n.Type == NodeDir {
				if n.Subtree == nil {
					errf("%s/%s: directory without subtree", path, n.Name)
					continue
				}
				walkTree(*n.Subtree, path+"/"+n.Name)
			}
		}
	}
	for _, n := range names {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		sid, err := id.Parse(n)
		if err != nil {
			res.Warnings = append(res.Warnings, "unexpected file snapshots/"+n)
			continue
		}
		s, err := r.LoadSnapshot(ctx, sid)
		if err != nil {
			errf("snapshot %s: %v", sid.Short(), err)
			continue
		}
		res.Snapshots++
		walkTree(s.Tree, "snapshot "+sid.Short()+":")
	}

	if opts.ReadData > 0 {
		sel := packs
		if opts.ReadData < 1 {
			sel = nil
			for _, p := range packs {
				if rand.Float64() < opts.ReadData {
					sel = append(sel, p)
				}
			}
			if len(sel) == 0 && len(packs) > 0 {
				sel = packs[:1]
			}
		}
		for i, p := range sel {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if _, ok := stored[p.ID]; !ok {
				continue
			}
			logf("reading pack %d/%d", i+1, len(sel))
			data, err := backend.LoadAll(ctx, r.be, backend.Handle{Type: backend.PackFile, Name: p.ID.String()})
			if err != nil {
				errf("pack %s: %v", p.ID.Short(), err)
				continue
			}
			res.PacksRead++
			res.BytesRead += int64(len(data))
			if id.Hash(data) != p.ID {
				errf("pack %s: content hash mismatch (corrupted)", p.ID.Short())
				continue
			}
			hdr, err := r.ParsePackHeader(data)
			if err != nil {
				errf("pack %s: %v", p.ID.Short(), err)
				continue
			}
			inHdr := map[id.ID]PackEntry{}
			for _, e := range hdr {
				inHdr[e.ID] = e
			}
			for _, e := range p.Blobs {
				he, ok := inHdr[e.ID]
				if !ok || he.Offset != e.Offset || he.Length != e.Length {
					errf("pack %s: index entry for blob %s does not match pack header", p.ID.Short(), e.ID.Short())
					continue
				}
				if _, err := r.DecodeBlob(e.ID, data[e.Offset:e.Offset+e.Length]); err != nil {
					errf("pack %s: %v", p.ID.Short(), err)
					continue
				}
				res.BlobsRead++
			}
		}
	}
	return res, nil
}

// RebuildIndex reads all pack headers and writes a fresh index.
func (r *Repository) RebuildIndex(ctx context.Context, progress func(string)) (int, error) {
	var packs []PackInfo
	var names []string
	if err := r.be.List(ctx, backend.PackFile, func(fi backend.FileInfo) error {
		names = append(names, fi.Name)
		return nil
	}); err != nil {
		return 0, err
	}
	for i, n := range names {
		pid, err := id.Parse(n)
		if err != nil {
			continue
		}
		if progress != nil && i%50 == 0 {
			progress(fmt.Sprintf("reading pack headers %d/%d", i, len(names)))
		}
		entries, size, err := r.LoadPackHeader(ctx, pid)
		if err != nil {
			if progress != nil {
				progress(fmt.Sprintf("skipping damaged pack %s: %v", pid.Short(), err))
			}
			continue
		}
		packs = append(packs, PackInfo{ID: pid, Size: size, Blobs: entries})
	}
	old, err := r.ListNames(ctx, backend.IndexFile)
	if err != nil {
		return 0, err
	}
	r.idx = NewIndex()
	written, err := r.writeIndex(ctx, packs)
	if err != nil {
		return 0, err
	}
	keep := id.NewSet(written...)
	for _, n := range old {
		if fid, err := id.Parse(n); err == nil && keep.Has(fid) {
			continue
		}
		if err := r.be.Remove(ctx, backend.Handle{Type: backend.IndexFile, Name: n}); err != nil && !backend.IsNotFound(err) {
			return len(packs), err
		}
	}
	return len(packs), r.LoadIndex(ctx)
}

// Stats summarises repository usage.
type Stats struct {
	Snapshots   int   `json:"snapshots"`
	Packs       int   `json:"packs"`
	Blobs       int   `json:"blobs"`
	DataBlobs   int   `json:"data_blobs"`
	TreeBlobs   int   `json:"tree_blobs"`
	StoredBytes int64 `json:"stored_bytes"`
	RawBytes    int64 `json:"raw_bytes"`
}

// Stats computes repository statistics from the index.
func (r *Repository) Stats(ctx context.Context) (Stats, error) {
	var st Stats
	names, err := r.ListNames(ctx, backend.SnapFile)
	if err != nil {
		return st, err
	}
	st.Snapshots = len(names)
	seen := id.Set{}
	for _, p := range r.idx.Packs() {
		st.Packs++
		st.StoredBytes += p.Size
		for _, b := range p.Blobs {
			if seen.Has(b.ID) {
				continue
			}
			seen.Insert(b.ID)
			st.Blobs++
			st.RawBytes += int64(b.RawLength)
			if b.Type == TreeBlob {
				st.TreeBlobs++
			} else {
				st.DataBlobs++
			}
		}
	}
	return st, nil
}
