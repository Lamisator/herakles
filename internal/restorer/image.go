package restorer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"sync"

	"github.com/lamisator/herakles/internal/id"
	"github.com/lamisator/herakles/internal/imagefs"
	"github.com/lamisator/herakles/internal/repository"
)

// nodeReader reads a file or disk image of a snapshot at any offset,
// loading and keeping a few of its chunks.
type nodeReader struct {
	ctx     context.Context
	repo    *repository.Repository
	chunks  []id.ID
	offsets []int64 // start of each chunk; offsets[len(chunks)] = size
	mu      sync.Mutex
	cache   map[int][]byte
	order   []int
}

const nodeCacheChunks = 48

// NodeReader returns random access to the content of a file or device node.
func NodeReader(ctx context.Context, repo *repository.Repository, n *repository.Node) (io.ReaderAt, int64, error) {
	r := &nodeReader{ctx: ctx, repo: repo, chunks: n.Content, offsets: make([]int64, len(n.Content)+1), cache: map[int][]byte{}}
	for i, c := range n.Content {
		size, ok := repo.BlobSize(c)
		if !ok {
			return nil, 0, fmt.Errorf("chunk %s of %s is missing from the repository index", c.Short(), n.Name)
		}
		r.offsets[i+1] = r.offsets[i] + size
	}
	return r, r.offsets[len(n.Content)], nil
}

func (r *nodeReader) chunk(i int) ([]byte, error) {
	r.mu.Lock()
	if b, ok := r.cache[i]; ok {
		r.mu.Unlock()
		return b, nil
	}
	r.mu.Unlock()
	b, err := r.repo.LoadBlob(r.ctx, r.chunks[i])
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.cache[i]; !ok {
		if len(r.order) >= nodeCacheChunks {
			delete(r.cache, r.order[0])
			r.order = r.order[1:]
		}
		r.cache[i] = b
		r.order = append(r.order, i)
	}
	return b, nil
}

func (r *nodeReader) ReadAt(p []byte, off int64) (int, error) {
	size := r.offsets[len(r.chunks)]
	if off < 0 {
		return 0, errors.New("negative offset")
	}
	if off >= size {
		return 0, io.EOF
	}
	done := 0
	for done < len(p) && off < size {
		i := sort.Search(len(r.chunks), func(i int) bool { return r.offsets[i+1] > off }) // chunk holding off
		b, err := r.chunk(i)
		if err != nil {
			return done, err
		}
		n := copy(p[done:], b[off-r.offsets[i]:])
		done += n
		off += int64(n)
	}
	if done < len(p) {
		return done, io.EOF
	}
	return done, nil
}

// Image is a disk or partition image of a snapshot, opened for reading the
// files inside it.
type Image struct {
	Node    *repository.Node
	Path    string // in the snapshot
	Volumes []*imagefs.Volume
}

// OpenImage opens the file systems of a device node.
func OpenImage(ctx context.Context, repo *repository.Repository, n *repository.Node, nodePath string) (*Image, error) {
	if n.Type != repository.NodeDevice {
		return nil, fmt.Errorf("%s is no disk image", nodePath)
	}
	r, size, err := NodeReader(ctx, repo, n)
	if err != nil {
		return nil, err
	}
	vols, err := imagefs.Open(r, size)
	if err != nil {
		return nil, err
	}
	return &Image{Node: n, Path: nodePath, Volumes: vols}, nil
}

// Single reports whether the image is one file system (shown directly,
// without a partition level).
func (im *Image) Single() bool { return len(im.Volumes) == 1 && im.Volumes[0].Name == "" }

// Resolve finds the volume and the path in its file system for a path
// inside the image ("" = the image). vol is nil for the image itself when
// it has partitions.
func (im *Image) Resolve(inner string) (vol *imagefs.Volume, fsPath string, err error) {
	inner = strings.Trim(inner, "/")
	if im.Single() {
		return im.Volumes[0], inner, nil
	}
	if inner == "" {
		return nil, "", nil
	}
	first, rest, _ := strings.Cut(inner, "/")
	for _, v := range im.Volumes {
		if v.Name == first {
			return v, rest, nil
		}
	}
	return nil, "", fmt.Errorf("%s: no such partition in %s", first, im.Path)
}

// Source resolves a path inside the image to a file, folder or whole file
// system (for downloads and restores).
func (im *Image) Source(inner string) (ImageSource, error) {
	vol, fsPath, err := im.Resolve(inner)
	if err != nil {
		return ImageSource{}, err
	}
	if vol == nil {
		return ImageSource{}, errors.New("choose a partition of the disk image, or use the image itself")
	}
	if !vol.Browsable() {
		return ImageSource{}, fmt.Errorf("%s: %s", Describe(vol), vol.Note)
	}
	if fsPath == "" {
		name := vol.Name
		if name == "" {
			name = path.Base(im.Path)
		}
		return ImageSource{FS: vol.FS, Entry: imagefs.Entry{Name: name, Type: "dir"}}, nil
	}
	e, err := vol.FS.Stat(fsPath)
	if err != nil {
		return ImageSource{}, fmt.Errorf("%s: %w", inner, err)
	}
	return ImageSource{FS: vol.FS, Path: fsPath, Entry: e}, nil
}

// Describe names a volume for people: "part2 · root · ext4 · 64 MiB".
func Describe(v *imagefs.Volume) string {
	var parts []string
	if v.Name != "" {
		parts = append(parts, v.Name)
	}
	for _, s := range []string{v.Part, v.Label, v.Type} {
		if s != "" && !contains(parts, s) {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, " · ")
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
