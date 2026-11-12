package hub

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"

	"github.com/lamisator/herakles/internal/blockdev"
	"github.com/lamisator/herakles/internal/config"
)

// Credentials are the login of an attached server (hub-agent.json in the
// state directory, 0600).
type Credentials struct {
	Hub  string `json:"hub"`
	ID   string `json:"id"`
	Name string `json:"name"`
	// Key is this server's private identity key (Ed25519 seed); the hub
	// knows only its public half.
	Key []byte `json:"key"`
	// HubKey is the hub's public identity key from the enrollment code.
	HubKey []byte `json:"hub_key"`
	// TLSPin trusts the hub's own certificate (from the code).
	TLSPin []byte `json:"tls_pin,omitempty"`
}

func (c *Credentials) privateKey() (ed25519.PrivateKey, error) {
	if len(c.Key) != ed25519.SeedSize || len(c.HubKey) != ed25519.PublicKeySize {
		return nil, errors.New("the hub login is incomplete; enroll again with a new code")
	}
	return ed25519.NewKeyFromSeed(c.Key), nil
}

func sameCredentials(a, b *Credentials) bool {
	return a != nil && b != nil && a.Hub == b.Hub && a.ID == b.ID && bytes.Equal(a.Key, b.Key) &&
		bytes.Equal(a.HubKey, b.HubKey) && bytes.Equal(a.TLSPin, b.TLSPin)
}

// CredentialsPath is the login file in a state directory.
func CredentialsPath(stateDir string) string { return filepath.Join(stateDir, "hub-agent.json") }

// LoadCredentials reads the login; nil without one.
func LoadCredentials(stateDir string) (*Credentials, error) {
	b, err := os.ReadFile(CredentialsPath(stateDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var c Credentials
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

// SaveCredentials writes the login.
func SaveCredentials(stateDir string, c *Credentials) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return err
	}
	p := CredentialsPath(stateDir)
	if err := os.WriteFile(p+".tmp", b, 0o600); err != nil {
		return err
	}
	return os.Rename(p+".tmp", p)
}

// LocalHello describes this machine.
func LocalHello(version string) Hello {
	host, _ := os.Hostname()
	return Hello{Hostname: host, Version: version, OS: runtime.GOOS, Arch: runtime.GOARCH, Container: blockdev.Container(), Capabilities: LocalCapabilities()}
}

// HTTPClient connects to the hub. A certificate is accepted when it is
// valid for the system (or ca_cert), or when its public key matches pin.
func HTTPClient(a config.Agent, pin []byte) (*http.Client, error) {
	roots, err := x509.SystemCertPool()
	if err != nil {
		roots = x509.NewCertPool()
	}
	if a.CACert != "" {
		pem, err := os.ReadFile(config.ExpandHome(a.CACert))
		if err != nil {
			return nil, err
		}
		roots = x509.NewCertPool()
		if !roots.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("%s: no certificates", a.CACert)
		}
	}
	tc := &tls.Config{
		// Checked in VerifyConnection, so a pinned self-signed certificate
		// works too.
		InsecureSkipVerify: true, //nolint:gosec // verified below
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("the hub sent no certificate")
			}
			leaf := cs.PeerCertificates[0]
			if pin != nil {
				if h := sha256.Sum256(leaf.RawSubjectPublicKeyInfo); bytes.Equal(h[:], pin) {
					return nil
				}
			}
			if a.InsecureSkipVerify {
				return nil
			}
			inter := x509.NewCertPool()
			for _, c := range cs.PeerCertificates[1:] {
				inter.AddCert(c)
			}
			_, err := leaf.Verify(x509.VerifyOptions{DNSName: cs.ServerName, Roots: roots, Intermediates: inter})
			return err
		},
	}
	return &http.Client{Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, TLSClientConfig: tc}}, nil
}

// Enroll attaches this machine to a hub with an enrollment code: it makes
// its own key pair, proves the one-time secret, and accepts the answer
// only when the hub named in the code signed it.
func Enroll(ctx context.Context, code *Code, a config.Agent, hello Hello) (*Credentials, error) {
	if time.Now().After(code.ExpiresAt()) {
		return nil, fmt.Errorf("the enrollment code expired at %s; create a new one on the hub", code.ExpiresAt().Format(time.RFC3339))
	}
	a.Hub = code.URL
	cl, err := HTTPClient(a, code.TLSPin)
	if err != nil {
		return nil, err
	}
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	b, _ := json.Marshal(joinRequest{Secret: code.Secret, PublicKey: pub, Nonce: nonce, Hello: hello})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, code.URL+"/hub/join", bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := cl.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != 200 {
		var e struct{ Error string }
		if json.Unmarshal(body, &e) != nil || e.Error == "" {
			if resp.StatusCode == 404 {
				return nil, fmt.Errorf("%s is not a Herakles hub (no web interface there, or an older version)", code.URL)
			}
			e.Error = resp.Status
		}
		return nil, fmt.Errorf("hub: %s", e.Error)
	}
	var jr joinResponse
	if err := json.Unmarshal(body, &jr); err != nil {
		return nil, fmt.Errorf("hub: unexpected answer: %w", err)
	}
	if !ed25519.Verify(code.HubKey, joinMessage(nonce, pub, jr.ID, jr.Name), jr.Signature) {
		return nil, errors.New("the answer is not signed by the hub in the enrollment code (someone may be in between); not attached")
	}
	return &Credentials{Hub: code.URL, ID: jr.ID, Name: jr.Name, Key: key.Seed(), HubKey: code.HubKey, TLSPin: code.TLSPin}, nil
}

// AgentState is the connection state of an attached server.
type AgentState struct {
	Hub       string    `json:"hub"`
	Name      string    `json:"name,omitempty"`
	Connected bool      `json:"connected"`
	Since     time.Time `json:"since,omitempty"`
	Error     string    `json:"error,omitempty"`
}

// Agent keeps this machine connected to its hub and serves the hub's
// requests with Handler (the GUI API).
type Agent struct {
	Config  func() *config.Config
	Handler http.Handler
	Version string
	Log     *slog.Logger
	// Apply stores the repositories the hub passes on.
	Apply func(shared []SharedRepository) (SyncResult, error)

	wake  chan struct{}
	mu    sync.Mutex
	state AgentState
}

// Wake makes the agent re-read its configuration (after a reload).
func (a *Agent) Wake() {
	select {
	case a.wakeCh() <- struct{}{}:
	default:
	}
}

func (a *Agent) wakeCh() chan struct{} {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.wake == nil {
		a.wake = make(chan struct{}, 1)
	}
	return a.wake
}

// State returns the connection state (nil when not attached).
func (a *Agent) State() *AgentState {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.state.Hub == "" {
		return nil
	}
	s := a.state
	return &s
}

func (a *Agent) setState(fn func(*AgentState)) {
	a.mu.Lock()
	fn(&a.state)
	a.mu.Unlock()
}

// errRemoved means the hub no longer knows this server.
var errRemoved = errors.New("the hub refused the login: this server was removed from the hub")

// Run connects to the hub and reconnects until ctx ends.
func (a *Agent) Run(ctx context.Context) {
	wake := a.wakeCh()
	delay := time.Second
	for {
		cfg := a.Config()
		creds, err := LoadCredentials(cfg.GetStateDir())
		if err != nil {
			a.Log.Error("hub: cannot read the login", "err", err)
		}
		if cfg.Agent.Hub == "" || creds == nil {
			a.setState(func(s *AgentState) { *s = AgentState{} })
			select {
			case <-ctx.Done():
				return
			case <-wake:
				continue
			}
		}
		a.setState(func(s *AgentState) { *s = AgentState{Hub: cfg.Agent.Hub, Name: creds.Name} })
		start := time.Now()
		err = a.session(ctx, cfg, creds, wake)
		if ctx.Err() != nil {
			return
		}
		if time.Since(start) > time.Minute {
			delay = time.Second
		}
		wait := delay
		if errors.Is(err, errRemoved) {
			wait = 10 * time.Minute
		}
		if err != nil {
			a.Log.Warn("hub: not connected; retrying", "hub", cfg.Agent.Hub, "err", err, "in", wait.Round(time.Second))
			a.setState(func(s *AgentState) { s.Connected, s.Error = false, err.Error() })
		}
		select {
		case <-ctx.Done():
			return
		case <-wake:
		case <-time.After(wait):
		}
		delay = min(2*delay, time.Minute)
	}
}

func (a *Agent) session(ctx context.Context, cfg *config.Config, creds *Credentials, wake chan struct{}) error {
	key, err := creds.privateKey()
	if err != nil {
		return err
	}
	cl, err := HTTPClient(cfg.Agent, creds.TLSPin)
	if err != nil {
		return err
	}
	auth, nonce := connectAuth(creds.ID, key, creds.HubKey, time.Now())
	dctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	ws, resp, err := websocket.Dial(dctx, strings.TrimSuffix(cfg.Agent.Hub, "/")+"/hub/connect", &websocket.DialOptions{
		HTTPClient:      cl,
		HTTPHeader:      http.Header{AuthHeader: {auth}},
		CompressionMode: websocket.CompressionDisabled,
	})
	cancel()
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusUnauthorized {
			var e struct{ Error string }
			if resp.Body != nil {
				_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&e)
			}
			if strings.Contains(e.Error, "clock") {
				return errors.New("hub: " + e.Error)
			}
			return errRemoved
		}
		return err
	}
	// Only the hub from the enrollment code can sign our nonce.
	sig, _ := b64.DecodeString(resp.Header.Get(HubSigHeader))
	if !ed25519.Verify(creds.HubKey, hubMessage(creds.ID, nonce), sig) {
		_ = ws.CloseNow()
		return errors.New("the hub did not prove its identity (its key differs from the enrollment code); not connected")
	}
	ws.SetReadLimit(64 << 20)
	sctx, stop := context.WithCancel(ctx)
	defer stop()
	sess, err := yamux.Server(websocket.NetConn(sctx, ws, websocket.MessageBinary), yamuxConfig())
	if err != nil {
		_ = ws.CloseNow()
		return err
	}
	a.Log.Info("hub: connected", "hub", cfg.Agent.Hub, "as", creds.Name)
	a.setState(func(s *AgentState) { s.Connected, s.Since, s.Error = true, time.Now(), "" })
	srv := &http.Server{Handler: a.handler(), ReadHeaderTimeout: 30 * time.Second}
	go func() {
		for {
			select {
			case <-sctx.Done():
				_ = sess.Close()
				return
			case <-sess.CloseChan():
				return
			case <-wake:
				// Reconnect only when the hub or the login changed.
				c := a.Config()
				nc, _ := LoadCredentials(c.GetStateDir())
				if c.Agent != cfg.Agent || !sameCredentials(nc, creds) {
					_ = sess.Close()
					select {
					case wake <- struct{}{}:
					default:
					}
					return
				}
			}
		}
	}()
	_ = srv.Serve(sess)
	_ = sess.Close()
	a.setState(func(s *AgentState) { s.Connected = false })
	if ctx.Err() != nil {
		return nil
	}
	a.Log.Info("hub: connection closed", "hub", cfg.Agent.Hub)
	return errors.New("connection closed")
}

// handler answers the hub: who this is, the shared repositories, and the
// GUI API for everything else.
func (a *Agent) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /hub/hello", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, LocalHello(a.Version))
	})
	mux.HandleFunc("PUT /hub/repositories", func(w http.ResponseWriter, r *http.Request) {
		var shared []SharedRepository
		if err := json.NewDecoder(io.LimitReader(r.Body, 16<<20)).Decode(&shared); err != nil {
			writeJSON(w, 400, map[string]string{"error": err.Error()})
			return
		}
		res, err := a.Apply(shared)
		if err != nil {
			writeJSON(w, 500, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, 200, res)
	})
	mux.Handle("/", a.Handler)
	return mux
}

// Attach enrolls this machine with code and prepares c for the hub: the
// login goes into c's state directory and c.Agent is set to ag (whose Hub
// becomes the code's URL). The caller saves c.
func Attach(ctx context.Context, c *config.Config, code *Code, ag config.Agent, name, version string) (*Credentials, error) {
	if c.Agent.Hub != "" {
		if cur, _ := LoadCredentials(c.GetStateDir()); cur != nil {
			return nil, fmt.Errorf("already attached to %s as %s; leave that hub first", c.Agent.Hub, cur.Name)
		}
	}
	if name != "" && code.Name == "" {
		code.Name = name
	}
	ag.Hub = code.URL
	c.Agent = ag
	if err := c.Validate(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	hello := LocalHello(version)
	if name != "" {
		hello.Hostname = name
	}
	creds, err := Enroll(ctx, code, ag, hello)
	if err != nil {
		return nil, err
	}
	if err := SaveCredentials(c.GetStateDir(), creds); err != nil {
		return nil, err
	}
	return creds, nil
}

// Detach removes the hub from c and deletes the login. The hub's
// repositories stay as ordinary ones where jobs use them (returned). The
// caller saves c.
func Detach(c *config.Config) ([]string, error) {
	c.Agent = config.Agent{}
	kept := Release(c)
	if err := os.Remove(CredentialsPath(c.GetStateDir())); err != nil && !errors.Is(err, os.ErrNotExist) {
		return kept, err
	}
	return kept, nil
}
