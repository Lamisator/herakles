package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/lamisator/herakles/internal/id"
)

// NodeType is the kind of a filesystem entry.
type NodeType string

// Node types.
const (
	NodeFile    NodeType = "file"
	NodeDir     NodeType = "dir"
	NodeSymlink NodeType = "symlink"
	// NodeDevice is the image of a block device / partition (raw contents).
	NodeDevice  NodeType = "device"
	NodeCharDev NodeType = "chardev"
	NodeBlkDev  NodeType = "blockdev" // device node only (not its contents)
	NodeFIFO    NodeType = "fifo"
	NodeSocket  NodeType = "socket"
)

// Node is one entry of a directory tree.
type Node struct {
	Name       string            `json:"name"`
	Type       NodeType          `json:"type"`
	Mode       os.FileMode       `json:"mode"`
	ModTime    time.Time         `json:"mtime"`
	AccessTime time.Time         `json:"atime,omitempty"`
	ChangeTime time.Time         `json:"ctime,omitempty"`
	UID        uint32            `json:"uid"`
	GID        uint32            `json:"gid"`
	User       string            `json:"user,omitempty"`
	Group      string            `json:"group,omitempty"`
	Inode      uint64            `json:"inode,omitempty"`
	DeviceID   uint64            `json:"device_id,omitempty"`
	Links      uint64            `json:"links,omitempty"`
	Rdev       uint64            `json:"rdev,omitempty"`
	Size       uint64            `json:"size"`
	LinkTarget string            `json:"link_target,omitempty"`
	Xattrs     map[string][]byte `json:"xattrs,omitempty"`
	Content    []id.ID           `json:"content,omitempty"`
	Subtree    *id.ID            `json:"subtree,omitempty"`
	// Source is set on device images and records the original device path.
	Source string `json:"source,omitempty"`
}

// Tree is a directory listing.
type Tree struct {
	Nodes []*Node `json:"nodes"`
}

// Sort orders nodes by name.
func (t *Tree) Sort() {
	sort.Slice(t.Nodes, func(i, j int) bool { return t.Nodes[i].Name < t.Nodes[j].Name })
}

// Find returns the node with the given name.
func (t *Tree) Find(name string) *Node {
	i := sort.Search(len(t.Nodes), func(i int) bool { return t.Nodes[i].Name >= name })
	if i < len(t.Nodes) && t.Nodes[i].Name == name {
		return t.Nodes[i]
	}
	return nil
}

// SaveTree stores a tree blob.
func (r *Repository) SaveTree(ctx context.Context, t *Tree) (id.ID, error) {
	t.Sort()
	raw, err := json.Marshal(t)
	if err != nil {
		return id.Null, err
	}
	i, _, _, err := r.SaveBlob(ctx, TreeBlob, raw)
	return i, err
}

const treeCacheSize = 4096

// LoadTree loads a tree blob (cached).
func (r *Repository) LoadTree(ctx context.Context, i id.ID) (*Tree, error) {
	r.treeMu.Lock()
	if t, ok := r.trees[i]; ok {
		r.treeMu.Unlock()
		return t, nil
	}
	r.treeMu.Unlock()
	raw, err := r.LoadBlob(ctx, i)
	if err != nil {
		return nil, err
	}
	t := &Tree{}
	if err := json.Unmarshal(raw, t); err != nil {
		return nil, fmt.Errorf("decode tree %s: %w", i.Short(), err)
	}
	t.Sort()
	r.treeMu.Lock()
	if len(r.treeList) >= treeCacheSize {
		delete(r.trees, r.treeList[0])
		r.treeList = r.treeList[1:]
	}
	r.trees[i] = t
	r.treeList = append(r.treeList, i)
	r.treeMu.Unlock()
	return t, nil
}

// WalkFunc is called for each node; path uses "/" separators and starts with
// "/". Returning SkipDir from a directory skips its children.
type WalkFunc func(path string, n *Node) error

// SkipDir can be returned by a WalkFunc to skip a directory.
var SkipDir = fmt.Errorf("skip directory")

// Walk traverses the tree rooted at root depth-first in name order.
func (r *Repository) Walk(ctx context.Context, root id.ID, fn WalkFunc) error {
	return r.walk(ctx, root, "", fn)
}

func (r *Repository) walk(ctx context.Context, tid id.ID, prefix string, fn WalkFunc) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	t, err := r.LoadTree(ctx, tid)
	if err != nil {
		return err
	}
	for _, n := range t.Nodes {
		p := prefix + "/" + n.Name
		err := fn(p, n)
		if err == SkipDir {
			continue
		}
		if err != nil {
			return err
		}
		if n.Type == NodeDir && n.Subtree != nil {
			if err := r.walk(ctx, *n.Subtree, p, fn); err != nil {
				return err
			}
		}
	}
	return nil
}

// FindNode resolves a slash separated path within a tree. "/" or "" returns a
// synthetic directory node for the root.
func (r *Repository) FindNode(ctx context.Context, root id.ID, p string) (*Node, error) {
	cur := &Node{Name: "/", Type: NodeDir, Subtree: &root, Mode: os.ModeDir | 0o755}
	for _, part := range splitPath(p) {
		if cur.Type != NodeDir || cur.Subtree == nil {
			return nil, fmt.Errorf("%s: not a directory", p)
		}
		t, err := r.LoadTree(ctx, *cur.Subtree)
		if err != nil {
			return nil, err
		}
		next := t.Find(part)
		if next == nil {
			return nil, fmt.Errorf("%s: no such file or directory in snapshot", p)
		}
		cur = next
	}
	return cur, nil
}

// FindImage finds the disk image (device node) that path p leads into: it
// returns the image node, its path in the snapshot and the rest of p
// inside the image ("" for the image itself). ok is false when p does not
// lead into an image.
func (r *Repository) FindImage(ctx context.Context, root id.ID, p string) (dev *Node, devPath, inner string, ok bool, err error) {
	cur := &Node{Name: "/", Type: NodeDir, Subtree: &root}
	parts := splitPath(p)
	for i, part := range parts {
		if cur.Type == NodeDevice {
			devPath = "/" + strings.Join(parts[:i], "/")
			// Inside the image only "/" separates (a "\\" is part of a name).
			inner = strings.Join(parts[i:], "/")
			if full := "/" + strings.Trim(p, "/"); strings.HasPrefix(full, devPath+"/") {
				inner = strings.TrimPrefix(full, devPath+"/")
			}
			return cur, devPath, inner, true, nil
		}
		if cur.Type != NodeDir || cur.Subtree == nil {
			return nil, "", "", false, nil
		}
		t, err := r.LoadTree(ctx, *cur.Subtree)
		if err != nil {
			return nil, "", "", false, err
		}
		if cur = t.Find(part); cur == nil {
			return nil, "", "", false, nil
		}
	}
	if cur.Type == NodeDevice {
		return cur, "/" + strings.Join(parts, "/"), "", true, nil
	}
	return nil, "", "", false, nil
}

func splitPath(p string) []string {
	var out []string
	cur := ""
	for i := 0; i <= len(p); i++ {
		if i == len(p) || p[i] == '/' || p[i] == '\\' {
			if cur != "" && cur != "." {
				// Windows drive letters ("C:") are stored as "C".
				if len(out) == 0 && len(cur) == 2 && cur[1] == ':' {
					cur = cur[:1]
				}
				out = append(out, cur)
			}
			cur = ""
			continue
		}
		cur += string(p[i])
	}
	return out
}
