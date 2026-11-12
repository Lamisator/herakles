package restorer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/lamisator/herakles/internal/imagefs"
	"github.com/lamisator/herakles/internal/repository"
)

// ImageSource is a file, folder or whole file system inside a disk image.
type ImageSource struct {
	FS    imagefs.FS
	Path  string // in the file system; "" = its root
	Entry imagefs.Entry
}

// ImageEntryNode describes an entry of an image like a snapshot node.
func ImageEntryNode(e imagefs.Entry) *repository.Node {
	n := &repository.Node{Name: e.Name, Mode: e.Mode, ModTime: e.ModTime, Size: uint64(max(e.Size, 0)), LinkTarget: e.Link}
	switch e.Type {
	case "dir":
		n.Type = repository.NodeDir
	case "symlink":
		n.Type = repository.NodeSymlink
	case "file":
		n.Type = repository.NodeFile
	default:
		n.Type = repository.NodeFIFO // not copied
	}
	return n
}

// writeImage adds an image entry (folders recursively) to an archive.
func writeImage(ctx context.Context, sink entrySink, name string, src ImageSource) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	n := ImageEntryNode(src.Entry)
	content := func(w io.Writer) error {
		r, size, err := src.FS.Open(src.Path)
		if err != nil {
			return err
		}
		_, err = io.Copy(w, io.NewSectionReader(r, 0, size))
		return err
	}
	if name != "" {
		if err := sink.add(name, n, content); err != nil {
			return err
		}
	}
	if !src.Entry.IsDir() {
		return nil
	}
	ents, err := src.FS.ReadDir(src.Path)
	if err != nil {
		return err
	}
	for _, e := range ents {
		child := ImageSource{FS: src.FS, Path: path.Join(src.Path, e.Name), Entry: e}
		if err := writeImage(ctx, sink, path.Join(name, e.Name), child); err != nil {
			return err
		}
	}
	return nil
}

// RestoreImage copies a file, folder or file system from a disk image into
// opts.Target (a folder: the entry is created inside it, the contents of a
// file system root go directly into it). Ownership is not restored.
func RestoreImage(ctx context.Context, src ImageSource, opts Options) (*Result, error) {
	if opts.Target == "" {
		return nil, errors.New("no restore target given")
	}
	if opts.Overwrite == "" {
		opts.Overwrite = OverwriteAlways
	}
	target, err := filepath.Abs(opts.Target)
	if err != nil {
		return nil, err
	}
	res := &Result{}
	fail := func(p string, err error) {
		res.Errors++
		if opts.OnError != nil {
			opts.OnError(p, err)
		}
	}
	type dirTime struct {
		path string
		t    time.Time
	}
	var dirs []dirTime
	var restore func(src ImageSource, dst string) error
	restore = func(src ImageSource, dst string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		e := src.Entry
		switch e.Type {
		case "dir":
			if !opts.DryRun {
				if err := os.MkdirAll(dst, 0o755); err != nil {
					fail(dst, err)
					return nil
				}
			}
			res.Dirs++
			ents, err := src.FS.ReadDir(src.Path)
			if err != nil {
				fail(src.Path, err)
				return nil
			}
			for _, c := range ents {
				child := ImageSource{FS: src.FS, Path: path.Join(src.Path, c.Name), Entry: c}
				if err := restore(child, filepath.Join(dst, safeName(c.Name))); err != nil {
					return err
				}
			}
			if !e.ModTime.IsZero() {
				dirs = append(dirs, dirTime{dst, e.ModTime})
			}
		case "symlink":
			if opts.DryRun {
				return nil
			}
			if _, err := os.Lstat(dst); err == nil {
				if opts.Overwrite == OverwriteNever {
					res.Skipped++
					return nil
				}
				os.Remove(dst)
			}
			if err := os.Symlink(e.Link, dst); err != nil {
				fail(dst, err)
			}
		case "file":
			if st, err := os.Stat(dst); err == nil {
				if opts.Overwrite == OverwriteNever || opts.Overwrite == OverwriteIfChanged && st.Size() == e.Size && st.ModTime().Equal(e.ModTime) {
					res.Skipped++
					return nil
				}
			}
			if opts.DryRun {
				res.Files++
				return nil
			}
			n, err := copyImageFile(src, dst)
			if err != nil {
				fail(dst, err)
				return nil
			}
			res.Files++
			res.Bytes += n
		}
		return nil
	}
	dst := filepath.Join(target, safeName(src.Entry.Name))
	if src.Path == "" { // a whole file system: its contents go into target
		dst = target
	}
	if err := restore(src, dst); err != nil {
		return res, err
	}
	if !opts.DryRun {
		for i := len(dirs) - 1; i >= 0; i-- {
			_ = os.Chtimes(dirs[i].path, dirs[i].t, dirs[i].t)
		}
	}
	return res, nil
}

func copyImageFile(src ImageSource, dst string) (int64, error) {
	r, size, err := src.FS.Open(src.Path)
	if err != nil {
		return 0, err
	}
	mode := src.Entry.Mode.Perm()
	if mode == 0 {
		mode = 0o644
	}
	tmp := dst + ".herakles-tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(f, io.NewSectionReader(r, 0, size))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && n != size {
		err = fmt.Errorf("read %d of %d bytes", n, size)
	}
	if err == nil && runtime.GOOS == "windows" {
		os.Remove(dst)
	}
	if err == nil {
		err = os.Rename(tmp, dst)
	}
	if err != nil {
		os.Remove(tmp)
		return 0, err
	}
	if !src.Entry.ModTime.IsZero() {
		_ = os.Chtimes(dst, src.Entry.ModTime, src.Entry.ModTime)
	}
	return n, nil
}

// ImageName is the archive name of an image entry: its file name, or for
// a whole file system the volume's name.
func ImageName(volume, fsPath string) string {
	if fsPath = strings.Trim(fsPath, "/"); fsPath != "" {
		return path.Base(fsPath)
	}
	return volume
}
