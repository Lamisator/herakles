package hub

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"

	"github.com/lamisator/herakles/internal/config"
)

// Hub accepts the connections of attached servers (under /hub/ on the web
// interface) and forwards requests to them.
type Hub struct {
	Store  *Store
	Log    *slog.Logger
	Config func() *config.Config
	// Version of the hub's Herakles, compared with the servers'.
	Version string

	mu     sync.Mutex
	conns  map[string]*conn
	replay replayGuard
}

type conn struct {
	id      string
	sess    *yamux.Session
	tr      *http.Transport
	since   time.Time
	addr    string
	hello   Hello
	sync    *SyncResult
	syncErr string
}

// New creates a hub; it watches the store for removed servers until ctx
// ends.
func New(ctx context.Context, store *Store, log *slog.Logger, cfg func() *config.Config) *Hub {
	h := &Hub{Store: store, Log: log, Config: cfg, conns: map[string]*conn{}}
	go h.sweep(ctx)
	return h
}

func yamuxConfig() *yamux.Config {
	c := yamux.DefaultConfig()
	c.LogOutput = io.Discard
	c.KeepAliveInterval = 20 * time.Second
	c.MaxStreamWindowSize = 4 << 20
	return c
}

// sweep drops servers removed from the store (e.g. by 'herakles hub
// remove') and closes everything when ctx ends.
func (h *Hub) sweep(ctx context.Context) {
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			h.mu.Lock()
			for _, c := range h.conns {
				_ = c.sess.Close()
			}
			h.mu.Unlock()
			return
		case <-t.C:
		}
		servers, err := h.Store.Servers()
		if err != nil {
			continue
		}
		known := map[string]bool{}
		for _, s := range servers {
			known[s.ID] = true
		}
		h.mu.Lock()
		for id, c := range h.conns {
			if !known[id] {
				_ = c.sess.Close()
			}
		}
		h.mu.Unlock()
	}
}

// Handler serves /hub/join and /hub/connect for the attached servers. It
// does its own authentication (join token, server login).
func (h *Hub) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /hub/join", h.join)
	mux.HandleFunc("GET /hub/connect", h.connect)
	return mux
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

type joinRequest struct {
	Secret    string            `json:"secret"`
	PublicKey ed25519.PublicKey `json:"public_key"`
	Nonce     []byte            `json:"nonce"`
	Hello     Hello             `json:"hello"`
}

type joinResponse struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Signature []byte `json:"signature"`
}

// join enrolls a server: it proves the one-time secret, the hub registers
// the server's public key and signs the answer with its identity key.
func (h *Hub) join(w http.ResponseWriter, r *http.Request) {
	var req joinRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	if len(req.Nonce) < 16 {
		writeJSON(w, 400, map[string]string{"error": "missing nonce"})
		return
	}
	key, err := h.Store.Identity()
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	srv, err := h.Store.Join(strings.TrimSpace(req.Secret), req.PublicKey, req.Hello)
	if err != nil {
		time.Sleep(time.Second)
		code := 400
		if errors.Is(err, ErrBadToken) {
			code = 403
		}
		writeJSON(w, code, map[string]string{"error": err.Error()})
		return
	}
	h.Log.Info("hub: server enrolled", "server", srv.Name, "id", srv.ID, "address", remoteAddr(r))
	sig := ed25519.Sign(key, joinMessage(req.Nonce, req.PublicKey, srv.ID, srv.Name))
	writeJSON(w, 200, joinResponse{ID: srv.ID, Name: srv.Name, Signature: sig})
}

// authenticate checks a server's signed login (X-Herakles-Auth).
func (h *Hub) authenticate(r *http.Request, hubKey ed25519.PublicKey) (*Server, parsedAuth, error) {
	a, err := parseAuth(r.Header.Get(AuthHeader))
	if err != nil {
		return nil, a, err
	}
	now := time.Now()
	if d := now.Sub(time.Unix(a.ts, 0)); d > MaxClockSkew || d < -MaxClockSkew {
		return nil, a, fmt.Errorf("the server's clock is off by %s; fix its time (NTP)", d.Round(time.Second))
	}
	srv, err := h.Store.Get(a.id)
	if err != nil {
		return nil, a, err
	}
	if !ed25519.Verify(srv.PublicKey, connectMessage(a.id, a.ts, a.nonce, hubKey), a.sig) {
		return nil, a, errors.New("invalid signature")
	}
	if !h.replay.fresh(a.nonce, now) {
		return nil, a, errors.New("login replayed")
	}
	return srv, a, nil
}

func remoteAddr(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		return strings.TrimSpace(parts[len(parts)-1])
	}
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	return host
}

func (h *Hub) connect(w http.ResponseWriter, r *http.Request) {
	key, err := h.Store.Identity()
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	srv, auth, err := h.authenticate(r, key.Public().(ed25519.PublicKey))
	if err != nil {
		h.Log.Warn("hub: login refused", "address", remoteAddr(r), "err", err)
		time.Sleep(time.Second)
		writeJSON(w, 401, map[string]string{"error": err.Error()})
		return
	}
	// The hub proves its identity in the handshake answer.
	w.Header().Set(HubSigHeader, b64.EncodeToString(ed25519.Sign(key, hubMessage(srv.ID, auth.nonce))))
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		return
	}
	ws.SetReadLimit(64 << 20)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sess, err := yamux.Client(websocket.NetConn(ctx, ws, websocket.MessageBinary), yamuxConfig())
	if err != nil {
		_ = ws.CloseNow()
		return
	}
	c := &conn{id: srv.ID, sess: sess, since: time.Now(), addr: remoteAddr(r)}
	c.tr = &http.Transport{
		DialContext:         func(context.Context, string, string) (net.Conn, error) { return sess.Open() },
		MaxIdleConnsPerHost: 8,
		IdleConnTimeout:     time.Minute,
	}
	h.mu.Lock()
	if old := h.conns[srv.ID]; old != nil {
		_ = old.sess.Close()
	}
	h.conns[srv.ID] = c
	h.mu.Unlock()
	h.Log.Info("hub: server connected", "server", srv.Name, "address", c.addr)
	go h.greet(c, srv.Name)
	select {
	case <-sess.CloseChan():
	case <-r.Context().Done():
	}
	_ = sess.Close()
	c.tr.CloseIdleConnections()
	h.mu.Lock()
	if h.conns[srv.ID] == c {
		delete(h.conns, srv.ID)
	}
	h.mu.Unlock()
	_ = h.Store.Seen(srv.ID, c.addr, c.hello)
	h.Log.Info("hub: server disconnected", "server", srv.Name)
}

// greet asks a new connection who it is and passes the repositories on.
func (h *Hub) greet(c *conn, name string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var hello Hello
	if err := h.call(ctx, c, http.MethodGet, "/hub/hello", nil, &hello); err != nil {
		h.Log.Warn("hub: server did not answer", "server", name, "err", err)
		return
	}
	h.mu.Lock()
	c.hello = hello
	h.mu.Unlock()
	_ = h.Store.Seen(c.id, c.addr, hello)
	if cp := Compare(h.Version, LocalCapabilities(), hello); cp.Status != "ok" {
		for _, i := range cp.Issues {
			h.Log.Warn("hub: server and hub are not fully compatible", "server", name, "version", hello.Version,
				"hub_version", h.Version, "lacking", i.Side, "capability", i.Capability, "consequence", i.Message)
		}
	}
	h.push(ctx, c, name)
}

func (h *Hub) push(ctx context.Context, c *conn, name string) {
	var res SyncResult
	shared, err := Shared(h.Config())
	if err == nil {
		err = h.call(ctx, c, http.MethodPut, "/hub/repositories", shared, &res)
	}
	h.mu.Lock()
	c.sync, c.syncErr = &res, ""
	if err != nil {
		c.sync, c.syncErr = nil, err.Error()
	}
	h.mu.Unlock()
	switch {
	case err != nil:
		h.Log.Warn("hub: repositories not passed on", "server", name, "err", err)
	case res.Changed() || len(res.Conflicts) > 0:
		h.Log.Info("hub: repositories passed on", "server", name, "added", res.Added, "updated", res.Updated, "removed", res.Removed, "conflicts", res.Conflicts)
	}
}

// PushAll passes the shared repositories to every connected server again
// (after the configuration changed).
func (h *Hub) PushAll() {
	h.mu.Lock()
	conns := make([]*conn, 0, len(h.conns))
	for _, c := range h.conns {
		conns = append(conns, c)
	}
	h.mu.Unlock()
	for _, c := range conns {
		go func(c *conn) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			name := c.id
			if s, err := h.Store.Find(c.id); err == nil {
				name = s.Name
			}
			h.push(ctx, c, name)
		}(c)
	}
}

func (h *Hub) get(id string) (*conn, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	c := h.conns[id]
	if c == nil {
		return nil, ErrOffline
	}
	return c, nil
}

// ErrOffline is returned for servers that are not connected.
var ErrOffline = errors.New("the server is not connected to the hub")

func (h *Hub) call(ctx context.Context, c *conn, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://server"+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(RoleHeader, "admin")
	resp, err := (&http.Client{Transport: c.tr}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		var e struct{ Error string }
		_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&e)
		if e.Error == "" {
			e.Error = resp.Status
		}
		return errors.New(e.Error)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

// Call sends a request to a connected server's API as an administrator.
func (h *Hub) Call(ctx context.Context, id, method, path string, body, out any) error {
	c, err := h.get(id)
	if err != nil {
		return err
	}
	return h.call(ctx, c, method, path, body, out)
}

// Headers on requests the hub forwards: who is asking.
const (
	RoleHeader = "X-Herakles-Hub-Role"
	UserHeader = "X-Herakles-Hub-User"
)

// Proxy forwards a GUI request to a server's API. path is the escaped
// path on the server (/api/...). The answer is sandboxed: a server cannot
// run scripts in the hub's pages.
func (h *Hub) Proxy(w http.ResponseWriter, r *http.Request, id, path, user, role string) {
	c, err := h.get(id)
	if err != nil {
		writeJSON(w, 502, map[string]any{"error": err.Error(), "offline": true})
		return
	}
	unescaped, err := url.PathUnescape(path)
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme, pr.Out.URL.Host = "http", "server"
			pr.Out.URL.Path, pr.Out.URL.RawPath = unescaped, path
			pr.Out.Host = "server"
			for _, k := range []string{"Cookie", "Authorization", "Origin", "Referer", RoleHeader, UserHeader} {
				pr.Out.Header.Del(k)
			}
			pr.Out.Header.Set(RoleHeader, role)
			pr.Out.Header.Set(UserHeader, user)
		},
		Transport: c.tr,
		ModifyResponse: func(resp *http.Response) error {
			resp.Header.Del("Set-Cookie")
			ct := strings.ToLower(resp.Header.Get("Content-Type"))
			if strings.Contains(ct, "html") || strings.Contains(ct, "xml") || strings.Contains(ct, "javascript") {
				resp.Header.Set("Content-Type", "text/plain; charset=utf-8")
			}
			resp.Header.Set("X-Content-Type-Options", "nosniff")
			resp.Header.Set("Content-Security-Policy", "sandbox; default-src 'none'; img-src 'self' data:")
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			writeJSON(w, 502, map[string]string{"error": "server: " + err.Error()})
		},
	}
	rp.ServeHTTP(w, r)
}

// View is an attached server for the GUI.
type View struct {
	ID        string      `json:"id"`
	Name      string      `json:"name"`
	Online    bool        `json:"online"`
	Since     time.Time   `json:"since,omitempty"`
	LastSeen  time.Time   `json:"last_seen,omitempty"`
	Created   time.Time   `json:"created"`
	Address   string      `json:"address,omitempty"`
	Hello     Hello       `json:"hello"`
	Sync      *SyncResult `json:"sync,omitempty"`
	SyncError string      `json:"sync_error,omitempty"`
	// Compat says what the server and this hub cannot do together.
	Compat Compatibility `json:"compat"`
}

// Servers lists the attached servers with their connection state.
func (h *Hub) Servers() ([]View, error) {
	all, err := h.Store.Servers()
	if err != nil {
		return nil, err
	}
	out := make([]View, 0, len(all))
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, s := range all {
		v := View{ID: s.ID, Name: s.Name, LastSeen: s.LastSeen, Created: s.Created, Address: s.Address, Hello: s.Hello}
		if c := h.conns[s.ID]; c != nil {
			v.Online, v.Since, v.Address, v.Sync, v.SyncError = true, c.since, c.addr, c.sync, c.syncErr
			if c.hello.Hostname != "" {
				v.Hello = c.hello
			}
		}
		v.Compat = Compare(h.Version, LocalCapabilities(), v.Hello)
		out = append(out, v)
	}
	return out, nil
}

// Online lists the IDs of the connected servers.
func (h *Hub) Online() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]string, 0, len(h.conns))
	for id := range h.conns {
		out = append(out, id)
	}
	return out
}

// Remove detaches a server and closes its connection.
func (h *Hub) Remove(id string) error {
	if err := h.Store.Remove(id); err != nil {
		return err
	}
	h.mu.Lock()
	if c := h.conns[id]; c != nil {
		_ = c.sess.Close()
	}
	h.mu.Unlock()
	return nil
}

// JoinCommand is the command that enrolls a server with a code.
func JoinCommand(code string) string { return "sudo herakles hub join " + code }
