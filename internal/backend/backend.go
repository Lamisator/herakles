// Package backend defines the storage abstraction for repositories and
// provides implementations for local paths (including mounted or UNC network
// shares), SFTP, SMB and the Herakles repository server.
package backend

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
)

// FileType is the category of a repository file.
type FileType string

// Repository file types.
const (
	ConfigFile FileType = "config"
	KeyFile    FileType = "keys"
	PackFile   FileType = "data"
	IndexFile  FileType = "index"
	SnapFile   FileType = "snapshots"
	LockFile   FileType = "locks"
	// BundleFile holds one backup each: its new data, the list of that
	// data and its snapshot.
	BundleFile FileType = "backups"
	// ForgetFile marks a snapshot inside a backup file as forgotten.
	ForgetFile FileType = "forgotten"
)

// AllTypes lists all directory-based file types.
var AllTypes = []FileType{KeyFile, PackFile, IndexFile, SnapFile, LockFile, BundleFile, ForgetFile}

// ValidType reports whether t is a known file type.
func ValidType(t FileType) bool {
	if t == ConfigFile {
		return true
	}
	for _, x := range AllTypes {
		if x == t {
			return true
		}
	}
	return false
}

// Handle names a file in the repository.
type Handle struct {
	Type FileType
	Name string
}

func (h Handle) String() string {
	if h.Type == ConfigFile {
		return "config"
	}
	return string(h.Type) + "/" + h.Name
}

// Valid checks the handle for path traversal and unknown types.
func (h Handle) Valid() error {
	if !ValidType(h.Type) {
		return fmt.Errorf("invalid file type %q", h.Type)
	}
	if h.Type == ConfigFile {
		return nil
	}
	if h.Name == "" || strings.ContainsAny(h.Name, `/\:`) || h.Name == "." || h.Name == ".." {
		return fmt.Errorf("invalid file name %q", h.Name)
	}
	return nil
}

// FileInfo describes a stored file.
type FileInfo struct {
	Name string
	Size int64
}

// ErrNotFound is returned (possibly wrapped) when a file does not exist.
var ErrNotFound = errors.New("file not found")

// IsNotFound reports whether err indicates a missing file.
func IsNotFound(err error) bool { return errors.Is(err, ErrNotFound) }

// Backend stores repository files.
type Backend interface {
	// Location returns a human readable location string.
	Location() string
	// Create prepares an empty repository location (directories etc.).
	Create(ctx context.Context) error
	// Save stores data under h atomically. Existing files are not overwritten
	// except for lock files.
	Save(ctx context.Context, h Handle, data []byte) error
	// SaveStream stores everything read from rd under h, atomically: the
	// file appears only once rd reported io.EOF. Any other error from rd
	// discards what was written.
	SaveStream(ctx context.Context, h Handle, rd io.Reader) error
	// Load returns a reader for length bytes at offset (length 0 = to end).
	Load(ctx context.Context, h Handle, offset, length int64) (io.ReadCloser, error)
	Stat(ctx context.Context, h Handle) (FileInfo, error)
	Remove(ctx context.Context, h Handle) error
	List(ctx context.Context, t FileType, fn func(FileInfo) error) error
	Close() error
}

// PartialRemover is implemented by backends that keep partially written
// files (of interrupted uploads) next to the complete ones.
type PartialRemover interface {
	// RemovePartials deletes the unfinished files of type t and returns
	// how many there were. Only call it while nothing writes.
	RemovePartials(ctx context.Context, t FileType) (int, error)
}

// LoadAll reads a whole file.
func LoadAll(ctx context.Context, b Backend, h Handle) ([]byte, error) {
	rd, err := b.Load(ctx, h, 0, 0)
	if err != nil {
		return nil, err
	}
	defer rd.Close()
	return io.ReadAll(rd)
}

// LoadRange reads exactly length bytes at offset.
func LoadRange(ctx context.Context, b Backend, h Handle, offset, length int64) ([]byte, error) {
	rd, err := b.Load(ctx, h, offset, length)
	if err != nil {
		return nil, err
	}
	defer rd.Close()
	buf := make([]byte, length)
	if _, err := io.ReadFull(rd, buf); err != nil {
		return nil, fmt.Errorf("read %v at %d+%d: %w", h, offset, length, err)
	}
	return buf, nil
}

// subdir returns the relative path of a handle, sharding packs into 256
// subdirectories.
func relPath(h Handle) []string {
	if h.Type == ConfigFile {
		return []string{"config"}
	}
	if h.Type == PackFile && len(h.Name) >= 2 {
		return []string{string(h.Type), h.Name[:2], h.Name}
	}
	return []string{string(h.Type), h.Name}
}
