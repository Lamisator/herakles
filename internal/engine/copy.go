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
	// Every copied snapshot is one backup file in dst. A snapshot whose
	// parent was copied in this run builds on the parent's chain there;
	// the others become full backups. memo remembers what a chain holds.
	type copied struct {
		snap *repository.Snapshot
		memo *copyMemo
	}
	done := map[id.ID]copied{}
	var memo *copyMemo
	var copyTree func(tid id.ID) (id.ID, error)
	copyTree = func(tid id.ID) (id.ID, error) {
		if n, ok := memo.tree(tid); ok {
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
				m, ok := memo.blob(c)
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
					memo.blobs[c] = m
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
		memo.trees[tid] = nid
		return nid, nil
	}
	snaps = append([]*repository.Snapshot(nil), snaps...)
	repository.SortSnapshots(snaps)
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
		var parent *repository.Snapshot
		var pmemo *copyMemo
		if s.Parent != nil {
			if c, ok := done[*s.Parent]; ok {
				parent, pmemo = c.snap, c.memo
			}
		}
		parent, err := dst.BeginBackup(ctx, repository.BackupStart{Job: s.Job, Hostname: s.Hostname, Type: s.Type, Parent: parent})
		if err != nil {
			return res, err
		}
		if parent == nil {
			pmemo = nil
		}
		memo = &copyMemo{parent: pmemo, blobs: map[id.ID]id.ID{}, trees: map[id.ID]id.ID{}}
		tid, err := copyTree(s.Tree)
		if err != nil {
			dst.AbortBackup()
			return res, fmt.Errorf("snapshot %s: %w", s.ID().Short(), err)
		}
		ns := *s
		ns.Tree = tid
		ns.Parent = nil
		if parent != nil {
			pid := parent.ID()
			ns.Parent = &pid
		} else {
			ns.Type = repository.TypeFull
		}
		ns.Original = &orig
		if _, err := dst.SaveSnapshot(ctx, &ns); err != nil {
			return res, err
		}
		done[s.ID()] = copied{&ns, memo}
		have.Insert(orig)
		res.Copied++
	}
	return res, nil
}

// copyMemo maps source to destination IDs for one copied snapshot; lookups
// fall back to the chain it builds on.
type copyMemo struct {
	parent       *copyMemo
	blobs, trees map[id.ID]id.ID
}

func (m *copyMemo) blob(i id.ID) (id.ID, bool) {
	for ; m != nil; m = m.parent {
		if v, ok := m.blobs[i]; ok {
			return v, true
		}
	}
	return id.Null, false
}

func (m *copyMemo) tree(i id.ID) (id.ID, bool) {
	for ; m != nil; m = m.parent {
		if v, ok := m.trees[i]; ok {
			return v, true
		}
	}
	return id.Null, false
}
