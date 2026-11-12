package imagefs

import (
	"errors"
	"io"
	"io/fs"
	"path"
	"strings"
	"sync"

	"www.velocidex.com/golang/go-ntfs/parser"
)

// ntfsFS reads NTFS through go-ntfs (www.velocidex.com/golang/go-ntfs).
type ntfsFS struct {
	mu  sync.Mutex // the parser's caches are not safe for parallel use
	ctx *parser.NTFSContext
}

func openNTFS(r io.ReaderAt, size int64) (FS, error) {
	var ctx *parser.NTFSContext
	err := safely(func() error {
		var err error
		ctx, err = parser.GetNTFSContext(r, 0)
		if err != nil {
			return err
		}
		_, err = ctx.GetMFT(5)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &ntfsFS{ctx: ctx}, nil
}

// hidden leaves out NTFS metadata files and alternate data streams.
func ntfsHidden(dir, name string) bool {
	return name == "." || name == ".." || strings.Contains(name, ":") || dir == "" && strings.HasPrefix(name, "$") ||
		dir == "" && name == "System Volume Information"
}

func (n *ntfsFS) dir(p string) (*parser.MFT_ENTRY, error) {
	root, err := n.ctx.GetMFT(5)
	if err != nil {
		return nil, err
	}
	if p = clean(p); p == "" {
		return root, nil
	}
	return root.Open(n.ctx, p)
}

func (n *ntfsFS) list(p string) ([]Entry, error) {
	d, err := n.dir(p)
	if err != nil {
		return nil, fs.ErrNotExist
	}
	var out []Entry
	for _, fi := range parser.ListDir(n.ctx, d) {
		if ntfsHidden(clean(p), fi.Name) || fi.IsSlack {
			continue
		}
		en := Entry{Name: fi.Name, Type: "file", Size: fi.Size, ModTime: fi.Mtime, Mode: 0o644}
		if fi.IsDir {
			en.Type, en.Size, en.Mode = "dir", 0, fs.ModeDir|0o755
		}
		out = append(out, en)
	}
	return out, nil
}

func (n *ntfsFS) ReadDir(p string) (out []Entry, err error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	err = safely(func() error {
		out, err = n.list(p)
		return err
	})
	return out, err
}

func (n *ntfsFS) Stat(p string) (en Entry, err error) {
	if clean(p) == "" {
		return Entry{Name: "/", Type: "dir", Mode: fs.ModeDir | 0o755}, nil
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	err = safely(func() error {
		ents, err := n.list(path.Dir(clean(p)))
		if err != nil {
			return err
		}
		for _, e := range ents {
			if strings.EqualFold(e.Name, path.Base(clean(p))) {
				en = e
				return nil
			}
		}
		return fs.ErrNotExist
	})
	return en, err
}

func (n *ntfsFS) Open(p string) (io.ReaderAt, int64, error) {
	en, err := n.Stat(p)
	if err != nil {
		return nil, 0, err
	}
	if en.Type != "file" {
		return nil, 0, errors.New(p + " is not a regular file")
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	var data parser.RangeReaderAt
	err = safely(func() error {
		var err error
		data, err = parser.GetDataForPath(n.ctx, clean(p))
		return err
	})
	if err != nil {
		return nil, 0, err
	}
	return &ntfsFile{fs: n, r: data, size: en.Size}, en.Size, nil
}

type ntfsFile struct {
	fs   *ntfsFS
	r    io.ReaderAt
	size int64
}

func (f *ntfsFile) ReadAt(p []byte, off int64) (n int, err error) {
	if off >= f.size {
		return 0, io.EOF
	}
	if rest := f.size - off; int64(len(p)) > rest {
		p = p[:rest]
	}
	f.fs.mu.Lock()
	defer f.fs.mu.Unlock()
	err = safely(func() error {
		n, err = f.r.ReadAt(p, off)
		return err
	})
	if err == nil && off+int64(n) >= f.size {
		err = io.EOF
	}
	return n, err
}
