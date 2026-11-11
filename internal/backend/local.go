package backend

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Local stores a repository in a directory. This covers local disks as well
// as mounted network shares (NFS, CIFS) and Windows UNC paths.
type Local struct {
	root string
}

// NewLocal returns a backend for directory root.
func NewLocal(root string) *Local { return &Local{root: filepath.Clean(root)} }

// Location implements Backend.
func (l *Local) Location() string { return l.root }

func (l *Local) path(h Handle) string {
	return filepath.Join(append([]string{l.root}, relPath(h)...)...)
}

// Create implements Backend.
func (l *Local) Create(ctx context.Context) error {
	if _, err := os.Stat(filepath.Join(l.root, "config")); err == nil {
		return fmt.Errorf("repository already exists at %s", l.root)
	}
	dirs := []string{l.root}
	for _, t := range AllTypes {
		dirs = append(dirs, filepath.Join(l.root, string(t)))
	}
	for i := 0; i < 256; i++ {
		dirs = append(dirs, filepath.Join(l.root, string(PackFile), fmt.Sprintf("%02x", i)))
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	return nil
}

// Save implements Backend.
func (l *Local) Save(ctx context.Context, h Handle, data []byte) error {
	if err := h.Valid(); err != nil {
		return err
	}
	final := l.path(h)
	if err := os.MkdirAll(filepath.Dir(final), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(final), ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { tmp.Close(); os.Remove(tmpName) }
	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if h.Type != LockFile {
		if _, err := os.Stat(final); err == nil {
			os.Remove(tmpName)
			return fmt.Errorf("%v already exists", h)
		}
	}
	if err := os.Rename(tmpName, final); err != nil {
		os.Remove(tmpName)
		return err
	}
	if h.Type != LockFile {
		_ = os.Chmod(final, 0o400)
	}
	return nil
}

type sectionReadCloser struct {
	io.Reader
	f *os.File
}

func (s sectionReadCloser) Close() error { return s.f.Close() }

// Load implements Backend.
func (l *Local) Load(ctx context.Context, h Handle, offset, length int64) (io.ReadCloser, error) {
	if err := h.Valid(); err != nil {
		return nil, err
	}
	f, err := os.Open(l.path(h))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%v: %w", h, ErrNotFound)
		}
		return nil, err
	}
	if offset > 0 {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			f.Close()
			return nil, err
		}
	}
	if length > 0 {
		return sectionReadCloser{io.LimitReader(f, length), f}, nil
	}
	return f, nil
}

// Stat implements Backend.
func (l *Local) Stat(ctx context.Context, h Handle) (FileInfo, error) {
	if err := h.Valid(); err != nil {
		return FileInfo{}, err
	}
	fi, err := os.Stat(l.path(h))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return FileInfo{}, fmt.Errorf("%v: %w", h, ErrNotFound)
		}
		return FileInfo{}, err
	}
	return FileInfo{Name: h.Name, Size: fi.Size()}, nil
}

// Remove implements Backend.
func (l *Local) Remove(ctx context.Context, h Handle) error {
	if err := h.Valid(); err != nil {
		return err
	}
	p := l.path(h)
	_ = os.Chmod(p, 0o600) // needed on Windows for read-only files
	err := os.Remove(p)
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%v: %w", h, ErrNotFound)
	}
	return err
}

// List implements Backend.
func (l *Local) List(ctx context.Context, t FileType, fn func(FileInfo) error) error {
	base := filepath.Join(l.root, string(t))
	visit := func(dir string) error {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		for _, e := range entries {
			if e.IsDir() || strings.HasPrefix(e.Name(), ".tmp-") {
				continue
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			info, err := e.Info()
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					continue
				}
				return err
			}
			if err := fn(FileInfo{Name: e.Name(), Size: info.Size()}); err != nil {
				return err
			}
		}
		return nil
	}
	if t != PackFile {
		return visit(base)
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		if e.IsDir() {
			if err := visit(filepath.Join(base, e.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

// Close implements Backend.
func (l *Local) Close() error { return nil }

// FilePath returns the filesystem path of a handle (used by the server).
func (l *Local) FilePath(h Handle) string { return l.path(h) }
