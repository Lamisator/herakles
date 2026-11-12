package imagefs

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"sync"
	"time"

	"github.com/diskfs/go-diskfs/backend"
	"github.com/diskfs/go-diskfs/filesystem/fat12"
	"github.com/diskfs/go-diskfs/filesystem/fat16"
	"github.com/diskfs/go-diskfs/filesystem/fat32"
)

// storage presents a read-only image section to go-diskfs.
type storage struct {
	r    io.ReaderAt
	size int64
	pos  int64
}

type storageInfo struct{ size int64 }

func (i storageInfo) Name() string       { return "image" }
func (i storageInfo) Size() int64        { return i.size }
func (i storageInfo) Mode() fs.FileMode  { return 0o400 }
func (i storageInfo) ModTime() time.Time { return time.Time{} }
func (i storageInfo) IsDir() bool        { return false }
func (i storageInfo) Sys() any           { return nil }

func (s *storage) Stat() (fs.FileInfo, error) { return storageInfo{s.size}, nil }
func (s *storage) Read(b []byte) (int, error) {
	n, err := s.r.ReadAt(b, s.pos)
	s.pos += int64(n)
	return n, err
}
func (s *storage) ReadAt(b []byte, off int64) (int, error) { return s.r.ReadAt(b, off) }
func (s *storage) Seek(off int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		s.pos = off
	case io.SeekCurrent:
		s.pos += off
	case io.SeekEnd:
		s.pos = s.size + off
	}
	return s.pos, nil
}
func (s *storage) Close() error                            { return nil }
func (s *storage) Sys() (*os.File, error)                  { return nil, backend.ErrNotSuitable }
func (s *storage) Writable() (backend.WritableFile, error) { return nil, backend.ErrIncorrectOpenMode }
func (s *storage) Path() string                            { return "" }

// stdFS adapts a file system with the io/fs interfaces (go-diskfs); calls
// are serialised.
type stdFS struct {
	mu   sync.Mutex
	fsys fs.FS
}

func openFAT(r io.ReaderAt, size int64, kind string) (FS, error) {
	st := &storage{r: r, size: size}
	var (
		fsys fs.FS
		err  error
	)
	switch kind {
	case "fat12":
		fsys, err = fat12.Read(st, size, 0, 0)
	case "fat16":
		fsys, err = fat16.Read(st, size, 0, 0)
	default:
		fsys, err = fat32.Read(st, size, 0, 0)
	}
	if err != nil {
		return nil, err
	}
	return &stdFS{fsys: fsys}, nil
}

func stdName(p string) string {
	if p = clean(p); p == "" {
		return "."
	}
	return p
}

func stdEntry(name string, info fs.FileInfo, dir bool) Entry {
	en := Entry{Name: name, Type: "file", Size: info.Size(), ModTime: info.ModTime(), Mode: 0o644}
	if dir || info.IsDir() {
		en.Type, en.Size, en.Mode = "dir", 0, fs.ModeDir|0o755
	}
	return en
}

func (s *stdFS) ReadDir(p string) (out []Entry, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	err = safely(func() error {
		ents, err := fs.ReadDir(s.fsys, stdName(p))
		if err != nil {
			return err
		}
		for _, d := range ents {
			if d.Name() == "." || d.Name() == ".." {
				continue
			}
			info, err := d.Info()
			if err != nil {
				continue
			}
			out = append(out, stdEntry(d.Name(), info, d.IsDir()))
		}
		return nil
	})
	return out, err
}

func (s *stdFS) Stat(p string) (en Entry, err error) {
	if clean(p) == "" {
		return Entry{Name: "/", Type: "dir", Mode: fs.ModeDir | 0o755}, nil
	}
	// Stat of go-diskfs FAT does not always say "directory": look the name
	// up in its parent.
	err = safely(func() error {
		ents, err := s.ReadDir(path.Dir(clean(p)))
		if err != nil {
			return err
		}
		for _, e := range ents {
			if e.Name == path.Base(clean(p)) {
				en = e
				return nil
			}
		}
		return fs.ErrNotExist
	})
	return en, err
}

func (s *stdFS) Open(p string) (io.ReaderAt, int64, error) {
	en, err := s.Stat(p)
	if err != nil {
		return nil, 0, err
	}
	if en.Type != "file" {
		return nil, 0, errors.New(p + " is not a regular file")
	}
	return &stdFile{fs: s, name: stdName(p)}, en.Size, nil
}

// stdFile reads a file of an io/fs file system at offsets (it reopens the
// file and skips ahead when it has no ReadAt of its own).
type stdFile struct {
	fs   *stdFS
	name string
}

func (f *stdFile) ReadAt(p []byte, off int64) (n int, err error) {
	f.fs.mu.Lock()
	defer f.fs.mu.Unlock()
	err = safely(func() error {
		h, err := f.fs.fsys.Open(f.name)
		if err != nil {
			return err
		}
		defer h.Close()
		if ra, ok := h.(io.ReaderAt); ok {
			n, err = ra.ReadAt(p, off)
			return err
		}
		if sk, ok := h.(io.Seeker); ok {
			if _, err := sk.Seek(off, io.SeekStart); err != nil {
				return err
			}
		} else if _, err := io.CopyN(io.Discard, h, off); err != nil {
			return err
		}
		n, err = io.ReadFull(h, p)
		if err == io.ErrUnexpectedEOF {
			err = io.EOF
		}
		return err
	})
	return n, err
}
