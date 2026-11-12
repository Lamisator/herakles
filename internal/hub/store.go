// Package hub lets servers attach to a central Herakles (the hub) that
// manages them: an attached server keeps one connection open to the hub's
// web interface, and the hub's GUI controls it through that connection.
// The hub also passes its shared repositories, with their passwords, to
// every attached server.
package hub

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// TokenTTL is how long a join token is valid by default.
const TokenTTL = 24 * time.Hour

// Server is an attached server as the hub knows it.
type Server struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	// PublicKey is the server's own identity key; it signs every login.
	PublicKey []byte `json:"public_key"`
	Created    time.Time `json:"created"`
	LastSeen   time.Time `json:"last_seen,omitempty"`
	Address    string    `json:"address,omitempty"`
	Hello      Hello     `json:"hello"`
}

// Hello describes an attached server; it reports it on every connection.
type Hello struct {
	Hostname  string `json:"hostname"`
	Version   string `json:"version"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	Container string `json:"container,omitempty"`
}

// Token is an unused invitation to attach a server.
type Token struct {
	Hash    string    `json:"hash"`
	Name    string    `json:"name,omitempty"`
	Expires time.Time `json:"expires"`
}

type storeData struct {
	Servers []*Server `json:"servers"`
	Tokens  []*Token  `json:"tokens,omitempty"`
}

// Store keeps the attached servers and join tokens in a file (0600). It
// is read on every access, so 'herakles hub' commands and the daemon can
// change it side by side.
type Store struct {
	path string
	mu   sync.Mutex
}

// OpenStore uses the file at path (created on the first change).
func OpenStore(path string) *Store { return &Store{path: path} }

// StorePath is the store of a state directory.
func StorePath(stateDir string) string { return filepath.Join(stateDir, "hub", "servers.json") }

func (s *Store) load() (*storeData, error) {
	d := &storeData{}
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return d, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, d); err != nil {
		return nil, fmt.Errorf("%s: %w", s.path, err)
	}
	return d, nil
}

func (s *Store) save(d *storeData) error {
	now := time.Now()
	keep := d.Tokens[:0]
	for _, t := range d.Tokens {
		if now.Before(t.Expires) {
			keep = append(keep, t)
		}
	}
	d.Tokens = keep
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *Store) update(fn func(d *storeData) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := s.load()
	if err != nil {
		return err
	}
	if err := fn(d); err != nil {
		return err
	}
	return s.save(d)
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func hash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// identityPath holds the hub's identity key (Ed25519 seed, 0600).
func (s *Store) identityPath() string { return filepath.Join(filepath.Dir(s.path), "identity.key") }

// Identity returns the hub's identity key, created on first use. Servers
// pin its public key at enrollment and accept only answers signed with it.
func (s *Store) Identity() (ed25519.PrivateKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.identityPath()
	b, err := os.ReadFile(p)
	if err == nil {
		seed, err := hex.DecodeString(strings.TrimSpace(string(b)))
		if err != nil || len(seed) != ed25519.SeedSize {
			return nil, fmt.Errorf("%s: invalid key", p)
		}
		return ed25519.NewKeyFromSeed(seed), nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	_, err = f.WriteString(hex.EncodeToString(key.Seed()) + "\n")
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return key, err
}

// NewEnrollment creates a one-time enrollment secret and returns the code
// that carries it with the hub's address and identity. name, if set,
// becomes the server's name on the hub.
func (s *Store) NewEnrollment(hubURL, name string, ttl time.Duration, tlsPin []byte) (Code, error) {
	if ttl <= 0 {
		ttl = TokenTTL
	}
	key, err := s.Identity()
	if err != nil {
		return Code{}, err
	}
	secret := randomHex(20)
	exp := time.Now().Add(ttl).Truncate(time.Second)
	err = s.update(func(d *storeData) error {
		d.Tokens = append(d.Tokens, &Token{Hash: hash(secret), Name: name, Expires: exp})
		return nil
	})
	return Code{URL: strings.TrimSuffix(hubURL, "/"), Secret: secret, HubKey: key.Public().(ed25519.PublicKey),
		TLSPin: tlsPin, Name: name, Expires: exp.Unix()}, err
}

// ErrBadToken is returned for unknown, used or expired enrollment secrets.
var ErrBadToken = errors.New("the enrollment code is invalid, used or expired; create a new one on the hub")

// Join uses an enrollment secret and registers a server with its public
// key.
func (s *Store) Join(secret string, pub ed25519.PublicKey, hello Hello) (*Server, error) {
	if len(pub) != ed25519.PublicKeySize {
		return nil, errors.New("invalid public key")
	}
	var srv *Server
	err := s.update(func(d *storeData) error {
		h := hash(secret)
		idx := -1
		for i, t := range d.Tokens {
			if subtle.ConstantTimeCompare([]byte(t.Hash), []byte(h)) == 1 && time.Now().Before(t.Expires) {
				idx = i
			}
		}
		if idx < 0 {
			return ErrBadToken
		}
		name := d.Tokens[idx].Name
		d.Tokens = append(d.Tokens[:idx], d.Tokens[idx+1:]...)
		if name == "" {
			name = hello.Hostname
		}
		name = uniqueName(d.Servers, name)
		srv = &Server{ID: randomHex(8), Name: name, PublicKey: pub, Created: time.Now(), Hello: hello}
		d.Servers = append(d.Servers, srv)
		return nil
	})
	return srv, err
}

func uniqueName(servers []*Server, name string) string {
	if name == "" {
		name = "server"
	}
	taken := map[string]bool{}
	for _, s := range servers {
		taken[s.Name] = true
	}
	out := name
	for i := 2; taken[out]; i++ {
		out = fmt.Sprintf("%s-%d", name, i)
	}
	return out
}

// Get returns a server by ID.
func (s *Store) Get(id string) (*Server, error) {
	all, err := s.Servers()
	if err != nil {
		return nil, err
	}
	for _, srv := range all {
		if srv.ID == id {
			return srv, nil
		}
	}
	return nil, errors.New("this server is not attached to the hub (removed?)")
}

// Seen records a connection of a server.
func (s *Store) Seen(id, addr string, hello Hello) error {
	return s.update(func(d *storeData) error {
		for _, srv := range d.Servers {
			if srv.ID == id {
				srv.LastSeen = time.Now()
				srv.Address = addr
				srv.Hello = hello
			}
		}
		return nil
	})
}

// Servers lists the attached servers by name.
func (s *Store) Servers() ([]*Server, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := s.load()
	if err != nil {
		return nil, err
	}
	sort.Slice(d.Servers, func(i, j int) bool { return d.Servers[i].Name < d.Servers[j].Name })
	return d.Servers, nil
}

// Find returns a server by ID or name.
func (s *Store) Find(key string) (*Server, error) {
	all, err := s.Servers()
	if err != nil {
		return nil, err
	}
	for _, srv := range all {
		if srv.ID == key || srv.Name == key {
			return srv, nil
		}
	}
	return nil, fmt.Errorf("no attached server %q", key)
}

// Rename changes a server's name.
func (s *Store) Rename(id, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("the name is empty")
	}
	return s.update(func(d *storeData) error {
		for _, srv := range d.Servers {
			if srv.ID != id && srv.Name == name {
				return fmt.Errorf("another server is called %q", name)
			}
		}
		for _, srv := range d.Servers {
			if srv.ID == id {
				srv.Name = name
				return nil
			}
		}
		return fmt.Errorf("no attached server %q", id)
	})
}

// Remove detaches a server: its login stops working.
func (s *Store) Remove(id string) error {
	return s.update(func(d *storeData) error {
		for i, srv := range d.Servers {
			if srv.ID == id {
				d.Servers = append(d.Servers[:i], d.Servers[i+1:]...)
				return nil
			}
		}
		return fmt.Errorf("no attached server %q", id)
	})
}
