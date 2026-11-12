package backend

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Backup files start with BundleMagic and a plaintext header holding the
// repository config and its key envelopes (password-protected master key):
// a backup file copied anywhere can be opened with the password alone.
const BundleMagic = "HERAKLES-BACKUP1"

// BundleHeader is the plaintext header of a backup file.
type BundleHeader struct {
	Config json.RawMessage            `json:"config"`
	Keys   map[string]json.RawMessage `json:"keys"`
}

// EncodeBundleHeader returns magic, length and header as written at the
// start of a backup file.
func EncodeBundleHeader(h *BundleHeader) ([]byte, error) {
	raw, err := json.Marshal(h)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(BundleMagic)+4+len(raw))
	out = append(out, BundleMagic...)
	out = binary.LittleEndian.AppendUint32(out, uint32(len(raw)))
	return append(out, raw...), nil
}

// ReadBundleHeader reads the header of a backup file.
func ReadBundleHeader(r io.ReaderAt) (*BundleHeader, error) {
	head := make([]byte, len(BundleMagic)+4)
	if _, err := r.ReadAt(head, 0); err != nil {
		return nil, err
	}
	if string(head[:len(BundleMagic)]) != BundleMagic {
		return nil, errors.New("not a Herakles backup file")
	}
	n := binary.LittleEndian.Uint32(head[len(BundleMagic):])
	if n > 16<<20 {
		return nil, errors.New("invalid backup file header")
	}
	raw := make([]byte, n)
	if _, err := r.ReadAt(raw, int64(len(head))); err != nil {
		return nil, err
	}
	var h BundleHeader
	if err := json.Unmarshal(raw, &h); err != nil {
		return nil, fmt.Errorf("backup file header: %w", err)
	}
	return &h, nil
}

// Loose reads backup files that were copied out of a repository: a folder
// of .hbk files (or one file). Config and keys come from their headers.
// Nothing can be written except lock files, which stay in memory.
type Loose struct {
	dir   string
	files map[string]string // name -> path
	cfg   []byte
	keys  map[string][]byte
	mu    sync.Mutex
	locks map[string][]byte
}

// IsLoose reports whether path is a backup file or a folder with backup
// files but no repository config.
func IsLoose(path string) bool {
	fi, err := os.Stat(path)
	if err != nil {
		return false
	}
	if !fi.IsDir() {
		return strings.HasSuffix(strings.ToLower(path), ".hbk")
	}
	if _, err := os.Stat(filepath.Join(path, "config")); err == nil {
		return false
	}
	m, _ := filepath.Glob(filepath.Join(path, "*.hbk"))
	return len(m) > 0
}

// NewLoose opens a backup file or a folder of backup files.
func NewLoose(path string) (*Loose, error) {
	l := &Loose{dir: path, files: map[string]string{}, keys: map[string][]byte{}, locks: map[string][]byte{}}
	var paths []string
	if fi, err := os.Stat(path); err != nil {
		return nil, err
	} else if fi.IsDir() {
		paths, _ = filepath.Glob(filepath.Join(path, "*.hbk"))
	} else {
		paths = []string{path}
	}
	sort.Strings(paths)
	for _, p := range paths {
		f, err := os.Open(p)
		if err != nil {
			return nil, err
		}
		h, err := ReadBundleHeader(f)
		f.Close()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", filepath.Base(p), err)
		}
		if l.cfg == nil {
			l.cfg = h.Config
		} else if !sameRepository(l.cfg, h.Config) {
			return nil, fmt.Errorf("%s belongs to another repository than the other backup files", filepath.Base(p))
		}
		for k, v := range h.Keys {
			l.keys[k] = v
		}
		l.files[filepath.Base(p)] = p
	}
	if l.cfg == nil {
		return nil, fmt.Errorf("no backup files (*.hbk) in %s", path)
	}
	return l, nil
}

func sameRepository(a, b []byte) bool {
	var x, y struct {
		ID string `json:"id"`
	}
	return json.Unmarshal(a, &x) == nil && json.Unmarshal(b, &y) == nil && x.ID == y.ID
}

var errLooseReadOnly = errors.New("backup files outside a repository can only be read")

// Location implements Backend.
func (l *Loose) Location() string { return l.dir }

// Create implements Backend.
func (l *Loose) Create(ctx context.Context) error { return errLooseReadOnly }

// Save implements Backend.
func (l *Loose) Save(ctx context.Context, h Handle, data []byte) error {
	if h.Type != LockFile {
		return errLooseReadOnly
	}
	l.mu.Lock()
	l.locks[h.Name] = append([]byte(nil), data...)
	l.mu.Unlock()
	return nil
}

// SaveStream implements Backend.
func (l *Loose) SaveStream(ctx context.Context, h Handle, rd io.Reader) error {
	return errLooseReadOnly
}

func (l *Loose) content(h Handle) ([]byte, string, error) {
	switch h.Type {
	case ConfigFile:
		return l.cfg, "", nil
	case KeyFile:
		if k, ok := l.keys[h.Name]; ok {
			return k, "", nil
		}
	case LockFile:
		l.mu.Lock()
		defer l.mu.Unlock()
		if k, ok := l.locks[h.Name]; ok {
			return k, "", nil
		}
	case BundleFile:
		if p, ok := l.files[h.Name]; ok {
			return nil, p, nil
		}
	}
	return nil, "", fmt.Errorf("%v: %w", h, ErrNotFound)
}

// Load implements Backend.
func (l *Loose) Load(ctx context.Context, h Handle, offset, length int64) (io.ReadCloser, error) {
	data, p, err := l.content(h)
	if err != nil {
		return nil, err
	}
	if p == "" {
		if offset > int64(len(data)) {
			offset = int64(len(data))
		}
		data = data[offset:]
		if length > 0 && length < int64(len(data)) {
			data = data[:length]
		}
		return io.NopCloser(bytes.NewReader(data)), nil
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		f.Close()
		return nil, err
	}
	var rd io.Reader = f
	if length > 0 {
		rd = io.LimitReader(f, length)
	}
	return sectionReadCloser{rd, f}, nil
}

// Stat implements Backend.
func (l *Loose) Stat(ctx context.Context, h Handle) (FileInfo, error) {
	data, p, err := l.content(h)
	if err != nil {
		return FileInfo{}, err
	}
	if p == "" {
		return FileInfo{Name: h.Name, Size: int64(len(data))}, nil
	}
	fi, err := os.Stat(p)
	if err != nil {
		return FileInfo{}, err
	}
	return FileInfo{Name: h.Name, Size: fi.Size()}, nil
}

// Remove implements Backend.
func (l *Loose) Remove(ctx context.Context, h Handle) error {
	if h.Type != LockFile {
		return errLooseReadOnly
	}
	l.mu.Lock()
	delete(l.locks, h.Name)
	l.mu.Unlock()
	return nil
}

// List implements Backend.
func (l *Loose) List(ctx context.Context, t FileType, fn func(FileInfo) error) error {
	var names []string
	switch t {
	case KeyFile:
		for k := range l.keys {
			names = append(names, k)
		}
	case BundleFile:
		for k := range l.files {
			names = append(names, k)
		}
	case LockFile:
		l.mu.Lock()
		for k := range l.locks {
			names = append(names, k)
		}
		l.mu.Unlock()
	}
	sort.Strings(names)
	for _, n := range names {
		fi, err := l.Stat(ctx, Handle{Type: t, Name: n})
		if err != nil {
			return err
		}
		if err := fn(fi); err != nil {
			return err
		}
	}
	return nil
}

// Close implements Backend.
func (l *Loose) Close() error { return nil }
