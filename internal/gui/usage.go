package gui

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/lamisator/herakles/internal/backend"
	"github.com/lamisator/herakles/internal/engine"
)

// repoUsage is how much a repository takes up on its storage and how full
// that storage is. It needs no password: the sizes of the repository's
// files are enough.
type repoUsage struct {
	Bytes      int64          `json:"bytes"`
	Files      int            `json:"files"`
	Space      *backend.Space `json:"space,omitempty"`
	SpaceError string         `json:"space_error,omitempty"`
	Error      string         `json:"error,omitempty"`
	Checked    time.Time      `json:"checked"`
}

type usageCache struct {
	mu    sync.Mutex
	items map[string]repoUsage
}

const usageTTL = 2 * time.Minute

func (s *Server) usage(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	rc, err := s.eng.RepoConfig(name)
	if err != nil || s.eng.Config().Repo(name) == nil {
		writeJSON(w, 404, apiError{Error: "unknown repository " + name})
		return
	}
	key := name + "\x00" + rc.Location
	s.usages.mu.Lock()
	if s.usages.items == nil {
		s.usages.items = map[string]repoUsage{}
	}
	u, ok := s.usages.items[key]
	s.usages.mu.Unlock()
	if ok && time.Since(u.Checked) < usageTTL && r.URL.Query().Get("refresh") == "" {
		writeJSON(w, 200, u)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	u = repoUsage{Checked: time.Now()}
	be, err := engine.OpenBackend(rc)
	if err != nil {
		u.Error = err.Error()
		writeJSON(w, 200, u)
		return
	}
	defer be.Close()
	for _, t := range backend.AllTypes {
		err := be.List(ctx, t, func(fi backend.FileInfo) error {
			u.Bytes += fi.Size
			u.Files++
			return nil
		})
		if err != nil {
			u.Error = err.Error()
			break
		}
	}
	if fi, err := be.Stat(ctx, backend.Handle{Type: backend.ConfigFile}); err == nil {
		u.Bytes += fi.Size
		u.Files++
	} else if u.Error == "" && u.Files == 0 {
		u.Error = "not initialized yet"
	}
	if sp, err := backend.SpaceOf(ctx, be); err == nil {
		u.Space = &sp
	} else {
		u.SpaceError = err.Error()
	}
	s.usages.mu.Lock()
	s.usages.items[key] = u
	s.usages.mu.Unlock()
	writeJSON(w, 200, u)
}
