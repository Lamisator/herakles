package repository

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/lamisator/herakles/internal/backend"
	"github.com/lamisator/herakles/internal/compress"
	"github.com/lamisator/herakles/internal/id"
)

// SaveUnpacked stores an object (snapshot, index, lock) as its own encrypted
// file. The file name is the SHA-256 of the stored bytes.
func (r *Repository) SaveUnpacked(ctx context.Context, t backend.FileType, obj any) (id.ID, error) {
	raw, err := json.Marshal(obj)
	if err != nil {
		return id.Null, err
	}
	ct := r.key.Seal(compress.Compress(r.opts.Compression, raw))
	name := id.Hash(ct)
	if err := r.be.Save(ctx, backend.Handle{Type: t, Name: name.String()}, ct); err != nil {
		return id.Null, err
	}
	return name, nil
}

// saveUnpackedAs stores obj under a fixed name (used for lock refresh).
func (r *Repository) saveUnpackedAs(ctx context.Context, t backend.FileType, name string, obj any) error {
	raw, err := json.Marshal(obj)
	if err != nil {
		return err
	}
	return r.be.Save(ctx, backend.Handle{Type: t, Name: name}, r.key.Seal(compress.Compress(r.opts.Compression, raw)))
}

// LoadUnpacked loads and decrypts a file stored with SaveUnpacked.
func (r *Repository) LoadUnpacked(ctx context.Context, t backend.FileType, name string, obj any) error {
	ct, err := backend.LoadAll(ctx, r.be, backend.Handle{Type: t, Name: name})
	if err != nil {
		return err
	}
	if t != backend.LockFile && id.Hash(ct).String() != name {
		return fmt.Errorf("%s/%s: content does not match its name (corrupted)", t, name)
	}
	if err := r.decodeUnpacked(ct, obj); err != nil {
		return fmt.Errorf("%s/%s: %w", t, name, err)
	}
	return nil
}

// ListNames returns all file names of a type.
func (r *Repository) ListNames(ctx context.Context, t backend.FileType) ([]string, error) {
	var out []string
	err := r.be.List(ctx, t, func(fi backend.FileInfo) error {
		out = append(out, fi.Name)
		return nil
	})
	return out, err
}
