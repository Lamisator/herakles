package backend

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
	"sync"
)

// remoteFile is the subset of file operations shared by SFTP and SMB files.
type remoteFile interface {
	io.Reader
	io.Writer
	io.Seeker
	io.Closer
}

// remoteFS is the subset of operations shared by sftp.Client and smb2.Share.
type remoteFS interface {
	MkdirAll(p string) error
	Create(p string) (remoteFile, error)
	Open(p string) (remoteFile, error)
	Stat(p string) (os.FileInfo, error)
	Rename(oldp, newp string) error
	Remove(p string) error
	ReadDir(p string) ([]os.FileInfo, error)
}

// fsBackend implements Backend on top of a remoteFS.
type fsBackend struct {
	fs       remoteFS
	root     string
	location string
	closer   func() error
	sem      chan struct{}
	mu       sync.Mutex
	dirs     map[string]bool
}

func newFSBackend(rfs remoteFS, root, location string, closer func() error, conns int) *fsBackend {
	if conns <= 0 {
		conns = 4
	}
	return &fsBackend{fs: rfs, root: root, location: location, closer: closer, sem: make(chan struct{}, conns), dirs: map[string]bool{}}
}

func (b *fsBackend) acquire() func() {
	b.sem <- struct{}{}
	return func() { <-b.sem }
}

func (b *fsBackend) path(h Handle) string {
	return path.Join(append([]string{b.root}, relPath(h)...)...)
}

func isNotExist(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, os.ErrNotExist)
}

func (b *fsBackend) Location() string { return b.location }

func (b *fsBackend) Create(ctx context.Context) error {
	defer b.acquire()()
	if _, err := b.fs.Stat(path.Join(b.root, "config")); err == nil {
		return fmt.Errorf("repository already exists at %s", b.location)
	}
	if err := b.fs.MkdirAll(b.root); err != nil {
		return err
	}
	for _, t := range AllTypes {
		if err := b.fs.MkdirAll(path.Join(b.root, string(t))); err != nil {
			return err
		}
	}
	return nil
}

func (b *fsBackend) ensureDir(d string) error {
	b.mu.Lock()
	ok := b.dirs[d]
	b.mu.Unlock()
	if ok {
		return nil
	}
	if err := b.fs.MkdirAll(d); err != nil {
		return err
	}
	b.mu.Lock()
	b.dirs[d] = true
	b.mu.Unlock()
	return nil
}

func (b *fsBackend) Save(ctx context.Context, h Handle, data []byte) error {
	if err := h.Valid(); err != nil {
		return err
	}
	defer b.acquire()()
	final := b.path(h)
	if err := b.ensureDir(path.Dir(final)); err != nil {
		return err
	}
	if h.Type != LockFile {
		if _, err := b.fs.Stat(final); err == nil {
			return fmt.Errorf("%v already exists", h)
		}
	}
	tmp := path.Join(path.Dir(final), ".tmp-"+randomSuffix())
	f, err := b.fs.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		_ = b.fs.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = b.fs.Remove(tmp)
		return err
	}
	if h.Type == LockFile {
		_ = b.fs.Remove(final)
	}
	if err := b.fs.Rename(tmp, final); err != nil {
		_ = b.fs.Remove(tmp)
		return err
	}
	return nil
}

type releaseOnClose struct {
	io.Reader
	f       io.Closer
	release func()
	once    sync.Once
}

func (r *releaseOnClose) Close() error {
	err := r.f.Close()
	r.once.Do(r.release)
	return err
}

func (b *fsBackend) Load(ctx context.Context, h Handle, offset, length int64) (io.ReadCloser, error) {
	if err := h.Valid(); err != nil {
		return nil, err
	}
	release := b.acquire()
	f, err := b.fs.Open(b.path(h))
	if err != nil {
		release()
		if isNotExist(err) {
			return nil, fmt.Errorf("%v: %w", h, ErrNotFound)
		}
		return nil, err
	}
	if offset > 0 {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			f.Close()
			release()
			return nil, err
		}
	}
	var rd io.Reader = f
	if length > 0 {
		rd = io.LimitReader(f, length)
	}
	return &releaseOnClose{Reader: rd, f: f, release: release}, nil
}

func (b *fsBackend) Stat(ctx context.Context, h Handle) (FileInfo, error) {
	if err := h.Valid(); err != nil {
		return FileInfo{}, err
	}
	defer b.acquire()()
	fi, err := b.fs.Stat(b.path(h))
	if err != nil {
		if isNotExist(err) {
			return FileInfo{}, fmt.Errorf("%v: %w", h, ErrNotFound)
		}
		return FileInfo{}, err
	}
	return FileInfo{Name: h.Name, Size: fi.Size()}, nil
}

func (b *fsBackend) Remove(ctx context.Context, h Handle) error {
	if err := h.Valid(); err != nil {
		return err
	}
	defer b.acquire()()
	err := b.fs.Remove(b.path(h))
	if err != nil && isNotExist(err) {
		return fmt.Errorf("%v: %w", h, ErrNotFound)
	}
	return err
}

func (b *fsBackend) List(ctx context.Context, t FileType, fn func(FileInfo) error) error {
	base := path.Join(b.root, string(t))
	readDir := func(d string) ([]os.FileInfo, error) {
		defer b.acquire()()
		entries, err := b.fs.ReadDir(d)
		if err != nil && isNotExist(err) {
			return nil, nil
		}
		return entries, err
	}
	entries, err := readDir(base)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if strings.HasPrefix(e.Name(), ".tmp-") {
			continue
		}
		if e.IsDir() {
			if t != PackFile {
				continue
			}
			sub, err := readDir(path.Join(base, e.Name()))
			if err != nil {
				return err
			}
			for _, s := range sub {
				if s.IsDir() || strings.HasPrefix(s.Name(), ".tmp-") {
					continue
				}
				if err := fn(FileInfo{Name: s.Name(), Size: s.Size()}); err != nil {
					return err
				}
			}
			continue
		}
		if err := fn(FileInfo{Name: e.Name(), Size: e.Size()}); err != nil {
			return err
		}
	}
	return nil
}

func (b *fsBackend) Close() error {
	if b.closer != nil {
		return b.closer()
	}
	return nil
}
