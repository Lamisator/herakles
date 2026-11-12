package gui

import (
	"crypto/ed25519"
	"net/http"
	"slices"
	"strings"

	"github.com/lamisator/herakles/internal/config"
	"github.com/lamisator/herakles/internal/hub"
)

// hubRoutes serve the attached servers in the hub's web interface.
func (s *Server) hubRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/hub", func(w http.ResponseWriter, r *http.Request) {
		servers, err := s.hub.Servers()
		if err != nil {
			s.fail(w, err, "")
			return
		}
		cfg := s.eng.Config()
		out := map[string]any{"url": cfg.Web.URL, "shared": nonNil(cfg.Hub.Repositories), "servers": nonNil(servers)}
		if key, err := s.hub.Store.Identity(); err == nil {
			out["hub_key"] = hub.KeyFingerprint(key.Public().(ed25519.PublicKey))
		}
		writeJSON(w, 200, out)
	})
	mux.HandleFunc("POST /api/hub/enroll", func(w http.ResponseWriter, r *http.Request) {
		if !requireAdmin(w, r) {
			return
		}
		var req struct{ Name string }
		if err := decode(r, &req); err != nil {
			s.fail(w, err, "")
			return
		}
		cfg := s.eng.Config()
		pin, err := hub.CertPin(config.ExpandHome(cfg.Web.TLSCert))
		if err != nil {
			s.fail(w, err, "")
			return
		}
		code, err := s.hub.Store.NewEnrollment(cfg.Web.URL, strings.TrimSpace(req.Name), hub.TokenTTL, pin)
		if err != nil {
			s.fail(w, err, "")
			return
		}
		text := code.Encode()
		writeJSON(w, 200, map[string]any{"code": text, "expires": code.ExpiresAt(), "command": hub.JoinCommand(text), "hub_key": hub.KeyFingerprint(code.HubKey)})
	})
	mux.HandleFunc("PUT /api/hub/servers/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !requireAdmin(w, r) {
			return
		}
		var req struct{ Name string }
		if err := decode(r, &req); err != nil {
			s.fail(w, err, "")
			return
		}
		if err := s.hub.Store.Rename(r.PathValue("id"), req.Name); err != nil {
			s.fail(w, err, "")
			return
		}
		writeJSON(w, 200, map[string]string{"status": "renamed"})
	})
	mux.HandleFunc("DELETE /api/hub/servers/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !requireAdmin(w, r) {
			return
		}
		if err := s.hub.Remove(r.PathValue("id")); err != nil {
			s.fail(w, err, "")
			return
		}
		writeJSON(w, 200, map[string]string{"status": "removed"})
	})
	mux.HandleFunc("PUT /api/hub/shared", func(w http.ResponseWriter, r *http.Request) {
		if !requireAdmin(w, r) {
			return
		}
		var req struct{ Repositories []string }
		if err := decode(r, &req); err != nil {
			s.fail(w, err, "")
			return
		}
		c := s.eng.Config().Clone()
		c.Hub.Repositories = slices.Compact(slices.Sorted(slices.Values(req.Repositories)))
		if len(c.Hub.Repositories) > 0 {
			// Check before saving that every password can be passed on.
			if _, err := hub.Shared(c); err != nil {
				s.fail(w, err, "")
				return
			}
		}
		if err := c.Validate(); err != nil {
			s.fail(w, err, "")
			return
		}
		if err := s.saveConfig(c); err != nil {
			s.fail(w, err, "")
			return
		}
		writeJSON(w, 200, map[string]any{"shared": nonNil(c.Hub.Repositories)})
	})
	for _, m := range []string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE"} {
		mux.HandleFunc(m+" /api/hub/servers/{id}/api/", s.hubProxy)
	}
}

// hubProxy forwards /api/hub/servers/ID/api/... to that server's /api/...
// with the same limits for plain users as on the hub itself.
func (s *Server) hubProxy(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	prefix := "/api/hub/servers/" + id
	path := strings.TrimPrefix(r.URL.EscapedPath(), prefix)
	if path == r.URL.EscapedPath() || !strings.HasPrefix(path, "/api/") {
		writeJSON(w, 404, apiError{Error: "not found"})
		return
	}
	u := userOf(r)
	if u == nil {
		writeJSON(w, 401, apiError{Error: "please sign in again"})
		return
	}
	inner := r.Clone(r.Context())
	inner.URL.Path = strings.TrimPrefix(r.URL.Path, prefix)
	if !u.IsAdmin() && adminOnly(inner) {
		writeJSON(w, 403, apiError{Error: "only administrators can do this"})
		return
	}
	s.hub.Proxy(w, r, id, path, u.Name, u.Role)
}

func requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	if u := userOf(r); u != nil && !u.IsAdmin() {
		writeJSON(w, 403, apiError{Error: "only administrators can do this"})
		return false
	}
	return true
}
