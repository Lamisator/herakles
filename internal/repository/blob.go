package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/lamisator/herakles/internal/backend"
	"github.com/lamisator/herakles/internal/compress"
	"github.com/lamisator/herakles/internal/id"
)

// BlobID computes the content ID of plaintext data.
func (r *Repository) BlobID(data []byte) id.ID { return r.key.ID(data) }

// HasBlob reports whether a blob is stored or about to be stored where the
// running backup may use it: in its own file or its chain.
func (r *Repository) HasBlob(i id.ID) bool {
	if r.inScope(i) {
		return true
	}
	r.pmu.Lock()
	defer r.pmu.Unlock()
	return r.pending.Has(i)
}

// SaveBlob stores data (deduplicated) and returns its ID, whether it was
// new, and the number of bytes it occupies in the repository.
func (r *Repository) SaveBlob(ctx context.Context, t BlobType, data []byte) (id.ID, bool, int, error) {
	return r.SaveBlobWithID(ctx, t, r.key.ID(data), data)
}

// SaveBlobWithID is SaveBlob with a precomputed ID.
func (r *Repository) SaveBlobWithID(ctx context.Context, t BlobType, i id.ID, data []byte) (id.ID, bool, int, error) {
	if err := r.ensureWriter(ctx); err != nil {
		return i, false, 0, err
	}
	if r.inScope(i) {
		return i, false, 0, nil
	}
	r.pmu.Lock()
	if r.pending.Has(i) {
		r.pmu.Unlock()
		return i, false, 0, nil
	}
	r.pending.Insert(i)
	r.pmu.Unlock()

	ct := r.key.Seal(compress.Compress(r.opts.Compression, data))
	if err := r.addToPacker(ctx, t, i, ct, len(data)); err != nil {
		r.pmu.Lock()
		r.pending.Delete(i)
		r.pmu.Unlock()
		return i, false, 0, err
	}
	return i, true, len(ct), nil
}

// inScope reports whether a stored blob may be referenced by what is being
// written: anything in the index for older-format data, otherwise only the
// chain of the running backup.
func (r *Repository) inScope(i id.ID) bool {
	r.bmu.Lock()
	legacy, sc := r.legacy, r.scope
	r.bmu.Unlock()
	if legacy {
		return r.idx.Has(i)
	}
	return sc.Has(i)
}

// ensureWriter opens a backup file (a full backup) when blobs are saved
// without BeginBackup.
func (r *Repository) ensureWriter(ctx context.Context) error {
	r.bmu.Lock()
	defer r.bmu.Unlock()
	if r.legacy || r.bw != nil {
		return nil
	}
	return r.openBundleLocked(ctx, bundleName(time.Now(), "data", TypeFull), "", nil, id.Set{})
}

func (r *Repository) addToPacker(ctx context.Context, t BlobType, i id.ID, ct []byte, rawLen int) error {
	var p *packer
	select {
	case p = <-r.packers:
	case <-ctx.Done():
		return ctx.Err()
	}
	p.add(t, i, ct, rawLen)
	var err error
	if p.buf.Len() >= r.opts.PackSize {
		err = r.uploadPack(ctx, p)
		if err != nil {
			p.buf.Reset()
			p.entries = nil
		}
	}
	r.packers <- p
	return err
}

// Flush uploads all partially filled packs and writes pending index entries.
// It must be called after a series of SaveBlob calls and before referencing
// the blobs from a snapshot.
func (r *Repository) Flush(ctx context.Context) error {
	var firstErr error
	n := cap(r.packers)
	held := make([]*packer, 0, n)
	for i := 0; i < n; i++ {
		select {
		case p := <-r.packers:
			held = append(held, p)
		case <-ctx.Done():
			for _, p := range held {
				r.packers <- p
			}
			return ctx.Err()
		}
	}
	for _, p := range held {
		if err := r.uploadPack(ctx, p); err != nil && firstErr == nil {
			firstErr = err
		}
		r.packers <- p
	}
	if firstErr != nil {
		return firstErr
	}
	r.pmu.Lock()
	toWrite := r.unsaved
	r.unsaved = nil
	r.pmu.Unlock()
	if len(toWrite) > 0 {
		if _, err := r.writeIndex(ctx, toWrite); err != nil {
			return err
		}
	}
	return nil
}

// LoadBlob loads, decrypts, decompresses and verifies a blob.
func (r *Repository) LoadBlob(ctx context.Context, i id.ID) ([]byte, error) {
	loc, ok := r.idx.Lookup(i)
	if !ok {
		return nil, fmt.Errorf("blob %s not found in index", i.Short())
	}
	h, off := loc.handle()
	ct, err := backend.LoadRange(ctx, r.be, h, off, int64(loc.Length))
	if err != nil {
		return nil, err
	}
	return r.DecodeBlob(i, ct)
}

// DecodeBlob decrypts and verifies an encrypted blob.
func (r *Repository) DecodeBlob(i id.ID, ct []byte) ([]byte, error) {
	pt, err := r.key.Open(ct)
	if err != nil {
		return nil, fmt.Errorf("blob %s: %w", i.Short(), err)
	}
	data, err := compress.Decompress(pt)
	if err != nil {
		return nil, fmt.Errorf("blob %s: %w", i.Short(), err)
	}
	if r.key.ID(data) != i {
		return nil, fmt.Errorf("blob %s: content hash mismatch", i.Short())
	}
	return data, nil
}

// BlobSize returns the plaintext size of a blob.
func (r *Repository) BlobSize(i id.ID) (int64, bool) {
	loc, ok := r.idx.Lookup(i)
	return int64(loc.RawLength), ok
}
