// Package accounts stores the users of the Herakles web interface: names,
// roles, Argon2id password hashes and passkeys (WebAuthn credentials), in a
// JSON file readable only by its owner.
package accounts

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
	"golang.org/x/crypto/argon2"
)

// Roles.
const (
	RoleAdmin = "admin" // everything, including configuration and users
	RoleUser  = "user"  // run backups, browse, restore
)

// Errors.
var (
	ErrNotFound    = errors.New("user not found")
	ErrExists      = errors.New("a user with this name already exists")
	ErrBadPassword = errors.New("wrong username or password")
	ErrLastAdmin   = errors.New("the last administrator cannot be removed or demoted")
)

// Passkey is a registered WebAuthn credential.
type Passkey struct {
	ID         string              `json:"id"` // base64url credential ID
	Name       string              `json:"name"`
	Credential webauthn.Credential `json:"credential"`
	Created    time.Time           `json:"created"`
	LastUsed   time.Time           `json:"last_used,omitempty"`
}

// User is an account.
type User struct {
	ID           uint64     `json:"id"`
	Name         string     `json:"name"`
	Role         string     `json:"role"`
	PasswordHash string     `json:"password_hash,omitempty"`
	Disabled     bool       `json:"disabled,omitempty"`
	Passkeys     []*Passkey `json:"passkeys,omitempty"`
	Created      time.Time  `json:"created"`
	LastLogin    time.Time  `json:"last_login,omitempty"`
}

// IsAdmin reports whether the user is an administrator.
func (u *User) IsAdmin() bool { return u.Role == RoleAdmin }

// HasPassword reports whether a password is set.
func (u *User) HasPassword() bool { return u.PasswordHash != "" }

// WebAuthn adapter.
func (u *User) WebAuthnID() []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, u.ID)
	return b
}
func (u *User) WebAuthnName() string        { return u.Name }
func (u *User) WebAuthnDisplayName() string { return u.Name }
func (u *User) WebAuthnCredentials() []webauthn.Credential {
	out := make([]webauthn.Credential, 0, len(u.Passkeys))
	for _, p := range u.Passkeys {
		out = append(out, p.Credential)
	}
	return out
}

// UserIDFromHandle decodes a WebAuthn user handle.
func UserIDFromHandle(h []byte) (uint64, bool) {
	if len(h) != 8 {
		return 0, false
	}
	return binary.BigEndian.Uint64(h), true
}

type file struct {
	NextID uint64  `json:"next_id"`
	Users  []*User `json:"users"`
}

// Store is the account database.
type Store struct {
	path string
	mu   sync.Mutex
	data file
	// mtime/size of the file as last read or written: the CLI may change
	// the file while the daemon runs.
	mtime time.Time
	size  int64
}

// Open loads (or creates) the store at path.
func Open(path string) (*Store, error) {
	s := &Store{path: path, data: file{NextID: 1}}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) load() error {
	st, err := os.Stat(s.path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil
	case err != nil:
		return err
	}
	b, err := os.ReadFile(s.path)
	if err != nil {
		return err
	}
	var d file
	if err := json.Unmarshal(b, &d); err != nil {
		return fmt.Errorf("%s: %w", s.path, err)
	}
	if d.NextID == 0 {
		d.NextID = 1
	}
	s.data, s.mtime, s.size = d, st.ModTime(), st.Size()
	return nil
}

// refresh reloads the file if another process changed it. Callers hold mu.
func (s *Store) refresh() {
	if st, err := os.Stat(s.path); err == nil && (!st.ModTime().Equal(s.mtime) || st.Size() != s.size) {
		_ = s.load()
	}
}

func (s *Store) save() error {
	b, err := json.MarshalIndent(s.data, "", "  ")
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
	if err := os.Rename(tmp, s.path); err != nil {
		return err
	}
	if st, err := os.Stat(s.path); err == nil {
		s.mtime, s.size = st.ModTime(), st.Size()
	}
	return nil
}

// clone returns a deep copy so callers never share state with the store.
func clone(u *User) *User {
	c := *u
	c.Passkeys = nil
	for _, p := range u.Passkeys {
		pc := *p
		c.Passkeys = append(c.Passkeys, &pc)
	}
	return &c
}

// Empty reports whether no user exists (setup mode).
func (s *Store) Empty() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refresh()
	return len(s.data.Users) == 0
}

// List returns all users.
func (s *Store) List() []*User {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refresh()
	out := make([]*User, 0, len(s.data.Users))
	for _, u := range s.data.Users {
		out = append(out, clone(u))
	}
	return out
}

func (s *Store) find(fn func(*User) bool) *User {
	for _, u := range s.data.Users {
		if fn(u) {
			return u
		}
	}
	return nil
}

// Get returns a user by ID.
func (s *Store) Get(id uint64) (*User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refresh()
	if u := s.find(func(u *User) bool { return u.ID == id }); u != nil {
		return clone(u), nil
	}
	return nil, ErrNotFound
}

// ByName returns a user by (case-insensitive) name.
func (s *Store) ByName(name string) (*User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refresh()
	if u := s.find(func(u *User) bool { return strings.EqualFold(u.Name, name) }); u != nil {
		return clone(u), nil
	}
	return nil, ErrNotFound
}

var validName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._@-]{0,63}$`)

// ValidatePassword enforces a minimum length.
func ValidatePassword(pw string) error {
	if len([]rune(pw)) < 10 {
		return errors.New("passwords need at least 10 characters")
	}
	return nil
}

// Create adds a user. password may be empty (passkey-only account).
func (s *Store) Create(name, role, password string) (*User, error) {
	if !validName.MatchString(name) {
		return nil, errors.New("user names are 1-64 letters, digits and . _ @ -")
	}
	if role != RoleAdmin && role != RoleUser {
		return nil, fmt.Errorf("unknown role %q", role)
	}
	var hash string
	if password != "" {
		if err := ValidatePassword(password); err != nil {
			return nil, err
		}
		hash = HashPassword(password)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refresh()
	if s.find(func(u *User) bool { return strings.EqualFold(u.Name, name) }) != nil {
		return nil, ErrExists
	}
	if len(s.data.Users) == 0 && role != RoleAdmin {
		return nil, errors.New("the first account must be an administrator")
	}
	u := &User{ID: s.data.NextID, Name: name, Role: role, PasswordHash: hash, Created: time.Now().UTC()}
	s.data.NextID++
	s.data.Users = append(s.data.Users, u)
	if err := s.save(); err != nil {
		s.data.Users = s.data.Users[:len(s.data.Users)-1]
		return nil, err
	}
	return clone(u), nil
}

// Update changes a user under the store lock and saves.
func (s *Store) Update(id uint64, fn func(u *User) error) (*User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refresh()
	u := s.find(func(u *User) bool { return u.ID == id })
	if u == nil {
		return nil, ErrNotFound
	}
	before := clone(u)
	if err := fn(u); err != nil {
		*u = *before
		return nil, err
	}
	if !s.hasActiveAdmin() {
		*u = *before
		return nil, ErrLastAdmin
	}
	if err := s.save(); err != nil {
		*u = *before
		return nil, err
	}
	return clone(u), nil
}

// hasActiveAdmin also guards against deleting the last user, which would
// reopen the setup.
func (s *Store) hasActiveAdmin() bool {
	return s.find(func(u *User) bool { return u.IsAdmin() && !u.Disabled }) != nil
}

// Delete removes a user.
func (s *Store) Delete(id uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refresh()
	i := slices.IndexFunc(s.data.Users, func(u *User) bool { return u.ID == id })
	if i < 0 {
		return ErrNotFound
	}
	removed := s.data.Users[i]
	s.data.Users = slices.Delete(s.data.Users, i, i+1)
	if !s.hasActiveAdmin() {
		s.data.Users = slices.Insert(s.data.Users, i, removed)
		return ErrLastAdmin
	}
	if err := s.save(); err != nil {
		s.data.Users = slices.Insert(s.data.Users, i, removed)
		return err
	}
	return nil
}

// Authenticate checks a name and password. Unknown users take as long as
// wrong passwords.
func (s *Store) Authenticate(name, password string) (*User, error) {
	u, err := s.ByName(name)
	if err != nil || u.Disabled || !u.HasPassword() {
		_ = VerifyPassword(dummyHash, password)
		return nil, ErrBadPassword
	}
	if !VerifyPassword(u.PasswordHash, password) {
		return nil, ErrBadPassword
	}
	return u, nil
}

// SetPassword sets (or with "" removes) a user's password.
func (s *Store) SetPassword(id uint64, password string) error {
	_, err := s.Update(id, func(u *User) error {
		if password == "" {
			if len(u.Passkeys) == 0 {
				return errors.New("an account without passkeys needs a password")
			}
			u.PasswordHash = ""
			return nil
		}
		if err := ValidatePassword(password); err != nil {
			return err
		}
		u.PasswordHash = HashPassword(password)
		return nil
	})
	return err
}

// AddPasskey stores a new credential.
func (s *Store) AddPasskey(id uint64, name string, c *webauthn.Credential) error {
	_, err := s.Update(id, func(u *User) error {
		if name = strings.TrimSpace(name); name == "" {
			name = "Passkey"
		}
		u.Passkeys = append(u.Passkeys, &Passkey{ID: base64.RawURLEncoding.EncodeToString(c.ID), Name: name, Credential: *c, Created: time.Now().UTC()})
		return nil
	})
	return err
}

// RemovePasskey deletes a credential.
func (s *Store) RemovePasskey(id uint64, credID string) error {
	_, err := s.Update(id, func(u *User) error {
		i := slices.IndexFunc(u.Passkeys, func(p *Passkey) bool { return p.ID == credID })
		if i < 0 {
			return ErrNotFound
		}
		if len(u.Passkeys) == 1 && !u.HasPassword() {
			return errors.New("this is the only way to sign in; set a password first")
		}
		u.Passkeys = slices.Delete(u.Passkeys, i, i+1)
		return nil
	})
	return err
}

// UsedPasskey records a login with a credential (sign count, flags).
func (s *Store) UsedPasskey(id uint64, c *webauthn.Credential) error {
	_, err := s.Update(id, func(u *User) error {
		cid := base64.RawURLEncoding.EncodeToString(c.ID)
		for _, p := range u.Passkeys {
			if p.ID == cid {
				p.Credential = *c
				p.LastUsed = time.Now().UTC()
				u.LastLogin = p.LastUsed
				return nil
			}
		}
		return ErrNotFound
	})
	return err
}

// LoggedIn records the time of a login.
func (s *Store) LoggedIn(id uint64) {
	_, _ = s.Update(id, func(u *User) error { u.LastLogin = time.Now().UTC(); return nil })
}

// Argon2id parameters (OWASP recommendation: 19 MiB, t=2, p=1).
var argonTime, argonMem, argonThreads uint32 = 2, 19 * 1024, 1

// HashPassword returns a PHC formatted Argon2id hash.
func HashPassword(pw string) string {
	salt := make([]byte, 16)
	_, _ = rand.Read(salt)
	key := argon2.IDKey([]byte(pw), salt, argonTime, argonMem, uint8(argonThreads), 32)
	b64 := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", argonMem, argonTime, argonThreads, b64.EncodeToString(salt), b64.EncodeToString(key))
}

// VerifyPassword checks a password against a hash from HashPassword.
func VerifyPassword(hash, pw string) bool {
	parts := strings.Split(hash, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var m, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return false
	}
	b64 := base64.RawStdEncoding
	salt, err1 := b64.DecodeString(parts[4])
	want, err2 := b64.DecodeString(parts[5])
	if err1 != nil || err2 != nil {
		return false
	}
	got := argon2.IDKey([]byte(pw), salt, t, m, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

var dummyHash = HashPassword("timing-equaliser")

// Path returns the file the store lives in.
func (s *Store) Path() string { return s.path }
