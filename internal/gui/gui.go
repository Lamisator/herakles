// Package gui implements the graphical desktop interface: a local web
// application (embedded assets + JSON API) opened in an app window.
package gui

import (
	"bytes"
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
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/lamisator/herakles/internal/accounts"
	"github.com/lamisator/herakles/internal/config"
	"github.com/lamisator/herakles/internal/crypto"
	"github.com/lamisator/herakles/internal/daemon"
	"github.com/lamisator/herakles/internal/engine"
	"github.com/lamisator/herakles/internal/hub"
	"github.com/lamisator/herakles/internal/repository"
	"github.com/lamisator/herakles/internal/restorer"
	"github.com/lamisator/herakles/internal/tray"
	"github.com/lamisator/herakles/internal/update"
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
	// Web serves the GUI on the network with user accounts instead of the
	// local session token.
	Web *WebOptions
	// TLSCert and TLSKey make the web mode serve HTTPS itself.
	TLSCert, TLSKey string
	// Service marks the GUI of the system service, shared with the local
	// users in gui.users (no tray of its own, see 'herakles daemon desktop').
	Service bool
	// Hub makes the web interface the hub that other servers attach to.
	Hub *hub.Hub
	// Agent reports the connection to this machine's hub, if attached.
	Agent func() *hub.AgentState
	// Update reports new Herakles releases; InstallUpdate (nil where this
	// window cannot update Herakles itself) installs one and restarts.
	Update        func() update.State
	InstallUpdate func() error
}

// Server is the GUI backend.
type Server struct {
	eng     *engine.Engine
	runner  *daemon.Runner
	logs    *daemon.LogBuffer
	token   string
	version string
	ctx     context.Context
	web     *webAuth
	service bool
	hub     *hub.Hub
	agent   func() *hub.AgentState
	upd     func() update.State
	install func() error
	images  imageCache
	usages  usageCache

	ticketMu sync.Mutex
	tickets  map[string]time.Time
}

// NewServer creates the GUI backend.
func NewServer(ctx context.Context, eng *engine.Engine, version string) *Server {
	s, _ := newServer(ctx, eng, Options{Version: version})
	return s
}

func newServer(ctx context.Context, eng *engine.Engine, opts Options) (*Server, error) {
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
	s := &Server{eng: eng, runner: runner, logs: logs, token: hex.EncodeToString(tok), version: opts.Version, ctx: ctx, service: opts.Service, hub: opts.Hub, agent: opts.Agent, upd: opts.Update, install: opts.InstallUpdate}
	if opts.Web != nil {
		if opts.Web.Log == nil {
			opts.Web.Log = eng.Log
		}
		wa, err := newWebAuth(*opts.Web)
		if err != nil {
			return nil, err
		}
		s.web = wa
	}
	return s, nil
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
	// Base is the address without the token, Token the session token
	// (local mode only).
	Base, Token string
	srv         *http.Server
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
	if opts.Web != nil {
		// Network mode: logins protect it; no fallback port.
		ln, err := net.Listen("tcp", opts.Listen)
		if err != nil {
			return nil, fmt.Errorf("web interface: %w", err)
		}
		s, err := newServer(ctx, eng, opts)
		if err != nil {
			ln.Close()
			return nil, err
		}
		srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
		if opts.TLSCert != "" {
			go func() { _ = srv.ServeTLS(ln, opts.TLSCert, opts.TLSKey) }()
		} else {
			go func() { _ = srv.Serve(ln) }()
		}
		return &Instance{URL: opts.Web.URL, srv: srv}, nil
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
	s, err := newServer(ctx, eng, opts)
	if err != nil {
		ln.Close()
		return nil, err
	}
	srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	base := "http://" + ln.Addr().String()
	return &Instance{URL: base + "/?token=" + s.token, Base: base, Token: s.token, srv: srv}, nil
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
	mux := s.apiMux()
	// Not part of apiMux: a hub must not re-attach a server it manages,
	// nor update it.
	s.agentRoutes(mux)
	mux.HandleFunc("POST /api/update/install", s.installUpdate)
	static, _ := fs.Sub(assets, "static")
	mux.Handle("GET /", http.FileServer(http.FS(static)))
	if s.web != nil {
		s.web.routes(mux)
		if s.hub != nil {
			s.hubRoutes(mux)
			guarded := s.web.guard(mux)
			agents := s.hub.Handler()
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Attached servers log in with their own credentials.
				if strings.HasPrefix(r.URL.Path, "/hub/") {
					agents.ServeHTTP(w, r)
					return
				}
				guarded.ServeHTTP(w, r)
			})
		}
		return s.web.guard(mux)
	}
	mux.HandleFunc("POST /api/ticket", s.ticket)
	return s.guard(mux)
}

// apiMux holds the JSON API, the same for every mode.
func (s *Server) apiMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/info", s.info)
	mux.HandleFunc("GET /api/config", func(w http.ResponseWriter, r *http.Request) {
		if u := userOf(r); u != nil && !u.IsAdmin() {
			writeJSON(w, 200, redactedConfig(s.eng.Config()))
			return
		}
		writeJSON(w, 200, s.eng.Config())
	})
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
	mux.HandleFunc("GET /api/repos/{name}/usage", s.usage)
	mux.HandleFunc("GET /api/repos/{name}/snapshots/{id}/tree", s.tree)
	mux.HandleFunc("GET /api/repos/{name}/snapshots/{id}/download", s.download)
	mux.HandleFunc("GET /api/repos/{name}/snapshots/{id}/preview", s.preview)
	mux.HandleFunc("GET /api/repos/{name}/snapshots/{id}/find", s.find)
	mux.HandleFunc("POST /api/repos/{name}/snapshots/{id}/restore", s.restore)
	mux.HandleFunc("GET /api/repos/{name}/diff", s.diff)
	s.dockerRoutes(mux)
	s.databaseRoutes(mux)
	s.wizardRoutes(mux)
	return mux
}

// NewRemote returns the API of this machine for its hub. The hub has
// checked the login; it says who is asking in headers, and plain users
// get the same limits as in the web interface.
func NewRemote(ctx context.Context, eng *engine.Engine, opts Options) http.Handler {
	opts.Web = nil
	s, _ := newServer(ctx, eng, opts)
	mux := s.apiMux()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		role := r.Header.Get(hub.RoleHeader)
		if role != accounts.RoleAdmin {
			role = accounts.RoleUser
		}
		u := &accounts.User{Name: r.Header.Get(hub.UserHeader), Role: role}
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			writeJSON(w, 404, apiError{Error: "not found"})
			return
		}
		if !u.IsAdmin() && adminOnly(r) {
			writeJSON(w, 403, apiError{Error: "only administrators can do this"})
			return
		}
		mux.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, u)))
	})
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
		if t := r.URL.Query().Get("ticket"); t != "" && r.URL.Path == "/" {
			if s.useTicket(t) {
				http.SetCookie(w, &http.Cookie{Name: "herakles_session", Value: s.token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
				http.Redirect(w, r, "/", http.StatusFound)
				return
			}
		}
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

// ticket hands out a one-time login for opening a browser window: the URL
// then carries a code that is useless once used or after a minute, not the
// session token, so it does not leak through the browser's command line.
func (s *Server) ticket(w http.ResponseWriter, r *http.Request) {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	t := hex.EncodeToString(b)
	s.ticketMu.Lock()
	if s.tickets == nil {
		s.tickets = map[string]time.Time{}
	}
	now := time.Now()
	for k, exp := range s.tickets {
		if now.After(exp) {
			delete(s.tickets, k)
		}
	}
	s.tickets[t] = now.Add(time.Minute)
	s.ticketMu.Unlock()
	writeJSON(w, 200, map[string]string{"ticket": t})
}

func (s *Server) useTicket(t string) bool {
	s.ticketMu.Lock()
	defer s.ticketMu.Unlock()
	exp, ok := s.tickets[t]
	delete(s.tickets, t)
	return ok && time.Now().Before(exp)
}

func (s *Server) info(w http.ResponseWriter, r *http.Request) {
	host, _ := os.Hostname()
	out := map[string]any{"version": s.version, "config_path": s.eng.Config().Path(), "hostname": host, "os": runtime.GOOS, "mode": "local"}
	if u := userOf(r); u != nil {
		out["mode"] = "web"
		out["user"] = viewOf(u)
	}
	if s.hub != nil {
		out["hub"] = true
	}
	if s.upd != nil {
		st := s.upd()
		out["update"] = map[string]any{"state": st, "can_install": s.install != nil}
	}
	if s.agent != nil {
		out["can_attach"] = true
		if st := s.agent(); st != nil {
			out["agent"] = st
		}
	}
	if s.service {
		out["service"] = map[string]any{"users": nonNil(s.eng.Config().GUI.Users)}
	}
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
	if !s.service {
		tray.ApplyLogged(c, s.eng.Log)
	}
	if cl, err := daemon.NewClient(c); err == nil {
		_ = cl.Reload()
	}
	if s.hub != nil {
		s.hub.PushAll()
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
	if s.hub != nil {
		s.hub.PushAll()
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
	// File is the backup file holding the snapshot; Chain lists the files
	// a restore needs (its own first), ChainBytes their size.
	File       string   `json:"file,omitempty"`
	Chain      []string `json:"chain,omitempty"`
	ChainBytes int64    `json:"chain_bytes,omitempty"`
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
	bundles, err := repo.Bundles(r.Context())
	if err != nil {
		s.fail(w, err, "")
		return
	}
	out := []snapView{}
	for i := len(all) - 1; i >= 0; i-- {
		v := snapView{ID: all[i].ID().String(), Snapshot: all[i], File: all[i].File()}
		chain, _ := repository.ChainIn(bundles, all[i])
		for _, b := range chain {
			v.Chain = append(v.Chain, b.Name)
			v.ChainBytes += b.Size
		}
		out = append(out, v)
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
	// Inside disk images: Image marks entries read from an image; volumes
	// (partitions) carry a description, and a note when not browsable.
	Image     bool   `json:"image,omitempty"`
	Label     string `json:"label,omitempty"`
	Note      string `json:"note,omitempty"`
	Browsable bool   `json:"browsable,omitempty"`
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
	if err != nil || node.Type == repository.NodeDevice {
		img, inner, ok, ierr := s.imageAt(r, repo, snap, p)
		if ok {
			s.imageTree(w, img, p, inner)
			return
		}
		if ierr != nil {
			err = ierr
		}
	}
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

// download streams files or directories of a snapshot. Query: path
// (repeatable; default the whole snapshot) and format (tar, tar.gz, zip;
// a single file without format is sent as it is).
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
	paths := r.URL.Query()["path"]
	if len(paths) == 0 {
		paths = []string{"/"}
	}
	format := r.URL.Query().Get("format")
	if format != "" && !restorer.ValidFormat(format) {
		s.fail(w, fmt.Errorf("unknown format %q (tar, tar.gz, zip)", format), "")
		return
	}
	// Entries are named relative to the common parent of the selection.
	parent := commonDir(paths)
	var items []restorer.Item
	for _, p := range paths {
		name := strings.TrimPrefix(strings.TrimPrefix(path.Clean("/"+p), parent), "/")
		node, err := repo.FindNode(r.Context(), snap.Tree, p)
		if err != nil {
			img, inner, ok, ierr := s.imageAt(r, repo, snap, p)
			if !ok {
				if ierr != nil {
					err = ierr
				}
				s.fail(w, err, "")
				return
			}
			src, err := img.Source(inner)
			if err != nil {
				s.fail(w, err, "")
				return
			}
			items = append(items, restorer.Item{Name: name, Image: &src})
			continue
		}
		items = append(items, restorer.Item{Name: name, Node: node})
	}
	if len(items) == 1 && items[0].Image != nil && items[0].Image.Entry.Type != "dir" && format == "" {
		src := items[0].Image
		rd, size, err := src.FS.Open(src.Path) // links: the file they point to
		if err != nil {
			s.fail(w, err, "")
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", fmt.Sprint(size))
		w.Header().Set("Content-Disposition", attachment(src.Entry.Name))
		_, _ = io.Copy(w, io.NewSectionReader(rd, 0, size))
		return
	}
	if len(items) == 1 && items[0].Node != nil && items[0].Node.Type != repository.NodeDir && format == "" {
		n := items[0].Node
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", fmt.Sprint(n.Size))
		w.Header().Set("Content-Disposition", attachment(n.Name))
		_ = restorer.DumpFile(r.Context(), repo, n, w)
		return
	}
	if format == "" {
		format = restorer.FormatTar
	}
	base := ""
	switch {
	case len(items) == 1 && items[0].Name != "":
		base = path.Base(items[0].Name)
	case len(items) == 1:
		base = snap.Job
		if base == "" {
			base = "snapshot"
		}
		base = strings.ReplaceAll(base, "/", "-") + "-" + snap.Time.Local().Format("2006-01-02-1504")
	default:
		base = fmt.Sprintf("%s-%d-items", path.Base("/"+parent), len(items))
		if parent == "/" || parent == "" {
			base = fmt.Sprintf("snapshot-%s-%d-items", snap.ID().Short(), len(items))
		}
	}
	ct := map[string]string{restorer.FormatTar: "application/x-tar", restorer.FormatTarGz: "application/gzip", restorer.FormatZip: "application/zip"}[format]
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Disposition", attachment(base+"."+format))
	_ = restorer.WriteArchive(r.Context(), repo, items, format, w)
}

// commonDir returns the deepest directory containing all paths' parents.
func commonDir(paths []string) string {
	var common []string
	for i, p := range paths {
		parts := strings.Split(strings.Trim(path.Dir(path.Clean("/"+p)), "/"), "/")
		if i == 0 {
			common = parts
			continue
		}
		n := 0
		for n < len(common) && n < len(parts) && common[n] == parts[n] {
			n++
		}
		common = common[:n]
	}
	return path.Clean("/" + strings.Join(common, "/"))
}

// attachment builds a Content-Disposition header that also carries
// non-ASCII file names.
func attachment(name string) string {
	ascii := strings.Map(func(r rune) rune {
		if r < 0x20 || r > 0x7e || r == '"' || r == '\\' {
			return '_'
		}
		return r
	}, name)
	return fmt.Sprintf("attachment; filename=%q; filename*=UTF-8''%s", ascii, url.PathEscape(name))
}

// preview shows a file inline: images as they are, text (up to 256 KiB)
// as plain text. Nothing is executed: the response is sandboxed.
func (s *Server) preview(w http.ResponseWriter, r *http.Request) {
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
	n, err := repo.FindNode(r.Context(), snap.Tree, p)
	var dump func(io.Writer) error
	if err != nil {
		img, inner, ok, ierr := s.imageAt(r, repo, snap, p)
		if !ok {
			if ierr != nil {
				err = ierr
			}
			s.fail(w, err, "")
			return
		}
		src, err := img.Source(inner)
		if err != nil {
			s.fail(w, err, "")
			return
		}
		n = restorer.ImageEntryNode(src.Entry)
		if src.Entry.Type == "symlink" {
			// Show what the link points to, when it is a file.
			if _, size, err := src.FS.Open(src.Path); err == nil {
				n.Type, n.Size = repository.NodeFile, uint64(size)
			}
		}
		dump = func(out io.Writer) error {
			rd, size, err := src.FS.Open(src.Path)
			if err != nil {
				return err
			}
			_, err = io.Copy(out, io.NewSectionReader(rd, 0, size))
			return err
		}
	} else {
		dump = func(out io.Writer) error { return restorer.DumpFile(r.Context(), repo, n, out) }
	}
	if n.Type != repository.NodeFile {
		s.fail(w, errors.New("only files can be previewed"), "")
		return
	}
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'; img-src 'self' data:; style-src 'unsafe-inline'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if ct, ok := previewImages[strings.ToLower(path.Ext(n.Name))]; ok {
		if n.Size > 20<<20 {
			s.fail(w, errors.New("the image is too large to preview; download it instead"), "")
			return
		}
		w.Header().Set("Content-Type", ct)
		_ = dump(w)
		return
	}
	const limit = 256 << 10
	var buf bytes.Buffer
	_ = dump(&limitWriter{w: &buf, n: limit})
	data := buf.Bytes()
	if bytes.IndexByte(data[:min(len(data), 8192)], 0) >= 0 || !utf8.Valid(trimUTF8(data)) {
		s.fail(w, errors.New("no preview for binary files; download it instead"), "")
		return
	}
	if n.Size > limit {
		w.Header().Set("X-Herakles-Truncated", "1")
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write(data)
}

var previewImages = map[string]string{".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif", ".webp": "image/webp", ".svg": "image/svg+xml", ".ico": "image/x-icon", ".bmp": "image/bmp"}

// trimUTF8 drops a multi-byte character cut off at the end.
func trimUTF8(b []byte) []byte {
	for i := 0; i < 3 && len(b) > 0 && !utf8.Valid(b); i++ {
		b = b[:len(b)-1]
	}
	return b
}

type limitWriter struct {
	w io.Writer
	n int
}

func (l *limitWriter) Write(p []byte) (int, error) {
	if l.n <= 0 {
		return 0, errPreviewFull
	}
	q := p
	if len(q) > l.n {
		q = q[:l.n]
	}
	n, err := l.w.Write(q)
	l.n -= n
	if err == nil && len(q) < len(p) {
		err = errPreviewFull
	}
	return len(p), err
}

var errPreviewFull = errors.New("preview limit reached")

// find searches a snapshot below path for names containing q (or matching
// q as a glob with * and ?), case-insensitively; at most 500 results.
func (s *Server) find(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.open(w, r)
	if !ok {
		return
	}
	defer repo.Close()
	snap, ok := s.findSnap(w, r, repo)
	if !ok {
		return
	}
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	if q == "" {
		s.fail(w, errors.New("enter something to search for"), "")
		return
	}
	glob := strings.ContainsAny(q, "*?")
	base := path.Clean("/" + r.URL.Query().Get("path"))
	node, err := repo.FindNode(r.Context(), snap.Tree, base)
	if err != nil || node.Type == repository.NodeDevice {
		if img, inner, ok, _ := s.imageAt(r, repo, snap, base); ok {
			out, truncated, err := imageFind(img, base, inner, q, glob, 500)
			if err != nil {
				s.fail(w, err, "")
				return
			}
			writeJSON(w, 200, map[string]any{"results": out, "truncated": truncated})
			return
		}
	}
	if err != nil || node.Type != repository.NodeDir || node.Subtree == nil {
		s.fail(w, fmt.Errorf("%s is not a directory in this snapshot", base), "")
		return
	}
	prefix := strings.TrimSuffix(base, "/")
	out := []treeEntry{}
	truncated := false
	err = repo.Walk(r.Context(), *node.Subtree, func(p string, n *repository.Node) error {
		name := strings.ToLower(n.Name)
		match := strings.Contains(name, q)
		if glob {
			match, _ = path.Match(q, name)
		}
		if match {
			if len(out) >= 500 {
				truncated = true
				return errPreviewFull
			}
			out = append(out, treeEntry{Name: n.Name, Path: prefix + p, Type: string(n.Type), Size: n.Size, Mode: n.Mode.String(), MTime: n.ModTime, Link: n.LinkTarget})
		}
		return nil
	})
	if err != nil && err != errPreviewFull {
		s.fail(w, err, "")
		return
	}
	writeJSON(w, 200, map[string]any{"results": out, "truncated": truncated})
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
		Paths     []string `json:"paths"`
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
	paths := req.Paths
	if req.Path != "" || len(paths) == 0 {
		p := req.Path
		if p == "" {
			p = "/"
		}
		paths = append([]string{p}, paths...)
	}
	var errs []string
	total := &restorer.Result{}
	for _, p := range paths {
		opts := restorer.Options{
			Target: config.ExpandHome(req.Target), Overwrite: req.Overwrite, Verify: req.Verify, Includes: req.Includes, Excludes: req.Excludes,
			OnError: func(p string, err error) {
				if len(errs) < 20 {
					errs = append(errs, p+": "+err.Error())
				}
			},
		}
		var res *restorer.Result
		var err error
		if _, ferr := repo.FindNode(r.Context(), snap.Tree, p); ferr != nil {
			// Files inside a disk image.
			img, inner, ok, ierr := s.imageAt(r, repo, snap, p)
			if !ok {
				if ierr != nil {
					ferr = ierr
				}
				s.fail(w, fmt.Errorf("%s: %w", p, ferr), "")
				return
			}
			src, serr := img.Source(inner)
			if serr != nil {
				s.fail(w, serr, "")
				return
			}
			res, err = restorer.RestoreImage(r.Context(), src, opts)
		} else {
			res, err = restorer.Restore(r.Context(), repo, snap, p, opts)
		}
		if err != nil {
			s.fail(w, fmt.Errorf("%s: %w", p, err), "")
			return
		}
		total.Files += res.Files
		total.Dirs += res.Dirs
		total.Bytes += res.Bytes
		total.Skipped += res.Skipped
		total.Errors += res.Errors
		total.Verified += res.Verified
	}
	writeJSON(w, 200, map[string]any{"result": total, "errors": nonNil(errs)})
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

// installUpdate starts the update; Herakles restarts when it is done, and
// errors show up in the update state.
func (s *Server) installUpdate(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	if s.install == nil {
		hint := "this window cannot update Herakles"
		if s.upd != nil && s.upd().Hint != "" {
			hint = s.upd().Hint
		}
		writeJSON(w, 400, apiError{Error: hint})
		return
	}
	go func() { _ = s.install() }()
	writeJSON(w, 202, map[string]any{"started": true})
}
