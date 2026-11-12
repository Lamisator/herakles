package repository

import (
	"context"
	"fmt"
	"math/rand/v2"
	"sort"

	"github.com/lamisator/herakles/internal/backend"
	"github.com/lamisator/herakles/internal/id"
)

// pruneBundles deletes backup files no live backup needs and merges the
// expired beginning of a chain when much of its data is no longer used.
func (r *Repository) pruneBundles(ctx context.Context, snaps []*Snapshot, opts PruneOptions, st *PruneStats, logf func(string, ...any)) error {
	all, err := r.Bundles(ctx)
	if err != nil {
		return err
	}
	st.FilesTotal = len(all)
	if len(all) == 0 {
		return r.cleanForgotten(ctx, all, opts.DryRun)
	}
	repl := replacements(all)
	live := map[string]*Snapshot{} // file -> its live snapshot
	for _, s := range snaps {
		if s.bundle != "" {
			live[s.bundle] = s
		}
	}

	// Files needed by live backups, and per chain root the expired files
	// at its beginning (all files from the root up to the first live one).
	needed := map[string]bool{}
	expired := map[string]map[string]bool{} // root -> files
	descendants := map[string][]*Snapshot{} // root -> live snapshots
	names := make([]string, 0, len(live))
	for n := range live {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		chain, err := chainOf(all, repl, n)
		if err != nil {
			return fmt.Errorf("snapshot %s: %w", live[n].ID().Short(), err)
		}
		for _, b := range chain {
			needed[b.Name] = true
		}
		root := chain[len(chain)-1].Name
		descendants[root] = append(descendants[root], live[n])
		for i := len(chain) - 1; i > 0; i-- {
			if _, ok := live[chain[i].Name]; ok {
				break
			}
			if expired[root] == nil {
				expired[root] = map[string]bool{}
			}
			expired[root][chain[i].Name] = true
		}
	}

	var del []*Bundle
	for _, b := range all {
		if !needed[b.Name] {
			del = append(del, b)
			st.BytesFreed += b.Size
		}
		st.BytesBefore += b.Size
	}
	st.FilesDeleted = len(del)

	type merge struct {
		root  string
		files []*Bundle
		keep  id.Set
		size  int64
		used  int64
	}
	var merges []*merge
	roots := make([]string, 0, len(expired))
	for r := range expired {
		roots = append(roots, r)
	}
	sort.Strings(roots)
	for _, root := range roots {
		m := &merge{root: root}
		for n := range expired[root] {
			m.files = append(m.files, all[n])
		}
		sort.Slice(m.files, func(i, j int) bool { return m.files[i].Name < m.files[j].Name })
		used, err := r.UsedBlobs(ctx, descendants[root])
		if err != nil {
			return err
		}
		m.keep = id.Set{}
		for _, b := range m.files {
			m.size += b.Size
			for _, p := range b.Packs {
				for _, e := range p.Blobs {
					if used.Has(e.ID) && !m.keep.Has(e.ID) {
						m.keep.Insert(e.ID)
						m.used += int64(e.Length)
					}
				}
			}
		}
		if m.size > 0 && float64(m.size-m.used)/float64(m.size) > opts.RepackThreshold {
			merges = append(merges, m)
			st.FilesMerged += len(m.files)
			st.BytesRepacked += m.used
			st.BytesFreed += m.size - m.used
		}
	}
	logf("%d backup files: %d to delete, %d to merge into %d", len(all), len(del), st.FilesMerged, len(merges))
	if opts.DryRun {
		return nil
	}

	for i, m := range merges {
		logf("merging expired backup files %d/%d", i+1, len(merges))
		if err := r.mergeBundles(ctx, m.files, m.keep); err != nil {
			return err
		}
		del = append(del, m.files...)
	}
	for _, b := range del {
		if err := r.be.Remove(ctx, backend.Handle{Type: backend.BundleFile, Name: b.Name}); err != nil && !backend.IsNotFound(err) {
			return err
		}
	}
	if pr, ok := r.be.(backend.PartialRemover); ok {
		if n, err := pr.RemovePartials(ctx, backend.BundleFile); err == nil && n > 0 {
			logf("removed %d unfinished backup files of interrupted backups", n)
		}
	}
	all, err = r.Bundles(ctx)
	if err != nil {
		return err
	}
	return r.cleanForgotten(ctx, all, false)
}

// mergeBundles writes the blobs in keep from files into one new backup
// file that replaces them. The caller deletes the files afterwards.
func (r *Repository) mergeBundles(ctx context.Context, files []*Bundle, keep id.Set) error {
	replaces := make([]string, 0, len(files))
	for _, b := range files {
		replaces = append(replaces, b.Name)
		// What the merged files replaced is replaced again.
		replaces = append(replaces, b.Replaces...)
	}
	r.bmu.Lock()
	err := r.openBundleLocked(ctx, bundleName(files[0].Created, "merged", "merged"), "", replaces, id.Set{})
	r.bmu.Unlock()
	if err != nil {
		return err
	}
	written := id.Set{}
	for _, b := range files {
		for _, p := range b.Packs {
			var want []PackEntry
			for _, e := range p.Blobs {
				if keep.Has(e.ID) && !written.Has(e.ID) {
					want = append(want, e)
				}
			}
			if len(want) == 0 {
				continue
			}
			data, err := backend.LoadRange(ctx, r.be, backend.Handle{Type: backend.BundleFile, Name: b.Name}, p.Offset, p.Size)
			if err != nil {
				r.AbortBackup()
				return err
			}
			for _, e := range want {
				ct := data[e.Offset : e.Offset+e.Length]
				if _, err := r.DecodeBlob(e.ID, ct); err != nil {
					r.AbortBackup()
					return fmt.Errorf("merge %s: %w", b.Name, err)
				}
				written.Insert(e.ID)
				r.pmu.Lock()
				r.pending.Insert(e.ID)
				r.pmu.Unlock()
				if err := r.addToPacker(ctx, e.Type, e.ID, append([]byte(nil), ct...), int(e.RawLength)); err != nil {
					r.AbortBackup()
					return err
				}
			}
		}
	}
	_, err = r.commitBundle(ctx, nil)
	return err
}

// cleanForgotten removes markers of snapshots whose files are gone.
func (r *Repository) cleanForgotten(ctx context.Context, all map[string]*Bundle, dryRun bool) error {
	present := id.Set{}
	for _, b := range all {
		if b.Snapshot != nil {
			present.Insert(b.Snapshot.id)
		}
	}
	forgotten, err := r.forgottenSet(ctx)
	if err != nil || dryRun {
		return err
	}
	for i := range forgotten {
		if !present.Has(i) {
			if err := r.be.Remove(ctx, backend.Handle{Type: backend.ForgetFile, Name: i.String()}); err != nil && !backend.IsNotFound(err) {
				return err
			}
		}
	}
	return nil
}

// checkBundles verifies the backup files: chains complete, every snapshot
// refers only to data in its own chain, and (with ReadData) the content.
func (r *Repository) checkBundles(ctx context.Context, opts CheckOptions, res *CheckResult) error {
	errf := func(f string, a ...any) { res.Errors = append(res.Errors, fmt.Sprintf(f, a...)) }
	all, err := r.Bundles(ctx)
	if err != nil {
		return err
	}
	res.Files = len(all)
	repl := replacements(all)
	forgotten, err := r.forgottenSet(ctx)
	if err != nil {
		return err
	}
	names := make([]string, 0, len(all))
	for n := range all {
		names = append(names, n)
	}
	sort.Strings(names)
	trees := id.Set{}
	defer func() { res.Trees += len(trees) }()
	for _, n := range names {
		b := all[n]
		if b.Snapshot == nil || forgotten.Has(b.Snapshot.id) {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		res.Snapshots++
		chain, err := chainOf(all, repl, n)
		if err != nil {
			errf("snapshot %s: %v", b.Snapshot.id.Short(), err)
			continue
		}
		inChain := id.Set{}
		for _, c := range chain {
			for _, p := range c.Packs {
				for _, e := range p.Blobs {
					inChain.Insert(e.ID)
				}
			}
		}
		missing := 0
		seen := id.Set{}
		var walk func(tid id.ID) error
		walk = func(tid id.ID) error {
			if seen.Has(tid) {
				return nil
			}
			seen.Insert(tid)
			trees.Insert(tid)
			if !inChain.Has(tid) {
				missing++
			}
			t, err := r.LoadTree(ctx, tid)
			if err != nil {
				return err
			}
			for _, nd := range t.Nodes {
				for _, c := range nd.Content {
					if !inChain.Has(c) {
						missing++
					}
				}
				if nd.Subtree != nil {
					if err := walk(*nd.Subtree); err != nil {
						return err
					}
				}
			}
			return nil
		}
		if err := walk(b.Snapshot.Tree); err != nil {
			errf("snapshot %s: %v", b.Snapshot.id.Short(), err)
			continue
		}
		if missing > 0 {
			errf("snapshot %s (%s): %d blobs are not in its own chain of backup files", b.Snapshot.id.Short(), n, missing)
		}
	}
	if opts.ReadData <= 0 {
		return nil
	}
	for i, n := range names {
		if opts.ReadData < 1 && rand.Float64() >= opts.ReadData && !(i == len(names)-1 && res.FilesRead == 0) {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if opts.Progress != nil {
			opts.Progress(fmt.Sprintf("reading backup file %d/%d", i+1, len(names)))
		}
		b := all[n]
		h := backend.Handle{Type: backend.BundleFile, Name: n}
		head, err := backend.LoadRange(ctx, r.be, h, 0, int64(len(bundleMagic)))
		if err != nil || string(head) != bundleMagic {
			errf("backup file %s: not a backup file (%v)", n, err)
			continue
		}
		res.FilesRead++
		for _, p := range b.Packs {
			data, err := backend.LoadRange(ctx, r.be, h, p.Offset, p.Size)
			if err != nil {
				errf("backup file %s: %v", n, err)
				break
			}
			res.PacksRead++
			res.BytesRead += int64(len(data))
			if id.Hash(data) != p.ID {
				errf("backup file %s: pack %s: content hash mismatch (corrupted)", n, p.ID.Short())
				continue
			}
			for _, e := range p.Blobs {
				if _, err := r.DecodeBlob(e.ID, data[e.Offset:e.Offset+e.Length]); err != nil {
					errf("backup file %s: %v", n, err)
					continue
				}
				res.BlobsRead++
			}
		}
	}
	return nil
}
