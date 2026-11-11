// Package gui implements the graphical desktop interface: a local web
// application (embedded assets + JSON API) opened in an app window.
package gui

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/lamisator/herakles/internal/config"
	"github.com/lamisator/herakles/internal/crypto"
	"github.com/lamisator/herakles/internal/daemon"
	"github.com/lamisator/herakles/internal/engine"
	"github.com/lamisator/herakles/internal/repository"
	"github.com/lamisator/herakles/internal/restorer"
	"github.com/lamisator/herakles/internal/tray"
)

//go:embed static
var assets embed.FS

// Options configure the GUI server.
type Options struct {
	Listen      string
	OpenBrowser bool
	Version     string
	Out         io.Writer
	// Runner and Logs let the GUI share a daemon's task runner and log
	// buffer (tray mode). If nil, the GUI uses its own and discards logs.
	Runner *daemon.Runner
	Logs   *daemon.LogBuffer
}

// Server is the GUI backend.
type Server struct {
	eng     *engine.Engine
	runner  *daemon.Runner
	logs    *daemon.LogBuffer
	token   string
	version string
	ctx     context.Context
}

// NewServer creates the GUI backend.
func NewServer(ctx context.Context, eng *engine.Engine, version string) *Server {
	return newServer(ctx, eng, Options{Version: version})
}

func newServer(ctx context.Context, eng *engine.Engine, opts Options) *Server {
	tok := make([]byte, 24)
	_, _ = rand.Read(tok)
	logs := opts.Logs
	if logs == nil {
		logs = daemon.NewLogBuffer(1000)
		eng.Log = slog.New(daemon.NewTeeHandler(slog.NewTextHandler(io.Discard, nil), logs))
	}
	runner := opts.Runner
	if runner == nil {
		runner = daemon.NewRunner(ctx, eng, 2)
	}
	eng.Prompt = nil
	return &Server{eng: eng, runner: runner, logs: logs, token: hex.EncodeToString(tok), version: opts.Version, ctx: ctx}
}

// Token returns the session token.
func (s *Server) Token() string { return s.token }

// Run serves the GUI until ctx is cancelled.
func Run(ctx context.Context, eng *engine.Engine, opts Options) error {
	if opts.Out == nil {
		opts.Out = os.Stderr
	}
	inst, err := Start(ctx, eng, opts)
	if err != nil {
		return err
	}
	fmt.Fprintf(opts.Out, "Herakles GUI running at %s\nPress Ctrl+C to quit.\n", inst.URL)
	if opts.OpenBrowser {
		if err := OpenWindow(inst.URL); err != nil {
			fmt.Fprintf(opts.Out, "could not open a browser window (%v); open the URL above manually\n", err)
		}
	}
	<-ctx.Done()
	return inst.Shutdown()
}

// Instance is a running GUI server.
type Instance struct {
	// URL opens the GUI; it carries the session token.
	URL string
	srv *http.Server
}

// Shutdown stops the server, waiting up to five seconds for requests.
func (i *Instance) Shutdown() error {
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return i.srv.Shutdown(sctx)
}

// Start serves the GUI in the background. It only listens on loopback
// addresses; if the port is taken, a free one is chosen.
func Start(ctx context.Context, eng *engine.Engine, opts Options) (*Instance, error) {
	if opts.Listen == "" {
		opts.Listen = "127.0.0.1:7478"
	}
	host, _, err := net.SplitHostPort(opts.Listen)
	if err != nil {
		return nil, err
	}
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return nil, fmt.Errorf("the GUI only listens on loopback addresses (got %s)", opts.Listen)
	}
	ln, err := net.Listen("tcp", opts.Listen)
	if err != nil {
		// Port busy (e.g. a second instance): pick a free one.
		ln, err = net.Listen("tcp", net.JoinHostPort(host, "0"))
		if err != nil {
			return nil, err
		}
	}
	s := newServer(ctx, eng, opts)
	srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	return &Instance{URL: fmt.Sprintf("http://%s/?token=%s", ln.Addr().String(), s.token), srv: srv}, nil
}

// OpenWindow prefers a chromeless Chromium/Edge "app" window and falls back
// to the default browser.
func OpenWindow(url string) error {
	var candidates []string
	switch runtime.GOOS {
	case "windows":
		for _, base := range []string{os.Getenv("ProgramFiles(x86)"), os.Getenv("ProgramFiles"), os.Getenv("LocalAppData")} {
			if base == "" {
				continue
			}
			candidates = append(candidates,
				filepath.Join(base, `Microsoft\Edge\Application\msedge.exe`),
				filepath.Join(base, `Google\Chrome\Application\chrome.exe`))
		}
	case "darwin":
		candidates = []string{"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome", "/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge"}
	default:
		for _, n := range []string{"chromium", "chromium-browser", "google-chrome", "google-chrome-stable", "microsoft-edge", "brave-browser"} {
			if p, err := exec.LookPath(n); err == nil {
				candidates = append(candidates, p)
			}
		}
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err != nil {
			continue
		}
		if err := exec.Command(c, "--app="+url, "--window-size=1280,860").Start(); err == nil {
			return nil
		}
	}
	switch runtime.GOOS {
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	case "darwin":
		return exec.Command("open", url).Start()
	default:
		return exec.Command("xdg-open", url).Start()
	}
}

type apiError struct {
	Error         string `json:"error"`
	NeedsPassword bool   `json:"needs_password,omitempty"`
	Repo          string `json:"repo,omitempty"`
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) fail(w http.ResponseWriter, err error, repo string) {
	code := http.StatusBadRequest
	e := apiError{Error: err.Error()}
	if errors.Is(err, config.ErrNoPassword) || errors.Is(err, crypto.ErrWrongCredentials) {
		e.NeedsPassword, e.Repo = true, repo
		code = http.StatusUnauthorized
	}
	writeJSON(w, code, e)
}

func decode(r *http.Request, v any) error {
	return json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(v)
}

// Handler returns the HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	static, _ := fs.Sub(assets, "static")
	files := http.FileServer(http.FS(static))
	mux.Handle("GET /", files)

	mux.HandleFunc("GET /api/info", s.info)
	mux.HandleFunc("GET /api/config", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, s.eng.Config()) })
	mux.HandleFunc("PUT /api/config", s.putConfig)
	mux.HandleFunc("GET /api/config/yaml", s.getYAML)
	mux.HandleFunc("PUT /api/config/yaml", s.putYAML)
	mux.HandleFunc("GET /api/config/example", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]string{"yaml": config.Example})
	})
	mux.HandleFunc("GET /api/jobs", s.jobs)
	mux.HandleFunc("POST /api/jobs/{name}/run", s.runJob)
	mux.HandleFunc("GET /api/runs", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"active": nonNil(s.runner.Active()), "recent": nonNil(s.runner.Recent())})
	})
	mux.HandleFunc("POST /api/runs/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]bool{"cancelled": s.runner.Cancel(r.PathValue("id"))})
	})
	mux.HandleFunc("GET /api/history", func(w http.ResponseWriter, r *http.Request) {
		h, err := s.eng.History().Load()
		if err != nil {
			s.fail(w, err, "")
			return
		}
		writeJSON(w, 200, nonNil(h))
	})
	mux.HandleFunc("GET /api/logs", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, nonNil(s.logs.Lines())) })
	mux.HandleFunc("POST /api/repos/{name}/unlock", s.unlock)
	mux.HandleFunc("POST /api/repos/{name}/init", s.initRepo)
	mux.HandleFunc("POST /api/repos/{name}/task/{kind}", s.repoTask)
	mux.HandleFunc("GET /api/repos/{name}/stats", s.stats)
	mux.HandleFunc("GET /api/repos/{name}/snapshots", s.snapshots)
	mux.HandleFunc("DELETE /api/repos/{name}/snapshots/{id}", s.forget)
	mux.HandleFunc("GET /api/repos/{name}/snapshots/{id}/tree", s.tree)
	mux.HandleFunc("GET /api/repos/{name}/snapshots/{id}/download", s.download)
	mux.HandleFunc("POST /api/repos/{name}/snapshots/{id}/restore", s.restore)
	mux.HandleFunc("GET /api/repos/{name}/diff", s.diff)
	return s.guard(mux)
}

func nonNil[T any](v []T) []T {
	if v == nil {
		return []T{}
	}
	return v
}

// guard enforces loopback Host headers (DNS rebinding protection) and the
// session token (cookie after the first ?token= visit, or bearer header).
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		ok := false
		if t := r.URL.Query().Get("token"); t != "" && subtle.ConstantTimeCompare([]byte(t), []byte(s.token)) == 1 {
			http.SetCookie(w, &http.Cookie{Name: "herakles_session", Value: s.token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
			if r.URL.Path == "/" {
				http.Redirect(w, r, "/", http.StatusFound)
				return
			}
			ok = true
		}
		if c, err := r.Cookie("herakles_session"); err == nil && subtle.ConstantTimeCompare([]byte(c.Value), []byte(s.token)) == 1 {
			ok = true
		}
		if b := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "); b != "" && subtle.ConstantTimeCompare([]byte(b), []byte(s.token)) == 1 {
			ok = true
		}
		if !ok {
			if strings.HasPrefix(r.URL.Path, "/api/") {
				writeJSON(w, 401, apiError{Error: "session expired — reopen Herakles (herakles gui or the tray icon)"})
				return
			}
			http.Error(w, "Open Herakles from the tray icon or with the URL printed by 'herakles gui' (it contains the session token).", http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodGet && r.Header.Get("X-Herakles") != "1" {
			// Simple CSRF guard: browsers cannot add custom headers cross-site.
			writeJSON(w, 403, apiError{Error: "missing X-Herakles header"})
			return
		}
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) info(w http.ResponseWriter, r *http.Request) {
	host, _ := os.Hostname()
	out := map[string]any{"version": s.version, "config_path": s.eng.Config().Path(), "hostname": host, "os": runtime.GOOS}
	if cl, err := daemon.NewClient(s.eng.Config()); err == nil {
		if st, err := cl.Status(); err == nil {
			out["daemon"] = st
		}
	}
	writeJSON(w, 200, out)
}

func (s *Server) saveConfig(c *config.Config) error {
	if err := c.Save(); err != nil {
		return err
	}
	s.eng.SetConfig(c)
	tray.ApplyLogged(c, s.eng.Log)
	if cl, err := daemon.NewClient(c); err == nil {
		_ = cl.Reload()
	}
	return nil
}

func (s *Server) putConfig(w http.ResponseWriter, r *http.Request) {
	c := config.New(s.eng.Config().Path())
	if err := decode(r, c); err != nil {
		s.fail(w, err, "")
		return
	}
	c.SetPath(s.eng.Config().Path())
	if err := s.saveConfig(c); err != nil {
		s.fail(w, err, "")
		return
	}
	writeJSON(w, 200, c)
}

func (s *Server) getYAML(w http.ResponseWriter, r *http.Request) {
	p := s.eng.Config().Path()
	data, err := os.ReadFile(p)
	if err != nil {
		data, _ = s.eng.Config().Marshal()
	}
	writeJSON(w, 200, map[string]string{"path": p, "yaml": string(data)})
}

func (s *Server) putYAML(w http.ResponseWriter, r *http.Request) {
	var req struct{ YAML string }
	if err := decode(r, &req); err != nil {
		s.fail(w, err, "")
		return
	}
	c, err := config.Parse([]byte(req.YAML))
	if err != nil {
		s.fail(w, err, "")
		return
	}
	p := s.eng.Config().Path()
	c.SetPath(p)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		s.fail(w, err, "")
		return
	}
	if err := os.WriteFile(p, []byte(req.YAML), 0o600); err != nil {
		s.fail(w, err, "")
		return
	}
	s.eng.SetConfig(c)
	tray.ApplyLogged(c, s.eng.Log)
	if cl, err := daemon.NewClient(c); err == nil {
		_ = cl.Reload()
	}
	writeJSON(w, 200, map[string]string{"status": "saved"})
}

type jobView struct {
	*config.Job
	Last   *engine.JobResult `json:"last,omitempty"`
	Active bool              `json:"active"`
}

func (s *Server) jobs(w http.ResponseWriter, r *http.Request) {
	h := s.eng.History()
	out := []jobView{}
	for _, j := range s.eng.Config().Jobs {
		out = append(out, jobView{Job: j, Last: h.Last(j.Name), Active: s.runner.IsActive(daemon.KindBackup, j.Name)})
	}
	writeJSON(w, 200, out)
}

func (s *Server) runJob(w http.ResponseWriter, r *http.Request) {
	var req struct{ Type string }
	_ = decode(r, &req)
	name := r.PathValue("name")
	job := s.eng.Config().Job(name)
	if job == nil {
		s.fail(w, fmt.Errorf("unknown job %q", name), "")
		return
	}
	// Make sure credentials are available before queueing.
	if rc := s.eng.Config().Repo(job.Repository); rc != nil {
		if _, err := s.eng.Credentials(rc); err != nil {
			s.fail(w, err, rc.Name)
			return
		}
	}
	run, err := s.runner.Enqueue(daemon.KindBackup, name, req.Type, "manual")
	if err != nil {
		s.fail(w, err, "")
		return
	}
	v, _ := s.runner.Get(run.ID)
	writeJSON(w, 202, v)
}

func (s *Server) repoTask(w http.ResponseWriter, r *http.Request) {
	name, kind := r.PathValue("name"), r.PathValue("kind")
	if kind != daemon.KindCheck && kind != daemon.KindPrune {
		s.fail(w, fmt.Errorf("unknown task %q", kind), "")
		return
	}
	if rc := s.eng.Config().Repo(name); rc != nil {
		if _, err := s.eng.Credentials(rc); err != nil {
			s.fail(w, err, name)
			return
		}
	}
	run, err := s.runner.Enqueue(kind, name, "", "manual")
	if err != nil {
		s.fail(w, err, "")
		return
	}
	v, _ := s.runner.Get(run.ID)
	writeJSON(w, 202, v)
}

func (s *Server) unlock(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var req struct{ Password string }
	if err := decode(r, &req); err != nil {
		s.fail(w, err, name)
		return
	}
	rc, err := s.eng.RepoConfig(name)
	if err != nil {
		s.fail(w, err, name)
		return
	}
	creds := crypto.Credentials{Password: req.Password}
	if rc.KeyFile != "" {
		kf, err := crypto.LoadKeyFile(config.ExpandHome(rc.KeyFile))
		if err != nil {
			s.fail(w, err, name)
			return
		}
		creds.KeyFileData = kf
	}
	s.eng.RememberCredentials(name, creds)
	repo, err := s.eng.Open(r.Context(), rc)
	if err != nil {
		s.forgetCreds(name)
		s.fail(w, err, name)
		return
	}
	repo.Close()
	writeJSON(w, 200, map[string]string{"status": "unlocked"})
}

func (s *Server) forgetCreds(name string) {
	s.eng.ForgetCredentials(name)
}

func (s *Server) initRepo(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var req struct{ Password string }
	_ = decode(r, &req)
	rc := s.eng.Config().Repo(name)
	if rc == nil {
		s.fail(w, fmt.Errorf("unknown repository %q", name), "")
		return
	}
	creds, err := rc.Credentials()
	if errors.Is(err, config.ErrNoPassword) {
		if req.Password == "" {
			s.fail(w, err, name)
			return
		}
		creds, err = crypto.Credentials{Password: req.Password}, nil
		s.eng.RememberCredentials(name, creds)
	}
	if err != nil {
		s.fail(w, err, name)
		return
	}
	repo, err := s.eng.Init(r.Context(), rc, creds)
	if err != nil {
		s.fail(w, err, name)
		return
	}
	repo.Close()
	writeJSON(w, 200, map[string]string{"status": "created", "id": repo.Config().ID})
}

func (s *Server) open(w http.ResponseWriter, r *http.Request) (*repository.Repository, bool) {
	name := r.PathValue("name")
	rc, err := s.eng.RepoConfig(name)
	if err == nil && s.eng.Config().Repo(name) == nil {
		err = fmt.Errorf("unknown repository %q", name)
	}
	if err != nil {
		s.fail(w, err, name)
		return nil, false
	}
	repo, err := s.eng.Open(r.Context(), rc)
	if err != nil {
		if errors.Is(err, crypto.ErrWrongCredentials) {
			s.forgetCreds(name)
		}
		s.fail(w, err, name)
		return nil, false
	}
	return repo, true
}

func (s *Server) stats(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.open(w, r)
	if !ok {
		return
	}
	defer repo.Close()
	st, err := repo.Stats(r.Context())
	if err != nil {
		s.fail(w, err, "")
		return
	}
	writeJSON(w, 200, map[string]any{"stats": st, "id": repo.Config().ID, "location": repo.Backend().Location()})
}

type snapView struct {
	ID string `json:"id"`
	*repository.Snapshot
}

func (s *Server) snapshots(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.open(w, r)
	if !ok {
		return
	}
	defer repo.Close()
	all, err := repo.Snapshots(r.Context())
	if err != nil {
		s.fail(w, err, "")
		return
	}
	out := []snapView{}
	for i := len(all) - 1; i >= 0; i-- {
		out = append(out, snapView{all[i].ID().String(), all[i]})
	}
	writeJSON(w, 200, out)
}

func (s *Server) findSnap(w http.ResponseWriter, r *http.Request, repo *repository.Repository) (*repository.Snapshot, bool) {
	snap, err := repo.FindSnapshot(r.Context(), r.PathValue("id"), repository.SnapshotFilter{})
	if err != nil {
		s.fail(w, err, "")
		return nil, false
	}
	return snap, true
}

func (s *Server) forget(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.open(w, r)
	if !ok {
		return
	}
	defer repo.Close()
	snap, ok := s.findSnap(w, r, repo)
	if !ok {
		return
	}
	l, err := repo.Lock(r.Context(), false, "forget")
	if err != nil {
		s.fail(w, err, "")
		return
	}
	defer l.Unlock()
	if err := repo.RemoveSnapshot(r.Context(), snap.ID()); err != nil {
		s.fail(w, err, "")
		return
	}
	writeJSON(w, 200, map[string]string{"status": "forgotten"})
}

type treeEntry struct {
	Name   string    `json:"name"`
	Path   string    `json:"path"`
	Type   string    `json:"type"`
	Size   uint64    `json:"size"`
	Mode   string    `json:"mode"`
	MTime  time.Time `json:"mtime"`
	Link   string    `json:"link,omitempty"`
	Source string    `json:"source,omitempty"`
}

func (s *Server) tree(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.open(w, r)
	if !ok {
		return
	}
	defer repo.Close()
	snap, ok := s.findSnap(w, r, repo)
	if !ok {
		return
	}
	p := r.URL.Query().Get("path")
	node, err := repo.FindNode(r.Context(), snap.Tree, p)
	if err != nil {
		s.fail(w, err, "")
		return
	}
	if node.Type != repository.NodeDir || node.Subtree == nil {
		s.fail(w, fmt.Errorf("%s is not a directory", p), "")
		return
	}
	t, err := repo.LoadTree(r.Context(), *node.Subtree)
	if err != nil {
		s.fail(w, err, "")
		return
	}
	base := "/" + strings.Trim(p, "/")
	if base == "/" {
		base = ""
	}
	out := []treeEntry{}
	for _, n := range t.Nodes {
		out = append(out, treeEntry{Name: n.Name, Path: base + "/" + n.Name, Type: string(n.Type), Size: n.Size, Mode: n.Mode.String(), MTime: n.ModTime, Link: n.LinkTarget, Source: n.Source})
	}
	writeJSON(w, 200, out)
}

func (s *Server) download(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.open(w, r)
	if !ok {
		return
	}
	defer repo.Close()
	snap, ok := s.findSnap(w, r, repo)
	if !ok {
		return
	}
	node, err := repo.FindNode(r.Context(), snap.Tree, r.URL.Query().Get("path"))
	if err != nil {
		s.fail(w, err, "")
		return
	}
	name := node.Name
	if name == "/" {
		name = "snapshot-" + snap.ID().Short()
	}
	if node.Type == repository.NodeDir {
		w.Header().Set("Content-Type", "application/x-tar")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name+".tar"))
		_ = restorer.DumpTar(r.Context(), repo, node, w)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", fmt.Sprint(node.Size))
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name))
	_ = restorer.DumpFile(r.Context(), repo, node, w)
}

func (s *Server) restore(w http.ResponseWriter, r *http.Request) {
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
		Path      string   `json:"path"`
		Target    string   `json:"target"`
		Overwrite string   `json:"overwrite"`
		Verify    bool     `json:"verify"`
		Includes  []string `json:"includes"`
		Excludes  []string `json:"excludes"`
	}
	if err := decode(r, &req); err != nil {
		s.fail(w, err, "")
		return
	}
	if req.Path == "" {
		req.Path = "/"
	}
	var errs []string
	res, err := restorer.Restore(r.Context(), repo, snap, req.Path, restorer.Options{
		Target: config.ExpandHome(req.Target), Overwrite: req.Overwrite, Verify: req.Verify, Includes: req.Includes, Excludes: req.Excludes,
		OnError: func(p string, err error) {
			if len(errs) < 20 {
				errs = append(errs, p+": "+err.Error())
			}
		},
	})
	if err != nil {
		s.fail(w, err, "")
		return
	}
	writeJSON(w, 200, map[string]any{"result": res, "errors": nonNil(errs)})
}

func (s *Server) diff(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.open(w, r)
	if !ok {
		return
	}
	defer repo.Close()
	all, err := repo.Snapshots(r.Context())
	if err != nil {
		s.fail(w, err, "")
		return
	}
	a, err := repository.ResolveSnapshot(all, r.URL.Query().Get("a"), repository.SnapshotFilter{})
	if err != nil {
		s.fail(w, err, "")
		return
	}
	b, err := repository.ResolveSnapshot(all, r.URL.Query().Get("b"), repository.SnapshotFilter{})
	if err != nil {
		s.fail(w, err, "")
		return
	}
	changes, err := restorer.Diff(r.Context(), repo, a.Tree, b.Tree)
	if err != nil {
		s.fail(w, err, "")
		return
	}
	writeJSON(w, 200, nonNil(changes))
}
