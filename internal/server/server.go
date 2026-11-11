// Package server implements the Herakles repository server, an HTTP(S)
// service that stores encrypted repositories for many clients. The server
// never sees plaintext or keys.
package server

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/lamisator/herakles/internal/backend"
	"github.com/lamisator/herakles/internal/config"
)

// MaxObjectSize bounds uploads (packs are far smaller).
const MaxObjectSize = 1 << 30

var repoNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// Server serves repositories below a data directory.
type Server struct {
	cfg  config.Server
	log  *slog.Logger
	root string

	authMu    sync.Mutex
	authCache map[[32]byte]time.Time

	sizeMu sync.Mutex
	sizes  map[string]int64
	quota  int64
}

// New creates a server from configuration.
func New(cfg config.Server, log *slog.Logger) (*Server, error) {
	if cfg.DataDir == "" {
		return nil, errors.New("server: data_dir is required")
	}
	if !cfg.NoAuth && len(cfg.Users) == 0 {
		return nil, errors.New("server: no users configured (add server.users or set no_auth: true for trusted networks)")
	}
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	root := config.ExpandHome(cfg.DataDir)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	return &Server{cfg: cfg, log: log, root: root, authCache: map[[32]byte]time.Time{}, sizes: map[string]int64{}, quota: int64(cfg.QuotaGiB) << 30}, nil
}

// HashPassword creates a bcrypt hash for server.users[].password_hash.
func HashPassword(pw string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	return string(h), err
}

var (
	dummyOnce sync.Once
	dummy     []byte
)

func dummyHash() []byte {
	dummyOnce.Do(func() { dummy, _ = bcrypt.GenerateFromPassword([]byte("dummy"), bcrypt.DefaultCost) })
	return dummy
}

type ctxKey struct{}

func (s *Server) user(r *http.Request) *config.ServerUser {
	u, _ := r.Context().Value(ctxKey{}).(*config.ServerUser)
	return u
}

func (s *Server) authenticate(r *http.Request) (*config.ServerUser, bool) {
	if s.cfg.NoAuth {
		return nil, true
	}
	name, pw, ok := r.BasicAuth()
	if !ok {
		return nil, false
	}
	var user *config.ServerUser
	for _, u := range s.cfg.Users {
		if subtle.ConstantTimeCompare([]byte(u.Name), []byte(name)) == 1 {
			user = u
		}
	}
	if user == nil {
		// Spend comparable time to avoid user enumeration.
		_ = bcrypt.CompareHashAndPassword(dummyHash(), []byte(pw))
		return nil, false
	}
	key := sha256.Sum256([]byte(user.Name + "\x00" + pw + "\x00" + user.PasswordHash))
	s.authMu.Lock()
	t, cached := s.authCache[key]
	s.authMu.Unlock()
	if cached && time.Since(t) < 10*time.Minute {
		return user, true
	}
	if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(pw)) != nil {
		return nil, false
	}
	s.authMu.Lock()
	if len(s.authCache) > 10000 {
		s.authCache = map[[32]byte]time.Time{}
	}
	s.authCache[key] = time.Now()
	s.authMu.Unlock()
	return user, true
}

func (s *Server) allowed(u *config.ServerUser, repo string) bool {
	if u == nil {
		return s.cfg.NoAuth
	}
	pats := u.Repositories
	if len(pats) == 0 {
		pats = []string{u.Name, u.Name + "-*"}
	}
	for _, p := range pats {
		if ok, _ := path.Match(p, repo); ok {
			return true
		}
	}
	return false
}

func (s *Server) appendOnly(u *config.ServerUser) bool {
	return s.cfg.AppendOnly || (u != nil && u.AppendOnly)
}

// Handler returns the HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/_health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","service":"herakles"}`))
	})
	mux.HandleFunc("/", s.serve)
	return s.logRequests(mux)
}

type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (w *statusWriter) WriteHeader(c int) { w.status = c; w.ResponseWriter.WriteHeader(c) }
func (w *statusWriter) Write(b []byte) (int, error) {
	n, err := w.ResponseWriter.Write(b)
	w.bytes += int64(n)
	return n, err
}

func (s *Server) logRequests(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: 200}
		h.ServeHTTP(sw, r)
		lvl := slog.LevelDebug
		if sw.status >= 400 {
			lvl = slog.LevelWarn
		}
		s.log.Log(r.Context(), lvl, "request", "method", r.Method, "path", r.URL.Path, "status", sw.status, "bytes", sw.bytes, "remote", r.RemoteAddr, "duration", time.Since(start))
	})
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	u, ok := s.authenticate(r)
	if !ok {
		w.Header().Set("WWW-Authenticate", `Basic realm="herakles"`)
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		http.Error(w, "repository name missing", http.StatusNotFound)
		return
	}
	repo := parts[0]
	if !repoNameRE.MatchString(repo) {
		http.Error(w, "invalid repository name", http.StatusBadRequest)
		return
	}
	if !s.allowed(u, repo) {
		http.Error(w, "access to repository denied", http.StatusForbidden)
		return
	}
	be := backend.NewLocal(filepath.Join(s.root, repo))
	ctx := context.WithValue(r.Context(), ctxKey{}, u)
	r = r.WithContext(ctx)
	switch {
	case len(parts) == 1:
		if r.Method == http.MethodPost && r.URL.Query().Get("create") == "true" {
			if err := be.Create(r.Context()); err != nil {
				http.Error(w, err.Error(), http.StatusConflict)
				return
			}
			s.log.Info("repository created", "repo", repo, "user", userName(u))
			w.WriteHeader(http.StatusCreated)
			return
		}
		http.Error(w, "unsupported", http.StatusMethodNotAllowed)
	case len(parts) == 2 && parts[1] == "config":
		s.object(w, r, be, repo, backend.Handle{Type: backend.ConfigFile})
	case len(parts) == 2 || (len(parts) == 3 && parts[2] == ""):
		t := backend.FileType(parts[1])
		if !backend.ValidType(t) || t == backend.ConfigFile {
			http.Error(w, "invalid type", http.StatusNotFound)
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "unsupported", http.StatusMethodNotAllowed)
			return
		}
		s.list(w, r, be, t)
	case len(parts) == 3:
		h := backend.Handle{Type: backend.FileType(parts[1]), Name: parts[2]}
		if h.Type == backend.ConfigFile || h.Valid() != nil {
			http.Error(w, "invalid object", http.StatusNotFound)
			return
		}
		s.object(w, r, be, repo, h)
	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}

func userName(u *config.ServerUser) string {
	if u == nil {
		return "-"
	}
	return u.Name
}

func (s *Server) list(w http.ResponseWriter, r *http.Request, be *backend.Local, t backend.FileType) {
	if _, err := os.Stat(be.FilePath(backend.Handle{Type: backend.ConfigFile})); err != nil {
		http.Error(w, "repository not found", http.StatusNotFound)
		return
	}
	entries := []backend.ListEntry{}
	if err := be.List(r.Context(), t, func(fi backend.FileInfo) error {
		entries = append(entries, backend.ListEntry{Name: fi.Name, Size: fi.Size})
		return nil
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(entries)
}

func (s *Server) repoSize(repo string) int64 {
	s.sizeMu.Lock()
	defer s.sizeMu.Unlock()
	if sz, ok := s.sizes[repo]; ok {
		return sz
	}
	var total int64
	_ = filepath.WalkDir(filepath.Join(s.root, repo), func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if fi, err := d.Info(); err == nil {
				total += fi.Size()
			}
		}
		return nil
	})
	s.sizes[repo] = total
	return total
}

func (s *Server) addSize(repo string, delta int64) {
	s.sizeMu.Lock()
	if _, ok := s.sizes[repo]; ok {
		s.sizes[repo] += delta
	}
	s.sizeMu.Unlock()
}

func (s *Server) object(w http.ResponseWriter, r *http.Request, be *backend.Local, repo string, h backend.Handle) {
	u := s.user(r)
	p := be.FilePath(h)
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		f, err := os.Open(p)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer f.Close()
		fi, err := f.Stat()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		http.ServeContent(w, r, "", fi.ModTime(), f)
	case http.MethodPost, http.MethodPut:
		if h.Type != backend.ConfigFile {
			if _, err := os.Stat(be.FilePath(backend.Handle{Type: backend.ConfigFile})); err != nil && h.Type != backend.KeyFile {
				http.Error(w, "repository not found", http.StatusNotFound)
				return
			}
		}
		if r.ContentLength > MaxObjectSize {
			http.Error(w, "object too large", http.StatusRequestEntityTooLarge)
			return
		}
		if s.quota > 0 && h.Type != backend.LockFile && r.ContentLength > 0 && s.repoSize(repo)+r.ContentLength > s.quota {
			http.Error(w, "repository quota exceeded", http.StatusInsufficientStorage)
			return
		}
		data, err := io.ReadAll(io.LimitReader(r.Body, MaxObjectSize+1))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if len(data) > MaxObjectSize {
			http.Error(w, "object too large", http.StatusRequestEntityTooLarge)
			return
		}
		if s.quota > 0 && h.Type != backend.LockFile {
			if s.repoSize(repo)+int64(len(data)) > s.quota {
				http.Error(w, "repository quota exceeded", http.StatusInsufficientStorage)
				return
			}
		}
		if h.Type == backend.ConfigFile {
			if _, err := os.Stat(p); err == nil {
				http.Error(w, "config already exists", http.StatusForbidden)
				return
			}
		}
		if err := be.Save(r.Context(), h, data); err != nil {
			if strings.Contains(err.Error(), "already exists") {
				http.Error(w, err.Error(), http.StatusForbidden)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if h.Type != backend.LockFile {
			s.addSize(repo, int64(len(data)))
		}
		w.WriteHeader(http.StatusOK)
	case http.MethodDelete:
		if h.Type == backend.ConfigFile {
			http.Error(w, "the repository config cannot be deleted", http.StatusForbidden)
			return
		}
		if s.appendOnly(u) && h.Type != backend.LockFile {
			http.Error(w, "repository is append-only: deletion denied", http.StatusForbidden)
			return
		}
		var size int64
		if fi, err := os.Stat(p); err == nil {
			size = fi.Size()
		}
		if err := be.Remove(r.Context(), h); err != nil {
			if backend.IsNotFound(err) {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if h.Type != backend.LockFile {
			s.addSize(repo, -size)
		}
		w.WriteHeader(http.StatusOK)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// ListenAndServe runs the server until ctx is cancelled.
func (s *Server) ListenAndServe(ctx context.Context) error {
	addr := s.cfg.Listen
	if addr == "" {
		addr = ":8443"
	}
	srv := &http.Server{Addr: addr, Handler: s.Handler(), ReadHeaderTimeout: 30 * time.Second}
	errc := make(chan error, 1)
	go func() {
		if s.cfg.TLSCert != "" {
			errc <- srv.ListenAndServeTLS(config.ExpandHome(s.cfg.TLSCert), config.ExpandHome(s.cfg.TLSKey))
		} else {
			errc <- srv.ListenAndServe()
		}
	}()
	scheme := "http"
	if s.cfg.TLSCert != "" {
		scheme = "https"
	}
	s.log.Info("repository server listening", "addr", addr, "scheme", scheme, "data_dir", s.root, "append_only", s.cfg.AppendOnly)
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(sctx)
	}
}
