package engine

import (
	"context"
	"fmt"

	"github.com/lamisator/herakles/internal/id"
	"github.com/lamisator/herakles/internal/repository"
)

// CopyResult summarises a copy run.
type CopyResult struct {
	Copied  int   `json:"copied"`
	Skipped int   `json:"skipped"`
	Bytes   int64 `json:"bytes_added"`
}

// Copy transfers snapshots from src to dst (e.g. for off-site replication).
// Data is re-encrypted with dst's key; snapshots already copied are skipped.
func Copy(ctx context.Context, src, dst *repository.Repository, snaps []*repository.Snapshot, progress func(string)) (CopyResult, error) {
	var res CopyResult
	lock, err := dst.Lock(ctx, false, "copy")
	if err != nil {
		return res, err
	}
	defer lock.Unlock()
	existing, err := dst.Snapshots(ctx)
	if err != nil {
		return res, err
	}
	have := id.Set{}
	for _, s := range existing {
		if s.Original != nil {
			have.Insert(*s.Original)
		}
		have.Insert(s.ID())
	}
	blobMap := map[id.ID]id.ID{}
	treeMap := map[id.ID]id.ID{}
	var copyTree func(tid id.ID) (id.ID, error)
	copyTree = func(tid id.ID) (id.ID, error) {
		if n, ok := treeMap[tid]; ok {
			return n, nil
		}
		t, err := src.LoadTree(ctx, tid)
		if err != nil {
			return id.Null, err
		}
		out := &repository.Tree{}
		for _, n := range t.Nodes {
			nn := *n
			nn.Content = nil
			for _, c := range n.Content {
				m, ok := blobMap[c]
				if !ok {
					data, err := src.LoadBlob(ctx, c)
					if err != nil {
						return id.Null, err
					}
					var isNew bool
					var stored int
					m, isNew, stored, err = dst.SaveBlob(ctx, repository.DataBlob, data)
					if err != nil {
						return id.Null, err
					}
					if isNew {
						res.Bytes += int64(stored)
					}
					blobMap[c] = m
				}
				nn.Content = append(nn.Content, m)
			}
			if n.Subtree != nil {
				st, err := copyTree(*n.Subtree)
				if err != nil {
					return id.Null, err
				}
				nn.Subtree = &st
			}
			out.Nodes = append(out.Nodes, &nn)
		}
		nid, err := dst.SaveTree(ctx, out)
		if err != nil {
			return id.Null, err
		}
		treeMap[tid] = nid
		return nid, nil
	}
	for _, s := range snaps {
		orig := s.ID()
		if s.Original != nil {
			orig = *s.Original
		}
		if have.Has(orig) {
			res.Skipped++
			continue
		}
		if progress != nil {
			progress(fmt.Sprintf("copying snapshot %s (%s)", s.ID().Short(), s.Time.Local().Format("2006-01-02 15:04")))
		}
		tid, err := copyTree(s.Tree)
		if err != nil {
			return res, fmt.Errorf("snapshot %s: %w", s.ID().Short(), err)
		}
		if err := dst.Flush(ctx); err != nil {
			return res, err
		}
		ns := *s
		ns.Tree = tid
		ns.Parent = nil
		ns.Original = &orig
		if _, err := dst.SaveSnapshot(ctx, &ns); err != nil {
			return res, err
		}
		have.Insert(orig)
		res.Copied++
	}
	return res, nil
}
