package gui

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/lamisator/herakles/internal/imagefs"
	"github.com/lamisator/herakles/internal/repository"
	"github.com/lamisator/herakles/internal/restorer"
)

// Disk images in snapshots are browsed like folders: the image lists its
// partitions (or is the file system), and below them are the files. Opened
// images stay open for a while with their own repository handle, so
// clicking through folders does not re-read the partition table and
// file system metadata every time.

type cachedImage struct {
	repo *repository.Repository
	img  *restorer.Image
	used time.Time
}

type imageCache struct {
	mu    sync.Mutex
	items map[string]*cachedImage
	once  sync.Once
}

const (
	imageCacheSize = 4
	imageIdle      = 10 * time.Minute
)

func (c *imageCache) janitor(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			c.evict(0)
			return
		case <-t.C:
			c.evict(imageIdle)
		}
	}
}

// evict closes images unused for longer than idle (all with 0).
func (c *imageCache) evict(idle time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, it := range c.items {
		if idle == 0 || time.Since(it.used) > idle {
			it.repo.Close()
			delete(c.items, k)
		}
	}
}

// imageAt finds the disk image that path p of a snapshot leads into. ok is
// false when p is not inside an image (or is not one).
func (s *Server) imageAt(r *http.Request, repo *repository.Repository, snap *repository.Snapshot, p string) (img *restorer.Image, inner string, ok bool, err error) {
	dev, devPath, inner, ok, err := repo.FindImage(r.Context(), snap.Tree, p)
	if !ok || err != nil {
		return nil, "", false, err
	}
	name := r.PathValue("name")
	key := name + "\x00" + snap.ID().String() + "\x00" + devPath
	c := &s.images
	c.once.Do(func() {
		c.items = map[string]*cachedImage{}
		go c.janitor(s.ctx)
	})
	c.mu.Lock()
	if it, hit := c.items[key]; hit {
		it.used = time.Now()
		c.mu.Unlock()
		return it.img, inner, true, nil
	}
	c.mu.Unlock()

	// A handle of its own: the request's repository is closed afterwards.
	own, _, err := s.eng.OpenByName(context.WithoutCancel(r.Context()), name)
	if err != nil {
		return nil, "", false, err
	}
	img, err = restorer.OpenImage(context.WithoutCancel(r.Context()), own, dev, devPath)
	if err != nil {
		own.Close()
		return nil, "", false, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.items) >= imageCacheSize {
		var oldest string
		for k, it := range c.items {
			if oldest == "" || it.used.Before(c.items[oldest].used) {
				oldest = k
			}
		}
		c.items[oldest].repo.Close()
		delete(c.items, oldest)
	}
	c.items[key] = &cachedImage{repo: own, img: img, used: time.Now()}
	return img, inner, true, nil
}

// volumeEntry shows a partition of an image in a listing.
func volumeEntry(base string, v *imagefs.Volume) treeEntry {
	return treeEntry{Name: v.Name, Path: base + "/" + v.Name, Type: "volume", Size: uint64(v.Size), Label: restorer.Describe(v),
		Note: v.Note, Browsable: v.Browsable(), Image: true}
}

func imageEntry(base string, e imagefs.Entry) treeEntry {
	mode := e.Mode.String()
	return treeEntry{Name: e.Name, Path: base + "/" + e.Name, Type: e.Type, Size: uint64(max(e.Size, 0)), Mode: mode, MTime: e.ModTime, Link: e.Link, Image: true}
}

// imageTree lists a folder inside an image (or the image's partitions).
func (s *Server) imageTree(w http.ResponseWriter, img *restorer.Image, p, inner string) {
	base := "/" + strings.Trim(p, "/")
	vol, fsPath, err := img.Resolve(inner)
	if err != nil {
		s.fail(w, err, "")
		return
	}
	out := []treeEntry{}
	if vol == nil {
		for _, v := range img.Volumes {
			out = append(out, volumeEntry(base, v))
		}
		writeJSON(w, 200, out)
		return
	}
	if !vol.Browsable() {
		s.fail(w, fmt.Errorf("%s: %s", restorer.Describe(vol), vol.Note), "")
		return
	}
	ents, err := vol.FS.ReadDir(fsPath)
	if err != nil {
		s.fail(w, err, "")
		return
	}
	for _, e := range ents {
		out = append(out, imageEntry(base, e))
	}
	writeJSON(w, 200, out)
}

// imageFind searches names below a folder inside an image.
func imageFind(img *restorer.Image, p, inner, q string, glob bool, limit int) ([]treeEntry, bool, error) {
	vol, fsPath, err := img.Resolve(inner)
	if err != nil {
		return nil, false, err
	}
	if vol == nil || !vol.Browsable() {
		return nil, false, errors.New("open a partition to search in it")
	}
	out := []treeEntry{}
	base := "/" + strings.Trim(p, "/")
	visited := 0
	var walk func(dir, at string) bool
	walk = func(dir, at string) bool {
		ents, err := vol.FS.ReadDir(dir)
		if err != nil {
			return true
		}
		for _, e := range ents {
			if visited++; visited > 200000 {
				return false
			}
			name := strings.ToLower(e.Name)
			match := strings.Contains(name, q)
			if glob {
				match, _ = path.Match(q, name)
			}
			if match {
				if len(out) >= limit {
					return false
				}
				out = append(out, imageEntry(at, e))
			}
			if e.IsDir() && !walk(path.Join(dir, e.Name), at+"/"+e.Name) {
				return false
			}
		}
		return true
	}
	complete := walk(fsPath, base)
	return out, !complete, nil
}
