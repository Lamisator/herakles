package restorer

import (
	"context"
	"fmt"
	"io"

	"github.com/lamisator/herakles/internal/archiver"
	"github.com/lamisator/herakles/internal/id"
	"github.com/lamisator/herakles/internal/repository"
)

// DumpFile writes the content of a file/device node to w.
func DumpFile(ctx context.Context, repo *repository.Repository, n *repository.Node, w io.Writer) error {
	for _, c := range n.Content {
		data, err := repo.LoadBlob(ctx, c)
		if err != nil {
			return err
		}
		if _, err := w.Write(data); err != nil {
			return err
		}
	}
	return nil
}

// Change is one entry of a snapshot diff.
type Change struct {
	Path     string `json:"path"`
	Kind     string `json:"kind"` // added, removed, modified, type
	OldSize  uint64 `json:"old_size"`
	NewSize  uint64 `json:"new_size"`
	Metadata bool   `json:"metadata_only,omitempty"`
}

// Diff compares two snapshot trees.
func Diff(ctx context.Context, repo *repository.Repository, a, b id.ID) ([]Change, error) {
	var out []Change
	var cmp func(ta, tb *id.ID, prefix string) error
	listAll := func(t id.ID, prefix, kind string) error {
		return repo.Walk(ctx, t, func(p string, n *repository.Node) error {
			c := Change{Path: prefix + p, Kind: kind}
			if kind == "added" {
				c.NewSize = n.Size
			} else {
				c.OldSize = n.Size
			}
			out = append(out, c)
			return nil
		})
	}
	cmp = func(ta, tb *id.ID, prefix string) error {
		if ta != nil && tb != nil && *ta == *tb {
			return nil
		}
		var treeA, treeB *repository.Tree
		var err error
		if ta != nil {
			if treeA, err = repo.LoadTree(ctx, *ta); err != nil {
				return err
			}
		} else {
			treeA = &repository.Tree{}
		}
		if tb != nil {
			if treeB, err = repo.LoadTree(ctx, *tb); err != nil {
				return err
			}
		} else {
			treeB = &repository.Tree{}
		}
		i, j := 0, 0
		for i < len(treeA.Nodes) || j < len(treeB.Nodes) {
			var na, nb *repository.Node
			switch {
			case i >= len(treeA.Nodes):
				nb = treeB.Nodes[j]
				j++
			case j >= len(treeB.Nodes):
				na = treeA.Nodes[i]
				i++
			case treeA.Nodes[i].Name < treeB.Nodes[j].Name:
				na = treeA.Nodes[i]
				i++
			case treeA.Nodes[i].Name > treeB.Nodes[j].Name:
				nb = treeB.Nodes[j]
				j++
			default:
				na, nb = treeA.Nodes[i], treeB.Nodes[j]
				i++
				j++
			}
			switch {
			case na == nil:
				p := prefix + "/" + nb.Name
				out = append(out, Change{Path: p, Kind: "added", NewSize: nb.Size})
				if nb.Type == repository.NodeDir && nb.Subtree != nil {
					if err := listAll(*nb.Subtree, p, "added"); err != nil {
						return err
					}
				}
			case nb == nil:
				p := prefix + "/" + na.Name
				out = append(out, Change{Path: p, Kind: "removed", OldSize: na.Size})
				if na.Type == repository.NodeDir && na.Subtree != nil {
					if err := listAll(*na.Subtree, p, "removed"); err != nil {
						return err
					}
				}
			default:
				p := prefix + "/" + na.Name
				if na.Type != nb.Type {
					out = append(out, Change{Path: p, Kind: "type", OldSize: na.Size, NewSize: nb.Size})
					continue
				}
				if na.Type == repository.NodeDir {
					if err := cmp(na.Subtree, nb.Subtree, p); err != nil {
						return err
					}
					continue
				}
				if !sameContent(na, nb) {
					out = append(out, Change{Path: p, Kind: "modified", OldSize: na.Size, NewSize: nb.Size})
				} else if na.Mode != nb.Mode || !na.ModTime.Equal(nb.ModTime) || na.UID != nb.UID || na.GID != nb.GID || na.LinkTarget != nb.LinkTarget {
					out = append(out, Change{Path: p, Kind: "modified", OldSize: na.Size, NewSize: nb.Size, Metadata: true})
				}
			}
		}
		return nil
	}
	return out, cmp(&a, &b, "")
}

func sameContent(a, b *repository.Node) bool {
	if len(a.Content) != len(b.Content) || a.Size != b.Size {
		return false
	}
	for i := range a.Content {
		if a.Content[i] != b.Content[i] {
			return false
		}
	}
	return true
}

// Match is a search result.
type Match struct {
	Snapshot *repository.Snapshot
	Path     string
	Node     *repository.Node
}

// Find searches snapshots for paths matching a pattern (same syntax as excludes).
func Find(ctx context.Context, repo *repository.Repository, snaps []*repository.Snapshot, pattern string, limit int) ([]Match, error) {
	f, err := archiver.NewFilter([]string{pattern})
	if err != nil {
		return nil, err
	}
	var out []Match
	for _, s := range snaps {
		err := repo.Walk(ctx, s.Tree, func(p string, n *repository.Node) error {
			if f.Excluded(p) {
				out = append(out, Match{Snapshot: s, Path: p, Node: n})
				if limit > 0 && len(out) >= limit {
					return errLimit
				}
			}
			return nil
		})
		if err == errLimit {
			break
		}
		if err != nil {
			return out, err
		}
	}
	return out, nil
}

var errLimit = fmt.Errorf("limit reached")
