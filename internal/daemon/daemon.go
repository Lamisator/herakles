package daemon

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lamisator/herakles/internal/config"
	"github.com/lamisator/herakles/internal/engine"
)

// DefaultControlAddr is the default control API address.
const DefaultControlAddr = "127.0.0.1:7477"

// Daemon combines runner, scheduler and control API.
type Daemon struct {
	Engine    *engine.Engine
	Runner    *Runner
	Scheduler *Scheduler
	Logs      *LogBuffer
	Started   time.Time
	Version   string
	token     string
	addr      string
	ln        net.Listener
	reload    func() (*config.Config, error)
	// Extra adds to the status, e.g. the hub connection.
	Extra func(*Status)
	// AfterReload runs once a reloaded configuration is in use.
	AfterReload func()
}

// Options configure a daemon.
type Options struct {
	Version string
	Logs    *LogBuffer
	// Reload re-reads the configuration (for SIGHUP / API).
	Reload func() (*config.Config, error)
}

// TokenPath is where the control token is stored.
func TokenPath(cfg *config.Config) string {
	return filepath.Join(cfg.GetStateDir(), "daemon.token")
}

// ControlAddr returns the configured control address.
func ControlAddr(cfg *config.Config) string {
	if cfg.Daemon.ControlListen != "" {
		return cfg.Daemon.ControlListen
	}
	return DefaultControlAddr
}

// New creates a daemon.
func New(ctx context.Context, eng *engine.Engine, opts Options) (*Daemon, error) {
	cfg := eng.Config()
	if opts.Logs == nil {
		opts.Logs = NewLogBuffer(500)
	}
	rn := NewRunner(ctx, eng, cfg.Daemon.MaxConcurrentJobs)
	d := &Daemon{Engine: eng, Runner: rn, Logs: opts.Logs, Started: time.Now(), Version: opts.Version, addr: ControlAddr(cfg), reload: opts.Reload}
	d.Scheduler = NewScheduler(rn)
	tok := make([]byte, 24)
	if _, err := rand.Read(tok); err != nil {
		return nil, err
	}
	d.token = hex.EncodeToString(tok)
	return d, nil
}

// Listen opens the control API port. Run calls it when needed; calling it
// first reports a busy port (usually another running daemon) before
// anything else is started.
func (d *Daemon) Listen() error {
	if d.ln != nil {
		return nil
	}
	ln, err := net.Listen("tcp", d.addr)
	if err != nil {
		return fmt.Errorf("control API: %w", err)
	}
	d.ln = ln
	return nil
}

// Run starts the scheduler and the control API and blocks until ctx ends.
func (d *Daemon) Run(ctx context.Context) error {
	if err := d.Listen(); err != nil {
		return err
	}
	ln := d.ln
	tp := TokenPath(d.Engine.Config())
	if err := os.MkdirAll(filepath.Dir(tp), 0o700); err != nil {
		ln.Close()
		return err
	}
	if err := os.WriteFile(tp, []byte(d.token+"\n"+ln.Addr().String()+"\n"), 0o600); err != nil {
		ln.Close()
		return err
	}
	defer os.Remove(tp)
	srv := &http.Server{Handler: d.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	d.Engine.Log.Info("daemon started", "control", ln.Addr().String(), "scheduled", len(d.Scheduler.Entries()))
	if started := d.Scheduler.CatchUp(); len(started) > 0 {
		d.Engine.Log.Info("catching up missed backups", "jobs", strings.Join(started, ","))
	}
	d.Scheduler.Run(ctx)
	d.Engine.Log.Info("daemon stopping")
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(sctx)
	d.Runner.Wait()
	return nil
}

// Reload re-reads the configuration.
func (d *Daemon) Reload() error {
	if d.reload == nil {
		return errors.New("reload not supported")
	}
	cfg, err := d.reload()
	if err != nil {
		return err
	}
	d.Engine.SetConfig(cfg)
	d.Scheduler.Reload()
	d.Engine.Log.Info("configuration reloaded", "jobs", len(cfg.Jobs))
	if d.AfterReload != nil {
		d.AfterReload()
	}
	return nil
}

// Status is returned by the control API.
type Status struct {
	Version  string                       `json:"version"`
	Started  time.Time                    `json:"started"`
	PID      int                          `json:"pid"`
	Schedule []Entry                      `json:"schedule"`
	Active   []RunView                    `json:"active"`
	Recent   []RunView                    `json:"recent"`
	Last     map[string]*engine.JobResult `json:"last"`
	// Hub lists the IDs of the servers connected to this hub.
	Hub []string `json:"hub,omitempty"`
	// Agent is the connection of this machine to its hub.
	Agent any `json:"agent,omitempty"`
}

// Status collects the current state.
func (d *Daemon) Status() Status {
	st := Status{Version: d.Version, Started: d.Started, PID: os.Getpid(), Schedule: d.Scheduler.Entries(), Active: d.Runner.Active(), Recent: d.Runner.Recent(), Last: map[string]*engine.JobResult{}}
	h := d.Engine.History()
	for _, j := range d.Engine.Config().Jobs {
		if r := h.Last(j.Name); r != nil {
			st.Last[j.Name] = r
		}
	}
	if d.Extra != nil {
		d.Extra(&st)
	}
	return st
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// Handler returns the control API handler (token protected).
func (d *Daemon) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/status", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, d.Status()) })
	mux.HandleFunc("GET /api/logs", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, d.Logs.Lines()) })
	mux.HandleFunc("GET /api/history", func(w http.ResponseWriter, r *http.Request) {
		h, err := d.Engine.History().Load()
		if err != nil {
			writeJSON(w, 500, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, 200, h)
	})
	mux.HandleFunc("POST /api/run", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Kind, Target, Type string }
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
			writeJSON(w, 400, map[string]string{"error": err.Error()})
			return
		}
		if req.Kind == "" {
			req.Kind = KindBackup
		}
		run, err := d.Runner.Enqueue(req.Kind, req.Target, req.Type, "manual")
		if err != nil {
			code := 400
			if errors.Is(err, ErrAlreadyQueued) {
				code = 409
			}
			writeJSON(w, code, map[string]string{"error": err.Error()})
			return
		}
		v, _ := d.Runner.Get(run.ID)
		writeJSON(w, 202, v)
	})
	mux.HandleFunc("GET /api/runs/{id}", func(w http.ResponseWriter, r *http.Request) {
		v, ok := d.Runner.Get(r.PathValue("id"))
		if !ok {
			writeJSON(w, 404, map[string]string{"error": "no such run"})
			return
		}
		writeJSON(w, 200, v)
	})
	mux.HandleFunc("POST /api/cancel/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !d.Runner.Cancel(r.PathValue("id")) {
			writeJSON(w, 404, map[string]string{"error": "no such active task"})
			return
		}
		writeJSON(w, 200, map[string]string{"status": "cancelling"})
	})
	mux.HandleFunc("POST /api/reload", func(w http.ResponseWriter, r *http.Request) {
		if err := d.Reload(); err != nil {
			writeJSON(w, 400, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, 200, map[string]string{"status": "reloaded"})
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(tok), []byte(d.token)) != 1 {
			writeJSON(w, 401, map[string]string{"error": "invalid token"})
			return
		}
		mux.ServeHTTP(w, r)
	})
}

// Token returns the control token (tests and in-process clients).
func (d *Daemon) Token() string { return d.token }

// Client talks to a running daemon.
type Client struct {
	Addr  string
	Token string
	http  *http.Client
}

// ErrNotRunning is returned when no daemon is reachable.
var ErrNotRunning = errors.New("daemon is not running")

// NewClient reads the token file written by a running daemon.
func NewClient(cfg *config.Config) (*Client, error) {
	data, err := os.ReadFile(TokenPath(cfg))
	if err != nil {
		return nil, ErrNotRunning
	}
	lines := strings.Fields(string(data))
	if len(lines) < 2 {
		return nil, ErrNotRunning
	}
	return &Client{Addr: lines[1], Token: lines[0], http: &http.Client{Timeout: 30 * time.Second}}, nil
}

func (c *Client) do(method, path string, body any, out any) error {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = strings.NewReader(string(b))
	}
	req, err := http.NewRequest(method, "http://"+c.Addr+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	resp, err := c.http.Do(req)
	if err != nil {
		return ErrNotRunning
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		var e struct{ Error string }
		_ = json.NewDecoder(resp.Body).Decode(&e)
		return fmt.Errorf("daemon: %s", e.Error)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

// Status fetches the daemon status.
func (c *Client) Status() (*Status, error) {
	var st Status
	return &st, c.do("GET", "/api/status", nil, &st)
}

// Run enqueues a task.
func (c *Client) Run(kind, target, typ string) (*Run, error) {
	var r Run
	return &r, c.do("POST", "/api/run", map[string]string{"Kind": kind, "Target": target, "Type": typ}, &r)
}

// GetRun fetches a task.
func (c *Client) GetRun(id string) (*RunView, error) {
	var r RunView
	return &r, c.do("GET", "/api/runs/"+id, nil, &r)
}

// Cancel cancels a task.
func (c *Client) Cancel(id string) error { return c.do("POST", "/api/cancel/"+id, nil, nil) }

// Reload asks the daemon to reload its configuration.
func (c *Client) Reload() error { return c.do("POST", "/api/reload", nil, nil) }

// Logs fetches recent log lines.
func (c *Client) Logs() ([]LogLine, error) {
	var l []LogLine
	return l, c.do("GET", "/api/logs", nil, &l)
}
