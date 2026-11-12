package repository

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/lamisator/herakles/internal/backend"
	"github.com/lamisator/herakles/internal/compress"
	"github.com/lamisator/herakles/internal/id"
)

// packer accumulates encrypted blobs until a pack is large enough.
type packer struct {
	buf     bytes.Buffer
	entries []PackEntry
}

func (p *packer) add(t BlobType, i id.ID, ct []byte, rawLen int) {
	p.entries = append(p.entries, PackEntry{ID: i, Type: t, Offset: uint32(p.buf.Len()), Length: uint32(len(ct)), RawLength: uint32(rawLen)})
	p.buf.Write(ct)
}

// finalize appends the encrypted header and trailer and returns the pack bytes.
func (r *Repository) finalize(p *packer) ([]byte, []PackEntry, error) {
	hdr, err := json.Marshal(p.entries)
	if err != nil {
		return nil, nil, err
	}
	enc := r.key.Seal(compress.Compress(compress.Mode{Algorithm: compress.Zstd, Level: 1, Auto: true}, hdr))
	p.buf.Write(enc)
	var trailer [4]byte
	binary.LittleEndian.PutUint32(trailer[:], uint32(len(enc)))
	p.buf.Write(trailer[:])
	data := p.buf.Bytes()
	entries := p.entries
	p.buf = bytes.Buffer{}
	p.entries = nil
	return data, entries, nil
}

// uploadPack finalizes and stores a pack, then registers it in the index.
func (r *Repository) uploadPack(ctx context.Context, p *packer) error {
	if len(p.entries) == 0 {
		return nil
	}
	data, entries, err := r.finalize(p)
	if err != nil {
		return err
	}
	packID := id.Hash(data)
	r.bmu.Lock()
	legacy := r.legacy
	r.bmu.Unlock()
	if !legacy {
		// The blobs stay pending until the backup file is complete.
		return r.writePack(data, PackInfo{ID: packID, Size: int64(len(data)), Blobs: entries})
	}
	if err := r.be.Save(ctx, backend.Handle{Type: backend.PackFile, Name: packID.String()}, data); err != nil {
		return fmt.Errorf("upload pack: %w", err)
	}
	info := PackInfo{ID: packID, Size: int64(len(data)), Blobs: entries}
	r.idx.AddPack(info)
	r.pmu.Lock()
	for _, e := range entries {
		r.pending.Delete(e.ID)
	}
	r.unsaved = append(r.unsaved, info)
	var toWrite []PackInfo
	if len(r.unsaved) >= 200 {
		toWrite, r.unsaved = r.unsaved, nil
	}
	r.pmu.Unlock()
	if toWrite != nil {
		// Periodic index writes keep interrupted backups resumable.
		if _, err := r.writeIndex(ctx, toWrite); err != nil {
			return err
		}
	}
	return nil
}

// ParsePackHeader reads the blob list from a pack's trailing header.
func (r *Repository) ParsePackHeader(data []byte) ([]PackEntry, error) {
	if len(data) < 4 {
		return nil, errors.New("pack too short")
	}
	hl := int(binary.LittleEndian.Uint32(data[len(data)-4:]))
	if hl+4 > len(data) {
		return nil, errors.New("invalid pack header length")
	}
	entries, err := r.decodeHeader(data[len(data)-4-hl : len(data)-4])
	if err != nil {
		return nil, err
	}
	end := uint64(len(data) - 4 - hl)
	for _, e := range entries {
		if uint64(e.Offset)+uint64(e.Length) > end {
			return nil, errors.New("pack header references data outside the pack")
		}
	}
	return entries, nil
}

func (r *Repository) decodeHeader(enc []byte) ([]PackEntry, error) {
	pt, err := r.key.Open(enc)
	if err != nil {
		return nil, fmt.Errorf("pack header: %w", err)
	}
	raw, err := compress.Decompress(pt)
	if err != nil {
		return nil, err
	}
	var entries []PackEntry
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, err
	}
	return entries, nil
}

// LoadPackHeader reads only the header of a stored pack.
func (r *Repository) LoadPackHeader(ctx context.Context, packID id.ID) ([]PackEntry, int64, error) {
	h := backend.Handle{Type: backend.PackFile, Name: packID.String()}
	fi, err := r.be.Stat(ctx, h)
	if err != nil {
		return nil, 0, err
	}
	if fi.Size < 4 {
		return nil, fi.Size, errors.New("pack too short")
	}
	// Read a generous tail in one request, fall back to the full header.
	tail := int64(64 << 10)
	if tail > fi.Size {
		tail = fi.Size
	}
	buf, err := backend.LoadRange(ctx, r.be, h, fi.Size-tail, tail)
	if err != nil {
		return nil, fi.Size, err
	}
	hl := int64(binary.LittleEndian.Uint32(buf[len(buf)-4:]))
	if hl+4 > fi.Size {
		return nil, fi.Size, errors.New("invalid pack header length")
	}
	if hl+4 > tail {
		buf, err = backend.LoadRange(ctx, r.be, h, fi.Size-hl-4, hl+4)
		if err != nil {
			return nil, fi.Size, err
		}
	}
	entries, err := r.decodeHeader(buf[len(buf)-int(hl)-4 : len(buf)-4])
	if err != nil {
		return nil, fi.Size, err
	}
	dataEnd := uint64(fi.Size - hl - 4)
	for _, e := range entries {
		if uint64(e.Offset)+uint64(e.Length) > dataEnd {
			return nil, fi.Size, errors.New("pack header references data outside the pack")
		}
	}
	return entries, fi.Size, nil
}
