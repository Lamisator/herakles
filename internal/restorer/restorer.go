// Package restorer restores snapshots to the filesystem or block devices and
// provides dump, find and diff helpers.
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
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/lamisator/herakles/internal/archiver"
	"github.com/lamisator/herakles/internal/fsutil"
	"github.com/lamisator/herakles/internal/id"
	"github.com/lamisator/herakles/internal/repository"
)

// Overwrite policies.
const (
	OverwriteAlways    = "always"
	OverwriteIfChanged = "if-changed"
	OverwriteNever     = "never"
)

// Options configure a restore.
type Options struct {
	Target    string
	Includes  []string // patterns on snapshot paths; empty = everything
	Excludes  []string
	Overwrite string
	Owner     bool // restore ownership (requires root)
	Verify    bool // re-read and verify restored files
	Workers   int
	DryRun    bool
	OnError   func(path string, err error)
	OnFile    func(path string)
	Progress  *Progress
}

// Progress tracks a restore.
type Progress struct {
	FilesTotal atomic.Int64
	BytesTotal atomic.Int64
	FilesDone  atomic.Int64
	BytesDone  atomic.Int64
	Skipped    atomic.Int64
	Errors     atomic.Int64
}

// Result summarises a restore.
type Result struct {
	Files    int64 `json:"files"`
	Dirs     int64 `json:"dirs"`
	Bytes    int64 `json:"bytes"`
	Skipped  int64 `json:"skipped"`
	Errors   int64 `json:"errors"`
	Verified int64 `json:"verified"`
}

type restorer struct {
	repo     *repository.Repository
	opts     Options
	include  *archiver.Filter
	exclude  *archiver.Filter
	res      Result
	mu       sync.Mutex
	dirs     []dirMeta
	created  map[string]bool
	zero     sync.Map // id.ID -> bool
	prog     *Progress
	jobs     chan fileJob
	wg       sync.WaitGroup
}

type dirMeta struct {
	path string
	node *repository.Node
}

type fileJob struct {
	path   string
	snPath string
	node   *repository.Node
}

// Restore restores the subtree at snapPath ("/" for everything) of snap into
// opts.Target.
func Restore(ctx context.Context, repo *repository.Repository, snap *repository.Snapshot, snapPath string, opts Options) (*Result, error) {
	if opts.Target == "" {
		return nil, errors.New("no restore target given")
	}
	if opts.Overwrite == "" {
		opts.Overwrite = OverwriteAlways
	}
	if opts.Workers <= 0 {
		opts.Workers = runtime.NumCPU()
	}
	if opts.Progress == nil {
		opts.Progress = &Progress{}
	}
	inc, err := archiver.NewFilter(opts.Includes)
	if err != nil {
		return nil, err
	}
	exc, err := archiver.NewFilter(opts.Excludes)
	if err != nil {
		return nil, err
	}
	r := &restorer{repo: repo, opts: opts, include: inc, exclude: exc, created: map[string]bool{}, prog: opts.Progress}
	node, err := repo.FindNode(ctx, snap.Tree, snapPath)
	if err != nil {
		return nil, err
	}
	target, err := filepath.Abs(opts.Target)
	if err != nil {
		return nil, err
	}
	snapPath = "/" + strings.Trim(filepath.ToSlash(snapPath), "/")
	if snapPath == "/" {
		snapPath = ""
	}
	// Pre-compute totals for progress reporting.
	r.estimate(ctx, node, snapPath)

	r.jobs = make(chan fileJob, opts.Workers*4)
	for i := 0; i < opts.Workers; i++ {
		r.wg.Add(1)
		go r.worker(ctx)
	}
	var walkErr error
	if node.Type == repository.NodeDir && node.Name == "/" {
		if !opts.DryRun {
			if err := os.MkdirAll(target, 0o700); err != nil {
				close(r.jobs)
				r.wg.Wait()
				return nil, err
			}
		}
		walkErr = r.restoreChildren(ctx, *node.Subtree, target, snapPath)
	} else {
		walkErr = r.restoreNode(ctx, node, filepath.Join(target, safeName(node.Name)), snapPath)
	}
	close(r.jobs)
	r.wg.Wait()
	if walkErr != nil {
		return &r.res, walkErr
	}
	// Directory metadata last, deepest first, so mtimes stick.
	sort.Slice(r.dirs, func(i, j int) bool { return len(r.dirs[i].path) > len(r.dirs[j].path) })
	if !opts.DryRun {
		for _, d := range r.dirs {
			if err := fsutil.RestoreMetadata(d.path, d.node, opts.Owner); err != nil {
				r.fail(d.path, err)
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return &r.res, err
	}
	return &r.res, nil
}

func safeName(n string) string {
	n = strings.ReplaceAll(n, "/", "_")
	if runtime.GOOS == "windows" {
		n = strings.NewReplacer(`\`, "_", ":", "_", "*", "_", "?", "_", `"`, "_", "<", "_", ">", "_", "|", "_").Replace(n)
	}
	if n == "" || n == "." || n == ".." {
		n = "_"
	}
	return n
}

func (r *restorer) fail(p string, err error) {
	r.mu.Lock()
	r.res.Errors++
	r.mu.Unlock()
	r.prog.Errors.Add(1)
	if r.opts.OnError != nil {
		r.opts.OnError(p, err)
	}
}

// selected reports whether a snapshot path should be restored and whether
// a directory needs to be descended into.
func (r *restorer) selected(snPath string, isDir bool) (restore, descend bool) {
	if r.exclude.Excluded(snPath) {
		return false, false
	}
	if r.include == nil || len(r.opts.Includes) == 0 {
		return true, true
	}
	// Matched if the path or any of its ancestors match.
	for p := snPath; p != "/" && p != "." && p != ""; p = path.Dir(p) {
		if r.include.Excluded(p) {
			return true, true
		}
	}
	return false, isDir
}

func (r *restorer) estimate(ctx context.Context, node *repository.Node, snPath string) {
	add := func(p string, n *repository.Node) {
		if ok, _ := r.selected(p, n.Type == repository.NodeDir); ok && n.Type != repository.NodeDir {
			r.prog.FilesTotal.Add(1)
			r.prog.BytesTotal.Add(int64(n.Size))
		}
	}
	if node.Type != repository.NodeDir {
		add(snPath, node)
		return
	}
	_ = r.repo.Walk(ctx, *node.Subtree, func(p string, n *repository.Node) error {
		full := snPath + p
		if _, d := r.selected(full, n.Type == repository.NodeDir); !d && n.Type == repository.NodeDir {
			return repository.SkipDir
		}
		add(full, n)
		return nil
	})
}

func (r *restorer) ensureDir(dir string) error {
	r.mu.Lock()
	ok := r.created[dir]
	r.mu.Unlock()
	if ok || r.opts.DryRun {
		return nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	r.mu.Lock()
	r.created[dir] = true
	r.mu.Unlock()
	return nil
}

func (r *restorer) restoreChildren(ctx context.Context, tid id.ID, dir, snDir string) error {
	tree, err := r.repo.LoadTree(ctx, tid)
	if err != nil {
		return err
	}
	for _, n := range tree.Nodes {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := r.restoreNode(ctx, n, filepath.Join(dir, safeName(n.Name)), snDir+"/"+n.Name); err != nil {
			return err
		}
	}
	return nil
}

func (r *restorer) restoreNode(ctx context.Context, n *repository.Node, target, snPath string) error {
	sel, descend := r.selected(snPath, n.Type == repository.NodeDir)
	if n.Type == repository.NodeDir {
		if !descend {
			return nil
		}
		if sel {
			if err := r.ensureDir(target); err != nil {
				r.fail(target, err)
				return nil
			}
			r.mu.Lock()
			r.dirs = append(r.dirs, dirMeta{target, n})
			r.res.Dirs++
			r.mu.Unlock()
		}
		if n.Subtree == nil {
			return nil
		}
		return r.restoreChildren(ctx, *n.Subtree, target, snPath)
	}
	if !sel {
		return nil
	}
	if err := r.ensureDir(filepath.Dir(target)); err != nil {
		r.fail(target, err)
		return nil
	}
	if r.opts.OnFile != nil {
		r.opts.OnFile(snPath)
	}
	if fi, err := os.Lstat(target); err == nil {
		skip := false
		switch r.opts.Overwrite {
		case OverwriteNever:
			skip = true
		case OverwriteIfChanged:
			skip = fi.Mode().IsRegular() && (n.Type == repository.NodeFile || n.Type == repository.NodeDevice) &&
				uint64(fi.Size()) == n.Size && fi.ModTime().Equal(n.ModTime)
		}
		if skip {
			r.prog.Skipped.Add(1)
			r.prog.FilesDone.Add(1)
			r.prog.BytesDone.Add(int64(n.Size))
			r.mu.Lock()
			r.res.Skipped++
			r.mu.Unlock()
			return nil
		}
		if !r.opts.DryRun && (n.Type != repository.NodeFile && n.Type != repository.NodeDevice || !fi.Mode().IsRegular()) {
			_ = os.Remove(target)
		}
	}
	switch n.Type {
	case repository.NodeFile, repository.NodeDevice:
		select {
		case r.jobs <- fileJob{path: target, snPath: snPath, node: n}:
		case <-ctx.Done():
			return ctx.Err()
		}
		return nil
	case repository.NodeSymlink:
		if !r.opts.DryRun {
			if err := os.Symlink(n.LinkTarget, target); err != nil {
				r.fail(target, err)
				return nil
			}
			_ = fsutil.RestoreMetadata(target, n, r.opts.Owner)
		}
	case repository.NodeFIFO, repository.NodeCharDev, repository.NodeBlkDev:
		if !r.opts.DryRun {
			if err := fsutil.CreateSpecial(target, n); err != nil {
				r.fail(target, err)
				return nil
			}
			_ = fsutil.RestoreMetadata(target, n, r.opts.Owner)
		}
	default:
		return nil
	}
	r.prog.FilesDone.Add(1)
	r.mu.Lock()
	r.res.Files++
	r.mu.Unlock()
	return nil
}

func (r *restorer) worker(ctx context.Context) {
	defer r.wg.Done()
	for j := range r.jobs {
		if ctx.Err() != nil {
			continue
		}
		if r.opts.DryRun {
			r.prog.FilesDone.Add(1)
			r.prog.BytesDone.Add(int64(j.node.Size))
			r.mu.Lock()
			r.res.Files++
			r.res.Bytes += int64(j.node.Size)
			r.mu.Unlock()
			continue
		}
		if err := r.restoreFile(ctx, j); err != nil {
			r.fail(j.path, err)
			continue
		}
		r.prog.FilesDone.Add(1)
		r.mu.Lock()
		r.res.Files++
		r.res.Bytes += int64(j.node.Size)
		r.mu.Unlock()
	}
}

func isZero(b []byte) bool {
	for len(b) >= 8 {
		if b[0]|b[1]|b[2]|b[3]|b[4]|b[5]|b[6]|b[7] != 0 {
			return false
		}
		b = b[8:]
	}
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}

func (r *restorer) restoreFile(ctx context.Context, j fileJob) error {
	f, err := os.OpenFile(j.path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if err := r.writeContent(ctx, f, j.node, true); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if r.opts.Verify {
		if err := r.verify(j.path, j.node); err != nil {
			return err
		}
		r.mu.Lock()
		r.res.Verified++
		r.mu.Unlock()
	}
	return fsutil.RestoreMetadata(j.path, j.node, r.opts.Owner)
}

// writeContent writes a node's content to w. With sparse, all-zero blobs are
// skipped by seeking, producing sparse files.
func (r *restorer) writeContent(ctx context.Context, f *os.File, n *repository.Node, sparse bool) error {
	var off int64
	for _, c := range n.Content {
		if err := ctx.Err(); err != nil {
			return err
		}
		if z, ok := r.zero.Load(c); ok && z.(bool) && sparse {
			sz, _ := r.repo.BlobSize(c)
			off += sz
			r.prog.BytesDone.Add(sz)
			continue
		}
		data, err := r.repo.LoadBlob(ctx, c)
		if err != nil {
			return err
		}
		z := isZero(data)
		r.zero.Store(c, z)
		if z && sparse {
			off += int64(len(data))
			r.prog.BytesDone.Add(int64(len(data)))
			continue
		}
		if _, err := f.WriteAt(data, off); err != nil {
			return err
		}
		off += int64(len(data))
		r.prog.BytesDone.Add(int64(len(data)))
	}
	if sparse {
		return f.Truncate(off)
	}
	return nil
}

func (r *restorer) verify(p string, n *repository.Node) error {
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	buf := make([]byte, 0, 1<<20)
	for i, c := range n.Content {
		sz, ok := r.repo.BlobSize(c)
		if !ok {
			return fmt.Errorf("verification failed: blob %s unknown", c.Short())
		}
		if int64(cap(buf)) < sz {
			buf = make([]byte, 0, sz)
		}
		b := buf[:sz]
		if _, err := io.ReadFull(f, b); err != nil {
			return fmt.Errorf("verification failed: restored file is truncated at chunk %d", i)
		}
		if r.repo.BlobID(b) != c {
			return fmt.Errorf("verification failed: content differs at chunk %d", i)
		}
	}
	if n, _ := f.Read(make([]byte, 1)); n != 0 {
		return fmt.Errorf("verification failed: restored file is longer than expected")
	}
	return nil
}

// RestoreDevice writes a device image node to a raw device or image file.
func RestoreDevice(ctx context.Context, repo *repository.Repository, n *repository.Node, target string, progress func(done, total int64)) error {
	if n.Type != repository.NodeDevice && n.Type != repository.NodeFile {
		return fmt.Errorf("%s is not a device image or file", n.Name)
	}
	f, err := fsutil.OpenDeviceForWrite(target)
	if err != nil {
		return err
	}
	var done int64
	for _, c := range n.Content {
		if err := ctx.Err(); err != nil {
			f.Close()
			return err
		}
		data, err := repo.LoadBlob(ctx, c)
		if err != nil {
			f.Close()
			return err
		}
		if _, err := f.WriteAt(data, done); err != nil {
			f.Close()
			return err
		}
		done += int64(len(data))
		if progress != nil {
			progress(done, int64(n.Size))
		}
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
