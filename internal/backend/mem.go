package backend

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sort"
	"sync"
)

// Mem is an in-memory backend, used for tests.
type Mem struct {
	mu    sync.Mutex
	files map[Handle][]byte
}

// NewMem creates an empty in-memory backend.
func NewMem() *Mem { return &Mem{files: map[Handle][]byte{}} }

// Location implements Backend.
func (m *Mem) Location() string { return "memory" }

// Create implements Backend.
func (m *Mem) Create(ctx context.Context) error { return nil }

// SaveStream implements Backend.
func (m *Mem) SaveStream(ctx context.Context, h Handle, rd io.Reader) error {
	data, err := io.ReadAll(ctxReader{ctx, rd})
	if err != nil {
		return err
	}
	return m.Save(ctx, h, data)
}

// Save implements Backend.
func (m *Mem) Save(ctx context.Context, h Handle, data []byte) error {
	if err := h.Valid(); err != nil {
		return err
	}
	if h.Type == ConfigFile {
		h.Name = ""
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.files[h]; ok && h.Type != LockFile {
		return fmt.Errorf("%v already exists", h)
	}
	m.files[h] = append([]byte(nil), data...)
	return nil
}

// Load implements Backend.
func (m *Mem) Load(ctx context.Context, h Handle, offset, length int64) (io.ReadCloser, error) {
	if h.Type == ConfigFile {
		h.Name = ""
	}
	m.mu.Lock()
	data, ok := m.files[h]
	m.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("%v: %w", h, ErrNotFound)
	}
	if offset > int64(len(data)) {
		return nil, fmt.Errorf("offset beyond end of %v", h)
	}
	data = data[offset:]
	if length > 0 && length < int64(len(data)) {
		data = data[:length]
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

// Stat implements Backend.
func (m *Mem) Stat(ctx context.Context, h Handle) (FileInfo, error) {
	if h.Type == ConfigFile {
		h.Name = ""
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	data, ok := m.files[h]
	if !ok {
		return FileInfo{}, fmt.Errorf("%v: %w", h, ErrNotFound)
	}
	return FileInfo{Name: h.Name, Size: int64(len(data))}, nil
}

// Remove implements Backend.
func (m *Mem) Remove(ctx context.Context, h Handle) error {
	if h.Type == ConfigFile {
		h.Name = ""
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.files[h]; !ok {
		return fmt.Errorf("%v: %w", h, ErrNotFound)
	}
	delete(m.files, h)
	return nil
}

// List implements Backend.
func (m *Mem) List(ctx context.Context, t FileType, fn func(FileInfo) error) error {
	m.mu.Lock()
	var infos []FileInfo
	for h, d := range m.files {
		if h.Type == t {
			infos = append(infos, FileInfo{Name: h.Name, Size: int64(len(d))})
		}
	}
	m.mu.Unlock()
	sort.Slice(infos, func(i, j int) bool { return infos[i].Name < infos[j].Name })
	for _, fi := range infos {
		if err := fn(fi); err != nil {
			return err
		}
	}
	return nil
}

// Close implements Backend.
func (m *Mem) Close() error { return nil }

// Corrupt flips a byte in a stored file (test helper).
func (m *Mem) Corrupt(h Handle, offset int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if d, ok := m.files[h]; ok && offset < len(d) {
		d[offset] ^= 0xff
	}
}
