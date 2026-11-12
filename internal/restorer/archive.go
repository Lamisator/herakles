package restorer

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"io/fs"
	"path"
	"strings"

	"github.com/lamisator/herakles/internal/repository"
)

// Archive formats for downloads.
const (
	FormatTar   = "tar"
	FormatTarGz = "tar.gz"
	FormatZip   = "zip"
)

// ValidFormat reports whether f is an archive format.
func ValidFormat(f string) bool { return f == FormatTar || f == FormatTarGz || f == FormatZip }

// Item is one selected node and the name it gets in the archive.
type Item struct {
	Name string // path inside the archive, e.g. "docs" or "docs/a.txt"
	Node *repository.Node
	// Image, instead of Node, is an entry inside a disk image.
	Image *ImageSource
}

// entrySink receives archive entries.
type entrySink interface {
	add(name string, n *repository.Node, content func(io.Writer) error) error
	close() error
}

// WriteArchive writes the items (directories recursively) to w.
func WriteArchive(ctx context.Context, repo *repository.Repository, items []Item, format string, w io.Writer) error {
	var sink entrySink
	switch format {
	case FormatTar:
		sink = &tarSink{tw: tar.NewWriter(w)}
	case FormatTarGz:
		gz, _ := gzip.NewWriterLevel(w, gzip.BestSpeed)
		sink = &tarSink{tw: tar.NewWriter(gz), gz: gz}
	case FormatZip:
		sink = &zipSink{zw: zip.NewWriter(w)}
	default:
		return fmt.Errorf("unknown archive format %q (tar, tar.gz, zip)", format)
	}
	content := func(n *repository.Node) func(io.Writer) error {
		return func(w io.Writer) error { return DumpFile(ctx, repo, n, w) }
	}
	for _, it := range items {
		name := strings.TrimPrefix(path.Clean("/"+it.Name), "/")
		if it.Image != nil {
			if err := writeImage(ctx, sink, name, *it.Image); err != nil {
				return err
			}
			continue
		}
		if name != "" {
			if err := sink.add(name, it.Node, content(it.Node)); err != nil {
				return err
			}
		}
		if it.Node.Type == repository.NodeDir && it.Node.Subtree != nil {
			err := repo.Walk(ctx, *it.Node.Subtree, func(p string, c *repository.Node) error {
				return sink.add(path.Join(name, strings.TrimPrefix(p, "/")), c, content(c))
			})
			if err != nil {
				return err
			}
		}
	}
	return sink.close()
}

type tarSink struct {
	tw *tar.Writer
	gz *gzip.Writer
}

func (t *tarSink) add(name string, n *repository.Node, content func(io.Writer) error) error {
	hdr := &tar.Header{Name: name, Mode: int64(n.Mode.Perm()), ModTime: n.ModTime, Uid: int(n.UID), Gid: int(n.GID), Uname: n.User, Gname: n.Group}
	switch n.Type {
	case repository.NodeDir:
		hdr.Typeflag, hdr.Name = tar.TypeDir, name+"/"
	case repository.NodeSymlink:
		hdr.Typeflag, hdr.Linkname = tar.TypeSymlink, n.LinkTarget
	case repository.NodeFile, repository.NodeDevice:
		hdr.Typeflag, hdr.Size = tar.TypeReg, int64(n.Size)
	case repository.NodeFIFO:
		hdr.Typeflag = tar.TypeFifo
	default:
		return nil
	}
	if err := t.tw.WriteHeader(hdr); err != nil {
		return err
	}
	if hdr.Typeflag == tar.TypeReg {
		return content(t.tw)
	}
	return nil
}

func (t *tarSink) close() error {
	if err := t.tw.Close(); err != nil {
		return err
	}
	if t.gz != nil {
		return t.gz.Close()
	}
	return nil
}

type zipSink struct{ zw *zip.Writer }

// stored lists extensions of already compressed data: zip stores them
// without deflating again.
var stored = map[string]bool{".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".webp": true, ".heic": true, ".mp4": true,
	".mkv": true, ".mov": true, ".mp3": true, ".m4a": true, ".ogg": true, ".flac": true, ".zip": true, ".gz": true, ".tgz": true,
	".xz": true, ".zst": true, ".bz2": true, ".7z": true, ".rar": true, ".jar": true, ".docx": true, ".xlsx": true, ".pptx": true, ".pdf": true}

func (z *zipSink) add(name string, n *repository.Node, content func(io.Writer) error) error {
	h := &zip.FileHeader{Name: name, Modified: n.ModTime, Method: zip.Deflate}
	switch n.Type {
	case repository.NodeDir:
		h.Name, h.Method = name+"/", zip.Store
		h.SetMode(fs.ModeDir | n.Mode.Perm())
		_, err := z.zw.CreateHeader(h)
		return err
	case repository.NodeSymlink:
		h.Method = zip.Store
		h.SetMode(fs.ModeSymlink | 0o777)
		w, err := z.zw.CreateHeader(h)
		if err != nil {
			return err
		}
		_, err = io.WriteString(w, n.LinkTarget)
		return err
	case repository.NodeFile, repository.NodeDevice:
		if stored[strings.ToLower(path.Ext(name))] {
			h.Method = zip.Store
		}
		h.SetMode(n.Mode.Perm())
		h.UncompressedSize64 = n.Size
		w, err := z.zw.CreateHeader(h)
		if err != nil {
			return err
		}
		return content(w)
	}
	return nil
}

func (z *zipSink) close() error { return z.zw.Close() }

// DumpTar writes a node (file or directory) as a tar stream to w.
func DumpTar(ctx context.Context, repo *repository.Repository, n *repository.Node, w io.Writer) error {
	name := n.Name
	if name == "/" {
		name = ""
	}
	return WriteArchive(ctx, repo, []Item{{Name: name, Node: n}}, FormatTar, w)
}
