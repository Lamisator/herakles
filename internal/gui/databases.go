package gui

import (
	"context"
	"net/http"
	"time"

	"github.com/lamisator/herakles/internal/config"
	"github.com/lamisator/herakles/internal/dbdump"
)

func (s *Server) databaseRoutes(mux *http.ServeMux) {
	// Connects with the given settings and lists what would be dumped.
	mux.HandleFunc("POST /api/databases/test", func(w http.ResponseWriter, r *http.Request) {
		var d config.Database
		if err := decode(r, &d); err != nil {
			s.fail(w, err, "")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		dumps, err := dbdump.Plan(ctx, &d)
		if err != nil {
			s.fail(w, err, "")
			return
		}
		writeJSON(w, 200, map[string]any{"dumps": dumps})
	})
	// Loads a dump of a snapshot back into its database server.
	mux.HandleFunc("POST /api/repos/{name}/snapshots/{id}/database-restore", func(w http.ResponseWriter, r *http.Request) {
		repo, ok := s.open(w, r)
		if !ok {
			return
		}
		defer repo.Close()
		snap, ok := s.findSnap(w, r, repo)
		if !ok {
			return
		}
		var req struct {
			Path    string `json:"path"`
			Into    string `json:"into"`
			Replace bool   `json:"replace"`
		}
		if err := decode(r, &req); err != nil {
			s.fail(w, err, "")
			return
		}
		if err := s.eng.RestoreDatabase(r.Context(), repo, snap, req.Path, dbdump.RestoreOptions{Into: req.Into, Replace: req.Replace}); err != nil {
			s.fail(w, err, "")
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
}
