package gui

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/lamisator/herakles/internal/accounts"
)

// WebOptions turn the GUI into a network service with user accounts.
type WebOptions struct {
	// URL is the public address (https://host); passkeys bind to its host.
	URL string
	// Accounts is the file holding users and passkeys.
	Accounts string
	// SessionTTL is the lifetime of a login (default 12h).
	SessionTTL time.Duration
	// Log receives the setup link while no account exists.
	Log *slog.Logger
}

const (
	sessionCookie  = "herakles_web"
	ceremonyCookie = "herakles_passkey"
)

type session struct {
	userID  uint64
	expires time.Time
}

type ceremony struct {
	data    webauthn.SessionData
	userID  uint64 // registration only
	expires time.Time
}

type failures struct {
	count int
	until time.Time
}

// webAuth implements logins for the web mode.
type webAuth struct {
	store      *accounts.Store
	base       *url.URL
	ttl        time.Duration
	secure     bool
	wa         *webauthn.WebAuthn
	log        *slog.Logger
	mu         sync.Mutex
	sessions   map[[32]byte]*session
	ceremonies map[string]*ceremony
	fails      map[string]*failures
	setupToken string
	linkFile   string
}

func newWebAuth(o WebOptions) (*webAuth, error) {
	u, err := url.Parse(o.URL)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("web: invalid url %q", o.URL)
	}
	store, err := accounts.Open(o.Accounts)
	if err != nil {
		return nil, err
	}
	if o.SessionTTL <= 0 {
		o.SessionTTL = 12 * time.Hour
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	origin := u.Scheme + "://" + u.Host
	wa, err := webauthn.New(&webauthn.Config{
		RPID: u.Hostname(), RPDisplayName: "Herakles", RPOrigins: []string{origin},
		AuthenticatorSelection: protocol.AuthenticatorSelection{
			ResidentKey: protocol.ResidentKeyRequirementRequired, RequireResidentKey: protocol.ResidentKeyRequired(),
			UserVerification: protocol.VerificationPreferred,
		},
	})
	if err != nil {
		return nil, err
	}
	a := &webAuth{store: store, base: u, ttl: o.SessionTTL, secure: u.Scheme == "https", wa: wa, log: o.Log,
		sessions: map[[32]byte]*session{}, ceremonies: map[string]*ceremony{}, fails: map[string]*failures{},
		linkFile: filepath.Join(filepath.Dir(o.Accounts), "setup-link")}
	if store.Empty() {
		a.setupToken = randomHex(16)
		link := strings.TrimSuffix(o.URL, "/") + "/login.html#setup=" + a.setupToken
		a.log.Warn("web interface: no account exists yet; create the first administrator at this link (valid until used or restart)", "link", link)
		// Also readable by root on the host, e.g. for `herakles web setup-link`.
		_ = os.MkdirAll(filepath.Dir(a.linkFile), 0o700)
		_ = os.WriteFile(a.linkFile, []byte(link+"\n"), 0o600)
	} else {
		_ = os.Remove(a.linkFile)
	}
	return a, nil
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

type ctxKey struct{}

func userOf(r *http.Request) *accounts.User {
	u, _ := r.Context().Value(ctxKey{}).(*accounts.User)
	return u
}

func (a *webAuth) newSession(w http.ResponseWriter, u *accounts.User) {
	tok := randomHex(32)
	a.mu.Lock()
	now := time.Now()
	for k, s := range a.sessions {
		if now.After(s.expires) {
			delete(a.sessions, k)
		}
	}
	a.sessions[sha256.Sum256([]byte(tok))] = &session{userID: u.ID, expires: now.Add(a.ttl)}
	a.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: tok, Path: "/", HttpOnly: true, Secure: a.secure,
		SameSite: http.SameSiteStrictMode, Expires: now.Add(a.ttl)})
	a.store.LoggedIn(u.ID)
}

// current returns the logged-in user, or nil.
func (a *webAuth) current(r *http.Request) *accounts.User {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return nil
	}
	key := sha256.Sum256([]byte(c.Value))
	a.mu.Lock()
	s := a.sessions[key]
	if s != nil && time.Now().After(s.expires) {
		delete(a.sessions, key)
		s = nil
	}
	a.mu.Unlock()
	if s == nil {
		return nil
	}
	u, err := a.store.Get(s.userID)
	if err != nil || u.Disabled {
		return nil
	}
	return u
}

// endSessions removes the sessions of a user (except keep).
func (a *webAuth) endSessions(userID uint64, keep string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	keepKey := sha256.Sum256([]byte(keep))
	for k, s := range a.sessions {
		if s.userID == userID && k != keepKey {
			delete(a.sessions, k)
		}
	}
}

// clientIP: the listener sits behind a reverse proxy that appends the
// client address to X-Forwarded-For.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		return strings.TrimSpace(parts[len(parts)-1])
	}
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	return host
}

// throttle slows down password guessing: after 5 failures per address and
// name, logins are refused for a growing time (up to 15 minutes).
func (a *webAuth) throttle(key string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if f := a.fails[key]; f != nil && time.Now().Before(f.until) {
		return fmt.Errorf("too many failed logins; try again in %s", time.Until(f.until).Round(time.Second))
	}
	return nil
}

func (a *webAuth) failed(key string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	f := a.fails[key]
	if f == nil {
		f = &failures{}
		a.fails[key] = f
	}
	f.count++
	if f.count >= 5 {
		d := time.Minute << min(f.count-5, 4)
		f.until = time.Now().Add(min(d, 15*time.Minute))
	}
}

type userView struct {
	ID          uint64        `json:"id"`
	Name        string        `json:"name"`
	Role        string        `json:"role"`
	Disabled    bool          `json:"disabled,omitempty"`
	HasPassword bool          `json:"has_password"`
	Passkeys    []passkeyView `json:"passkeys"`
	Created     time.Time     `json:"created"`
	LastLogin   time.Time     `json:"last_login,omitzero"`
}

type passkeyView struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Created  time.Time `json:"created"`
	LastUsed time.Time `json:"last_used,omitzero"`
}

func viewOf(u *accounts.User) userView {
	v := userView{ID: u.ID, Name: u.Name, Role: u.Role, Disabled: u.Disabled, HasPassword: u.HasPassword(),
		Created: u.Created, LastLogin: u.LastLogin, Passkeys: []passkeyView{}}
	for _, p := range u.Passkeys {
		v.Passkeys = append(v.Passkeys, passkeyView{ID: p.ID, Name: p.Name, Created: p.Created, LastUsed: p.LastUsed})
	}
	return v
}

func (a *webAuth) saveCeremony(w http.ResponseWriter, data *webauthn.SessionData, userID uint64) {
	id := randomHex(16)
	a.mu.Lock()
	now := time.Now()
	for k, c := range a.ceremonies {
		if now.After(c.expires) {
			delete(a.ceremonies, k)
		}
	}
	a.ceremonies[id] = &ceremony{data: *data, userID: userID, expires: now.Add(5 * time.Minute)}
	a.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: ceremonyCookie, Value: id, Path: "/", HttpOnly: true, Secure: a.secure,
		SameSite: http.SameSiteStrictMode, MaxAge: 300})
}

// takeCeremony returns and removes the ceremony of the request (single use).
func (a *webAuth) takeCeremony(w http.ResponseWriter, r *http.Request) (*ceremony, error) {
	c, err := r.Cookie(ceremonyCookie)
	if err != nil {
		return nil, errors.New("passkey request expired; try again")
	}
	http.SetCookie(w, &http.Cookie{Name: ceremonyCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: a.secure})
	a.mu.Lock()
	defer a.mu.Unlock()
	cer := a.ceremonies[c.Value]
	delete(a.ceremonies, c.Value)
	if cer == nil || time.Now().After(cer.expires) {
		return nil, errors.New("passkey request expired; try again")
	}
	return cer, nil
}

type webError struct {
	Error string `json:"error"`
}

func authFail(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, webError{Error: err.Error()})
}

// routes registers the login and account endpoints.
func (a *webAuth) routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /auth/state", func(w http.ResponseWriter, r *http.Request) {
		out := map[string]any{"setup": a.store.Empty()}
		if u := a.current(r); u != nil {
			out["user"] = viewOf(u)
		}
		writeJSON(w, 200, out)
	})
	mux.HandleFunc("POST /auth/setup", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Token, Name, Password string }
		if err := decode(r, &req); err != nil {
			authFail(w, 400, err)
			return
		}
		a.mu.Lock()
		tok := a.setupToken
		a.mu.Unlock()
		if tok == "" || !a.store.Empty() || subtle.ConstantTimeCompare([]byte(req.Token), []byte(tok)) != 1 {
			authFail(w, 403, errors.New("invalid or used setup link; see the daemon log"))
			return
		}
		u, err := a.store.Create(req.Name, accounts.RoleAdmin, req.Password)
		if err != nil {
			authFail(w, 400, err)
			return
		}
		a.mu.Lock()
		a.setupToken = ""
		a.mu.Unlock()
		_ = os.Remove(a.linkFile)
		a.log.Info("web interface: first administrator created", "user", u.Name)
		a.newSession(w, u)
		writeJSON(w, 200, viewOf(u))
	})
	mux.HandleFunc("POST /auth/login", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Name, Password string }
		if err := decode(r, &req); err != nil {
			authFail(w, 400, err)
			return
		}
		key := clientIP(r) + "|" + strings.ToLower(req.Name)
		if err := a.throttle(key); err != nil {
			authFail(w, 429, err)
			return
		}
		u, err := a.store.Authenticate(req.Name, req.Password)
		if err != nil {
			a.failed(key)
			a.log.Warn("web interface: failed login", "user", req.Name, "ip", clientIP(r))
			authFail(w, 401, err)
			return
		}
		a.newSession(w, u)
		writeJSON(w, 200, viewOf(u))
	})
	mux.HandleFunc("POST /auth/logout", func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie(sessionCookie); err == nil {
			a.mu.Lock()
			delete(a.sessions, sha256.Sum256([]byte(c.Value)))
			a.mu.Unlock()
		}
		http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: a.secure})
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /auth/passkey/begin", func(w http.ResponseWriter, r *http.Request) {
		opts, data, err := a.wa.BeginDiscoverableLogin()
		if err != nil {
			authFail(w, 500, err)
			return
		}
		a.saveCeremony(w, data, 0)
		writeJSON(w, 200, opts)
	})
	mux.HandleFunc("POST /auth/passkey/finish", func(w http.ResponseWriter, r *http.Request) {
		cer, err := a.takeCeremony(w, r)
		if err != nil {
			authFail(w, 400, err)
			return
		}
		user, cred, err := a.wa.FinishPasskeyLogin(func(rawID, handle []byte) (webauthn.User, error) {
			id, ok := accounts.UserIDFromHandle(handle)
			if !ok {
				return nil, errors.New("unknown passkey")
			}
			u, err := a.store.Get(id)
			if err != nil || u.Disabled {
				return nil, errors.New("unknown passkey")
			}
			return u, nil
		}, cer.data, r)
		if err != nil {
			a.log.Warn("web interface: failed passkey login", "ip", clientIP(r), "err", err)
			authFail(w, 401, errors.New("this passkey is not registered here"))
			return
		}
		u := user.(*accounts.User)
		_ = a.store.UsedPasskey(u.ID, cred)
		a.newSession(w, u)
		writeJSON(w, 200, viewOf(u))
	})

	// The account of the logged-in user.
	mux.HandleFunc("GET /api/account", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, viewOf(userOf(r))) })
	mux.HandleFunc("POST /api/account/password", func(w http.ResponseWriter, r *http.Request) {
		u := userOf(r)
		var req struct{ Current, New string }
		if err := decode(r, &req); err != nil {
			authFail(w, 400, err)
			return
		}
		if u.HasPassword() && !accounts.VerifyPassword(u.PasswordHash, req.Current) {
			authFail(w, 403, errors.New("the current password is wrong"))
			return
		}
		if err := a.store.SetPassword(u.ID, req.New); err != nil {
			authFail(w, 400, err)
			return
		}
		c, _ := r.Cookie(sessionCookie)
		a.endSessions(u.ID, c.Value)
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /api/account/passkeys/begin", func(w http.ResponseWriter, r *http.Request) {
		u := userOf(r)
		var exclude []protocol.CredentialDescriptor
		for _, c := range u.WebAuthnCredentials() {
			exclude = append(exclude, c.Descriptor())
		}
		opts, data, err := a.wa.BeginRegistration(u, webauthn.WithExclusions(exclude),
			webauthn.WithResidentKeyRequirement(protocol.ResidentKeyRequirementRequired))
		if err != nil {
			authFail(w, 500, err)
			return
		}
		a.saveCeremony(w, data, u.ID)
		writeJSON(w, 200, opts)
	})
	// The passkey name comes in the query: FinishRegistration reads the body.
	mux.HandleFunc("POST /api/account/passkeys/finish", func(w http.ResponseWriter, r *http.Request) {
		u := userOf(r)
		cer, err := a.takeCeremony(w, r)
		if err != nil || cer.userID != u.ID {
			authFail(w, 400, errors.New("passkey request expired; try again"))
			return
		}
		cred, err := a.wa.FinishRegistration(u, cer.data, r)
		if err != nil {
			authFail(w, 400, fmt.Errorf("passkey not accepted: %v", err))
			return
		}
		if err := a.store.AddPasskey(u.ID, r.URL.Query().Get("name"), cred); err != nil {
			authFail(w, 500, err)
			return
		}
		nu, _ := a.store.Get(u.ID)
		writeJSON(w, 200, viewOf(nu))
	})
	mux.HandleFunc("DELETE /api/account/passkeys/{id}", func(w http.ResponseWriter, r *http.Request) {
		if err := a.store.RemovePasskey(userOf(r).ID, r.PathValue("id")); err != nil {
			authFail(w, 400, err)
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	})

	// User administration (admins only, enforced in the guard).
	mux.HandleFunc("GET /api/users", func(w http.ResponseWriter, r *http.Request) {
		var out []userView
		for _, u := range a.store.List() {
			out = append(out, viewOf(u))
		}
		writeJSON(w, 200, out)
	})
	mux.HandleFunc("POST /api/users", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Name, Role, Password string }
		if err := decode(r, &req); err != nil {
			authFail(w, 400, err)
			return
		}
		if req.Password == "" {
			authFail(w, 400, errors.New("set an initial password; the user can add passkeys after signing in"))
			return
		}
		u, err := a.store.Create(req.Name, req.Role, req.Password)
		if err != nil {
			authFail(w, 400, err)
			return
		}
		a.log.Info("web interface: user created", "user", u.Name, "role", u.Role, "by", userOf(r).Name)
		writeJSON(w, 200, viewOf(u))
	})
	mux.HandleFunc("PATCH /api/users/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseUint(r.PathValue("id"), 10, 64)
		var req struct {
			Role          *string
			Disabled      *bool
			Password      *string
			ResetPasskeys bool `json:"reset_passkeys"`
		}
		if err := decode(r, &req); err != nil {
			authFail(w, 400, err)
			return
		}
		u, err := a.store.Update(id, func(u *accounts.User) error {
			if req.Role != nil {
				if *req.Role != accounts.RoleAdmin && *req.Role != accounts.RoleUser {
					return fmt.Errorf("unknown role %q", *req.Role)
				}
				u.Role = *req.Role
			}
			if req.Disabled != nil {
				u.Disabled = *req.Disabled
			}
			if req.Password != nil {
				if err := accounts.ValidatePassword(*req.Password); err != nil {
					return err
				}
				u.PasswordHash = accounts.HashPassword(*req.Password)
			}
			if req.ResetPasskeys {
				if !u.HasPassword() {
					return errors.New("set a password before removing all passkeys")
				}
				u.Passkeys = nil
			}
			return nil
		})
		if err != nil {
			authFail(w, 400, err)
			return
		}
		if u.Disabled || req.Password != nil || req.ResetPasskeys {
			a.endSessions(u.ID, "")
		}
		a.log.Info("web interface: user changed", "user", u.Name, "by", userOf(r).Name)
		writeJSON(w, 200, viewOf(u))
	})
	mux.HandleFunc("DELETE /api/users/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseUint(r.PathValue("id"), 10, 64)
		if id == userOf(r).ID {
			authFail(w, 400, errors.New("you cannot delete your own account"))
			return
		}
		if err := a.store.Delete(id); err != nil {
			authFail(w, 400, err)
			return
		}
		a.endSessions(id, "")
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
}

// adminOnly lists what plain users may not do: change configuration,
// repositories and users, delete snapshots, see secrets, or connect to and
// restore databases.
func adminOnly(r *http.Request) bool {
	p, m := r.URL.Path, r.Method
	switch {
	case strings.HasPrefix(p, "/api/users"), strings.HasPrefix(p, "/api/config/yaml"), p == "/api/wizard/devices", strings.HasPrefix(p, "/api/databases/"):
		return true
	case p == "/api/config" && m != http.MethodGet:
		return true
	case strings.HasPrefix(p, "/api/repos/") && (strings.HasSuffix(p, "/init") || strings.HasSuffix(p, "/task/prune") || strings.HasSuffix(p, "/password-file") || strings.HasSuffix(p, "/database-restore")):
		return true
	case strings.HasPrefix(p, "/api/repos/") && strings.Contains(p, "/snapshots/") && m == http.MethodDelete:
		return true
	}
	return false
}

// publicPath can be fetched without a login.
func publicPath(p string) bool {
	switch p {
	case "/login.html", "/login.js", "/icons.js", "/app.css", "/favicon.svg":
		return true
	}
	return strings.HasPrefix(p, "/auth/")
}

// guard protects the web mode: login, roles, Host and CSRF checks.
func (a *webAuth) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		if !strings.EqualFold(host, a.base.Hostname()) {
			if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
				http.Error(w, "unknown host", http.StatusMisdirectedRequest)
				return
			}
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Header.Get("X-Herakles") != "1" {
			// Browsers cannot add custom headers to cross-site requests.
			writeJSON(w, 403, apiError{Error: "missing X-Herakles header"})
			return
		}
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:")
		if a.secure {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000")
		}
		if publicPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		u := a.current(r)
		if u == nil {
			if strings.HasPrefix(r.URL.Path, "/api/") {
				writeJSON(w, 401, apiError{Error: "please sign in again"})
				return
			}
			http.Redirect(w, r, "/login.html", http.StatusFound)
			return
		}
		if !u.IsAdmin() && adminOnly(r) {
			writeJSON(w, 403, apiError{Error: "only administrators can do this"})
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, u)))
	})
}

// redactedConfig returns the configuration without secrets.
func redactedConfig(v any) any {
	b, err := json.Marshal(v)
	if err != nil {
		return v
	}
	var m any
	if json.Unmarshal(b, &m) != nil {
		return v
	}
	var walk func(any)
	walk = func(x any) {
		switch t := x.(type) {
		case map[string]any:
			for k, val := range t {
				switch k {
				case "password", "smtp_password", "password_hash":
					if s, ok := val.(string); ok && s != "" {
						t[k] = "***"
					}
				default:
					walk(val)
				}
			}
		case []any:
			for _, e := range t {
				walk(e)
			}
		}
	}
	walk(m)
	return m
}
