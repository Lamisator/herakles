// Package archiver creates snapshots of files, directories and block devices.
package archiver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lamisator/herakles/internal/chunker"
	"github.com/lamisator/herakles/internal/fsutil"
	"github.com/lamisator/herakles/internal/id"
	"github.com/lamisator/herakles/internal/repository"
)

// Version is recorded in snapshots.
var Version = "dev"

// Options configure a backup run.
type Options struct {
	Paths             []string // files and directories
	Devices           []string // block devices / partitions to image
	Excludes          []string
	ExcludeIfPresent  []string // e.g. ".nobackup"
	ExcludeCaches     bool     // skip dirs containing a valid CACHEDIR.TAG
	ExcludeLargerThan int64
	OneFileSystem     bool
	Type              string               // full, incremental, differential
	Parent            *repository.Snapshot // used for change detection (incremental/differential)
	Job               string
	Hostname          string
	Tags              []string
	FileWorkers       int
	BlobWorkers       int
	Progress          *Progress
	// OnError is called for non-fatal errors (unreadable files etc.).
	OnError func(path string, err error)
	// Time overrides the snapshot time (tests).
	Time time.Time
	// Docker is stored in the snapshot of a Docker project.
	Docker *repository.DockerInfo
}

// Archiver performs one backup.
type Archiver struct {
	repo   *repository.Repository
	opts   Options
	filter *Filter
	prog   *Progress

	fileSem  chan struct{}
	fileWG   sync.WaitGroup // in-flight file goroutines
	blobJobs chan blobJob
	blobWG   sync.WaitGroup
	chunkers sync.Pool

	sumMu   sync.Mutex
	summary repository.Summary
	devices map[string]uint64 // root path -> device for one_file_system
	fatal   atomic.Pointer[errBox]
}

// errBox lets errors of different concrete types share one atomic slot.
type errBox struct{ err error }

type blobJob struct {
	ctx  context.Context
	id   id.ID
	data []byte
	done func(err error)
}

// New creates an archiver.
func New(repo *repository.Repository, opts Options) (*Archiver, error) {
	f, err := NewFilter(opts.Excludes)
	if err != nil {
		return nil, err
	}
	if opts.FileWorkers <= 0 {
		opts.FileWorkers = 2
	}
	if opts.BlobWorkers <= 0 {
		opts.BlobWorkers = runtime.NumCPU()
	}
	if opts.Type == "" {
		opts.Type = repository.TypeIncremental
	}
	if opts.Hostname == "" {
		opts.Hostname, _ = os.Hostname()
	}
	if opts.Progress == nil {
		opts.Progress = &Progress{}
	}
	a := &Archiver{repo: repo, opts: opts, filter: f, prog: opts.Progress, devices: map[string]uint64{}}
	params, gear := repo.ChunkerParams(), repo.GearTable()
	a.chunkers.New = func() any { return chunker.New(nil, params, gear) }
	return a, nil
}

func (a *Archiver) addError(path string, err error) {
	a.prog.errors.Add(1)
	a.sumMu.Lock()
	a.summary.Errors++
	if len(a.summary.ErrorSamples) < 50 {
		a.summary.ErrorSamples = append(a.summary.ErrorSamples, fmt.Sprintf("%s: %v", path, err))
	}
	a.sumMu.Unlock()
	if a.opts.OnError != nil {
		a.opts.OnError(path, err)
	}
}

func (a *Archiver) count(f func(s *repository.Summary)) {
	a.sumMu.Lock()
	f(&a.summary)
	a.sumMu.Unlock()
}

// vnode is a node of the virtual tree built from the source paths.
type vnode struct {
	children map[string]*vnode
	realPath string // filesystem path of this node
	source   bool   // the node is a backup source (back up everything below)
	device   bool   // the node is a device to image
}

// splitAbs splits an absolute path into components usable as snapshot names.
func splitAbs(p string) []string {
	vol := filepath.VolumeName(p)
	rest := strings.TrimPrefix(p, vol)
	var parts []string
	if vol != "" {
		v := strings.TrimSuffix(vol, ":")
		v = strings.Trim(strings.ReplaceAll(v, `\`, "/"), "/")
		for _, s := range strings.Split(v, "/") {
			if s != "" {
				parts = append(parts, s)
			}
		}
	}
	for _, s := range strings.FieldsFunc(rest, func(r rune) bool { return r == '/' || r == '\\' }) {
		if s != "" && s != "." {
			parts = append(parts, s)
		}
	}
	return parts
}

// prefixPath reconstructs the filesystem path for the first n components.
func prefixPath(abs string, parts []string, n int) string {
	vol := filepath.VolumeName(abs)
	if vol == "" {
		return string(filepath.Separator) + filepath.Join(parts[:n]...)
	}
	volParts := len(splitAbs(vol))
	if n <= volParts {
		return vol + string(filepath.Separator)
	}
	return vol + string(filepath.Separator) + filepath.Join(parts[volParts:n]...)
}

func (a *Archiver) buildVTree() (*vnode, []string, error) {
	root := &vnode{children: map[string]*vnode{}}
	var paths []string
	add := func(p string, device bool) error {
		abs, err := filepath.Abs(p)
		if err != nil {
			return err
		}
		if device {
			abs = p // keep device paths verbatim (e.g. \\.\PhysicalDrive0)
		}
		paths = append(paths, abs)
		parts := splitAbs(abs)
		if device && runtime.GOOS == "windows" && strings.HasPrefix(p, `\\.\`) {
			parts = []string{"devices", strings.TrimSuffix(strings.TrimPrefix(p, `\\.\`), ":")}
		}
		if len(parts) == 0 {
			return fmt.Errorf("cannot back up the filesystem root %q directly; list its subdirectories or use a device source", p)
		}
		cur := root
		for i, part := range parts {
			if cur.source {
				return nil // already covered by a parent source
			}
			nxt, ok := cur.children[part]
			if !ok {
				nxt = &vnode{children: map[string]*vnode{}, realPath: prefixPath(abs, parts, i+1)}
				if device && runtime.GOOS == "windows" {
					nxt.realPath = ""
				}
				cur.children[part] = nxt
			}
			cur = nxt
		}
		cur.source, cur.device, cur.realPath = true, device, abs
		if !device {
			cur.children = map[string]*vnode{} // a source covers everything below
		}
		return nil
	}
	// Sort so parents are added before children. A source that matches an
	// exclude pattern itself (e.g. a bind-mounted log directory of a
	// Docker project) is skipped like anything below a source.
	srcs := append([]string(nil), a.opts.Paths...)
	sort.Strings(srcs)
	for _, p := range srcs {
		if abs, err := filepath.Abs(p); err == nil && a.filter.Excluded(abs) {
			continue
		}
		if err := add(p, false); err != nil {
			return nil, nil, err
		}
	}
	for _, d := range a.opts.Devices {
		if err := add(d, true); err != nil {
			return nil, nil, err
		}
	}
	if len(paths) == 0 {
		return nil, nil, errors.New("nothing to back up: no paths or devices given")
	}
	return root, paths, nil
}

// Run performs the backup and saves the snapshot.
func (a *Archiver) Run(ctx context.Context) (*repository.Snapshot, error) {
	start := time.Now()
	a.prog.setStart(start)
	// The progress may be shared by several snapshots of one job run (Docker
	// projects): the summary only counts what this snapshot added.
	storedBefore, newBefore := a.prog.bytesStored.Load(), a.prog.bytesNew.Load()
	vroot, paths, err := a.buildVTree()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	go a.scan(ctx, vroot)

	a.fileSem = make(chan struct{}, a.opts.FileWorkers)
	a.blobJobs = make(chan blobJob, a.opts.BlobWorkers*2)
	for i := 0; i < a.opts.BlobWorkers; i++ {
		a.blobWG.Add(1)
		go a.blobWorker()
	}

	var parentRoot *id.ID
	if a.opts.Parent != nil && a.opts.Type != repository.TypeFull {
		t := a.opts.Parent.Tree
		parentRoot = &t
	}
	treeID, err := a.saveVDir(ctx, vroot, parentRoot)
	if err != nil {
		cancel() // stop file workers that are still running
	}
	// File workers may still send blobs; wait for them before closing.
	a.fileWG.Wait()
	close(a.blobJobs)
	a.blobWG.Wait()
	if err == nil {
		if f := a.fatal.Load(); f != nil {
			err = f.err
		}
	}
	if err != nil {
		return nil, err
	}
	if err := a.repo.Flush(ctx); err != nil {
		return nil, fmt.Errorf("flush: %w", err)
	}
	if a.summary.TotalFiles == 0 && a.summary.Errors > 0 && a.summary.Errors >= int64(len(paths)) {
		return nil, fmt.Errorf("backup failed: no files could be read (%d errors, first: %s)", a.summary.Errors, a.summary.ErrorSamples[0])
	}
	uname := ""
	if u, err := user.Current(); err == nil {
		uname = u.Username
	}
	snapTime := a.opts.Time
	if snapTime.IsZero() {
		snapTime = start
	}
	a.summary.Start, a.summary.End = start.UTC(), time.Now().UTC()
	a.summary.BytesStored = a.prog.bytesStored.Load() - storedBefore
	a.summary.BytesAdded = a.prog.bytesNew.Load() - newBefore
	sum := a.summary
	snap := &repository.Snapshot{
		Time: snapTime.UTC(), Tree: treeID, Paths: paths, Hostname: a.opts.Hostname, Username: uname,
		Tags: a.opts.Tags, Job: a.opts.Job, Type: a.opts.Type, Version: Version, Summary: &sum, Docker: a.opts.Docker,
	}
	if a.opts.Parent != nil && a.opts.Type != repository.TypeFull {
		p := a.opts.Parent.ID()
		snap.Parent = &p
	}
	if _, err := a.repo.SaveSnapshot(ctx, snap); err != nil {
		return nil, fmt.Errorf("save snapshot: %w", err)
	}
	return snap, nil
}

func (a *Archiver) blobWorker() {
	defer a.blobWG.Done()
	for j := range a.blobJobs {
		_, isNew, stored, err := a.repo.SaveBlobWithID(j.ctx, repository.DataBlob, j.id, j.data)
		if err == nil && isNew {
			a.prog.bytesNew.Add(int64(len(j.data)))
			a.prog.bytesStored.Add(int64(stored))
			a.count(func(s *repository.Summary) { s.BlobsAdded++ })
		}
		j.done(err)
	}
}

func (a *Archiver) loadParentTree(ctx context.Context, pid *id.ID) *repository.Tree {
	if pid == nil {
		return nil
	}
	t, err := a.repo.LoadTree(ctx, *pid)
	if err != nil {
		return nil // parent unusable: fall back to reading everything
	}
	return t
}

// saveVDir saves an intermediate directory of the virtual tree.
func (a *Archiver) saveVDir(ctx context.Context, v *vnode, parent *id.ID) (id.ID, error) {
	ptree := a.loadParentTree(ctx, parent)
	names := make([]string, 0, len(v.children))
	for n := range v.children {
		names = append(names, n)
	}
	sort.Strings(names)
	tree := &repository.Tree{}
	for _, name := range names {
		child := v.children[name]
		var pnode *repository.Node
		if ptree != nil {
			pnode = ptree.Find(name)
		}
		var node *repository.Node
		var err error
		switch {
		case child.device:
			node, err = a.saveDevice(ctx, child.realPath)
		case child.source:
			node, err = a.saveSource(ctx, child.realPath, pnode)
		default:
			node, err = a.intermediate(ctx, child, pnode)
		}
		if err != nil {
			if ctx.Err() != nil {
				return id.Null, ctx.Err()
			}
			if isFatal(err) {
				return id.Null, err
			}
			a.addError(child.realPath, err)
			continue
		}
		if node == nil {
			continue
		}
		node.Name = name
		tree.Nodes = append(tree.Nodes, node)
	}
	return a.repo.SaveTree(ctx, tree)
}

func (a *Archiver) intermediate(ctx context.Context, v *vnode, pnode *repository.Node) (*repository.Node, error) {
	var node *repository.Node
	if v.realPath != "" {
		if fi, err := os.Lstat(v.realPath); err == nil && fi.IsDir() {
			node = fsutil.NodeFromInfo(v.realPath, fi)
		}
	}
	if node == nil {
		node = &repository.Node{Type: repository.NodeDir, Mode: os.ModeDir | 0o755, ModTime: time.Now().UTC()}
	}
	node.Type = repository.NodeDir
	var parent *id.ID
	if pnode != nil && pnode.Type == repository.NodeDir {
		parent = pnode.Subtree
	}
	sub, err := a.saveVDir(ctx, v, parent)
	if err != nil {
		return nil, err
	}
	node.Subtree = &sub
	return node, nil
}

// fatalError marks errors that must abort the backup (repository failures).
type fatalError struct{ error }

func (f fatalError) Unwrap() error { return f.error }

func isFatal(err error) bool {
	var f fatalError
	return errors.As(err, &f)
}

func (a *Archiver) saveSource(ctx context.Context, path string, pnode *repository.Node) (*repository.Node, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if fi.IsDir() {
		a.devices[path] = fsutil.DeviceOf(fi)
		return a.saveDir(ctx, path, fi, pnode, fsutil.DeviceOf(fi))
	}
	return a.saveEntry(ctx, path, fi, pnode)
}

func (a *Archiver) saveEntry(ctx context.Context, path string, fi os.FileInfo, pnode *repository.Node) (*repository.Node, error) {
	if fi.Mode().IsRegular() {
		res := make(chan fileResult, 1)
		a.saveFileAsync(ctx, path, fi, pnode, res)
		r := <-res
		return r.node, r.err
	}
	n := fsutil.NodeFromInfo(path, fi)
	a.prog.filesDone.Add(1)
	return n, nil
}

type fileResult struct {
	node *repository.Node
	err  error
}

func (a *Archiver) dirExcluded(path string) bool {
	for _, marker := range a.opts.ExcludeIfPresent {
		if _, err := os.Lstat(filepath.Join(path, marker)); err == nil {
			return true
		}
	}
	if a.opts.ExcludeCaches {
		f, err := os.Open(filepath.Join(path, "CACHEDIR.TAG"))
		if err == nil {
			buf := make([]byte, 43)
			n, _ := io.ReadFull(f, buf)
			f.Close()
			if string(buf[:n]) == "Signature: 8a477f597d28d172789f06886806bc55" {
				return true
			}
		}
	}
	return false
}

func (a *Archiver) saveDir(ctx context.Context, path string, fi os.FileInfo, pnode *repository.Node, dev uint64) (*repository.Node, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	node := fsutil.NodeFromInfo(path, fi)
	a.prog.setCurrent(path)
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	var ptree *repository.Tree
	if pnode != nil && pnode.Type == repository.NodeDir {
		ptree = a.loadParentTree(ctx, pnode.Subtree)
	}
	type slot struct {
		node *repository.Node
		res  chan fileResult
		path string
	}
	slots := make([]slot, 0, len(entries))
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		cp := filepath.Join(path, e.Name())
		if a.filter.Excluded(cp) {
			continue
		}
		cfi, err := os.Lstat(cp)
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				a.addError(cp, err)
			}
			continue
		}
		var cpn *repository.Node
		if ptree != nil {
			cpn = ptree.Find(e.Name())
		}
		switch {
		case cfi.IsDir():
			if a.opts.OneFileSystem && dev != 0 && fsutil.DeviceOf(cfi) != dev {
				continue
			}
			if a.dirExcluded(cp) {
				continue
			}
			child, err := a.saveDir(ctx, cp, cfi, cpn, dev)
			if err != nil {
				if ctx.Err() != nil || isFatal(err) {
					return nil, err
				}
				a.addError(cp, err)
				continue
			}
			slots = append(slots, slot{node: child})
		case cfi.Mode().IsRegular():
			if a.opts.ExcludeLargerThan > 0 && cfi.Size() > a.opts.ExcludeLargerThan {
				continue
			}
			res := make(chan fileResult, 1)
			a.saveFileAsync(ctx, cp, cfi, cpn, res)
			slots = append(slots, slot{res: res, path: cp})
		default:
			n := fsutil.NodeFromInfo(cp, cfi)
			a.prog.filesDone.Add(1)
			slots = append(slots, slot{node: n})
		}
	}
	tree := &repository.Tree{Nodes: make([]*repository.Node, 0, len(slots))}
	var fatal error
	for _, s := range slots {
		if s.res != nil {
			r := <-s.res
			if r.err != nil {
				if isFatal(r.err) || ctx.Err() != nil {
					if fatal == nil {
						fatal = r.err
					}
					continue
				}
				a.addError(s.path, r.err)
				continue
			}
			s.node = r.node
		}
		tree.Nodes = append(tree.Nodes, s.node)
	}
	if fatal != nil {
		return nil, fatal
	}
	sub, err := a.repo.SaveTree(ctx, tree)
	if err != nil {
		return nil, fatalError{err}
	}
	node.Subtree = &sub
	switch {
	case pnode == nil:
		a.count(func(s *repository.Summary) { s.DirsNew++ })
	case pnode.Subtree != nil && *pnode.Subtree == sub:
		a.count(func(s *repository.Summary) { s.DirsUnmodified++ })
	default:
		a.count(func(s *repository.Summary) { s.DirsChanged++ })
	}
	return node, nil
}

func unchanged(n, p *repository.Node) bool {
	if p == nil || p.Type != repository.NodeFile {
		return false
	}
	if n.Size != p.Size || !n.ModTime.Equal(p.ModTime) {
		return false
	}
	if n.Inode != 0 && p.Inode != 0 && n.Inode != p.Inode {
		return false
	}
	if !n.ChangeTime.IsZero() && !p.ChangeTime.IsZero() && runtime.GOOS != "windows" && !n.ChangeTime.Equal(p.ChangeTime) {
		return false
	}
	return true
}

func (a *Archiver) saveFileAsync(ctx context.Context, path string, fi os.FileInfo, pnode *repository.Node, res chan<- fileResult) {
	node := fsutil.NodeFromInfo(path, fi)
	if unchanged(node, pnode) {
		ok := true
		for _, c := range pnode.Content {
			if !a.repo.HasBlob(c) {
				ok = false
				break
			}
		}
		if ok {
			node.Content = pnode.Content
			a.prog.filesDone.Add(1)
			a.prog.bytesDone.Add(int64(node.Size))
			a.count(func(s *repository.Summary) {
				s.FilesUnmodified++
				s.TotalFiles++
				s.TotalBytes += int64(node.Size)
			})
			res <- fileResult{node: node}
			return
		}
	}
	select {
	case a.fileSem <- struct{}{}:
	case <-ctx.Done():
		res <- fileResult{err: ctx.Err()}
		return
	}
	a.fileWG.Add(1)
	go func() {
		defer a.fileWG.Done()
		defer func() { <-a.fileSem }()
		a.prog.setCurrent(path)
		err := a.readContent(ctx, path, node, false)
		if err != nil {
			res <- fileResult{err: err}
			return
		}
		a.prog.filesDone.Add(1)
		a.count(func(s *repository.Summary) {
			if pnode == nil {
				s.FilesNew++
			} else {
				s.FilesChanged++
			}
			s.TotalFiles++
			s.TotalBytes += int64(node.Size)
		})
		res <- fileResult{node: node}
	}()
}

// readContent chunks a file or device and stores new blobs.
func (a *Archiver) readContent(ctx context.Context, path string, node *repository.Node, device bool) error {
	var f *os.File
	var err error
	if device {
		f, _, err = fsutil.OpenDevice(path)
	} else {
		f, err = openNoAtime(path)
	}
	if err != nil {
		return err
	}
	defer f.Close()
	var rd io.Reader = f
	if device {
		rd = fsutil.NewAlignedReader(f, 1<<20)
	}
	ch := a.chunkers.Get().(*chunker.Chunker)
	defer a.chunkers.Put(ch)
	ch.Reset(rd)

	var wg sync.WaitGroup
	var errMu sync.Mutex
	var firstErr error
	done := func(err error) {
		if err != nil {
			errMu.Lock()
			if firstErr == nil {
				firstErr = err
			}
			errMu.Unlock()
		}
		wg.Done()
	}
	var content []id.ID
	var size uint64
	for {
		if err := ctx.Err(); err != nil {
			wg.Wait()
			return err
		}
		buf, err := ch.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			wg.Wait()
			return err
		}
		bid := a.repo.BlobID(buf)
		content = append(content, bid)
		size += uint64(len(buf))
		a.prog.bytesDone.Add(int64(len(buf)))
		if a.repo.HasBlob(bid) {
			continue
		}
		wg.Add(1)
		select {
		case a.blobJobs <- blobJob{ctx: ctx, id: bid, data: append([]byte(nil), buf...), done: done}:
		case <-ctx.Done():
			wg.Done()
			wg.Wait()
			return ctx.Err()
		}
	}
	wg.Wait()
	if firstErr != nil {
		a.fatal.CompareAndSwap(nil, &errBox{firstErr})
		return fatalError{firstErr}
	}
	if !device && size != node.Size {
		// File changed while reading; record what we actually read.
		a.addError(path, fmt.Errorf("file changed during backup (size %d -> %d)", node.Size, size))
	}
	node.Size = size
	node.Content = content
	return nil
}

func (a *Archiver) saveDevice(ctx context.Context, path string) (*repository.Node, error) {
	f, size, err := fsutil.OpenDevice(path)
	if err != nil {
		return nil, err
	}
	f.Close()
	node := &repository.Node{
		Name: filepath.Base(path), Type: repository.NodeDevice, Mode: 0o600,
		ModTime: time.Now().UTC(), Size: uint64(size), Source: path,
	}
	a.prog.setCurrent(path)
	if err := a.readContent(ctx, path, node, true); err != nil {
		return nil, err
	}
	a.prog.filesDone.Add(1)
	a.count(func(s *repository.Summary) {
		s.FilesNew++
		s.TotalFiles++
		s.TotalBytes += int64(node.Size)
	})
	return node, nil
}

// scan walks the sources to estimate totals for progress reporting.
func (a *Archiver) scan(ctx context.Context, root *vnode) {
	defer a.prog.scanDone.Store(true)
	var walk func(v *vnode)
	walk = func(v *vnode) {
		for _, c := range v.children {
			if ctx.Err() != nil {
				return
			}
			switch {
			case c.device:
				if f, size, err := fsutil.OpenDevice(c.realPath); err == nil {
					f.Close()
					a.prog.filesTotal.Add(1)
					a.prog.bytesTotal.Add(size)
				}
			case c.source:
				a.scanPath(ctx, c.realPath)
			default:
				walk(c)
			}
		}
	}
	walk(root)
}

func (a *Archiver) scanPath(ctx context.Context, root string) {
	rootFI, err := os.Lstat(root)
	if err != nil {
		return
	}
	dev := fsutil.DeviceOf(rootFI)
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			return nil
		}
		if p != root && a.filter.Excluded(p) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if p != root {
				if a.dirExcluded(p) {
					return filepath.SkipDir
				}
				if a.opts.OneFileSystem && dev != 0 {
					if fi, err := d.Info(); err == nil && fsutil.DeviceOf(fi) != dev {
						return filepath.SkipDir
					}
				}
			}
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return nil
		}
		if fi.Mode().IsRegular() {
			if a.opts.ExcludeLargerThan > 0 && fi.Size() > a.opts.ExcludeLargerThan {
				return nil
			}
			a.prog.bytesTotal.Add(fi.Size())
		}
		a.prog.filesTotal.Add(1)
		return nil
	})
}
