package crypto

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"golang.org/x/crypto/argon2"
)

// KDFParams configures Argon2id.
type KDFParams struct {
	Time    uint32 `json:"time"`
	Memory  uint32 `json:"memory"` // KiB
	Threads uint8  `json:"threads"`
}

// DefaultKDFParams are used for new keys. Tests may lower them.
var DefaultKDFParams = KDFParams{Time: 3, Memory: 64 * 1024, Threads: 4}

// Credentials are the secrets that unlock a repository: a password, a key
// file, or both.
type Credentials struct {
	Password    string
	KeyFileData []byte
}

// HasPassword reports whether a password is set.
func (c Credentials) HasPassword() bool { return c.Password != "" }

// HasKeyFile reports whether key-file material is set.
func (c Credentials) HasKeyFile() bool { return len(c.KeyFileData) > 0 }

// Empty reports whether no credential material is present.
func (c Credentials) Empty() bool { return !c.HasPassword() && !c.HasKeyFile() }

// LoadKeyFile reads key-file material from path.
func LoadKeyFile(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read key file: %w", err)
	}
	if len(data) < 32 {
		return nil, fmt.Errorf("key file %s is too short (need at least 32 bytes)", path)
	}
	return data, nil
}

// GenerateKeyFile writes a new random key file with mode 0600.
func GenerateKeyFile(path string) error {
	buf := make([]byte, 64)
	mustRand(buf)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(buf); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// KeyEnvelope is the on-disk representation of a master key wrapped by a
// password and/or key file. Several envelopes may wrap the same master key.
type KeyEnvelope struct {
	Created     time.Time `json:"created"`
	Hostname    string    `json:"hostname,omitempty"`
	Username    string    `json:"username,omitempty"`
	Label       string    `json:"label,omitempty"`
	KDF         string    `json:"kdf"`
	Params      KDFParams `json:"params"`
	Salt        []byte    `json:"salt"`
	UsePassword bool      `json:"use_password"`
	UseKeyFile  bool      `json:"use_keyfile"`
	Data        []byte    `json:"data"`
}

func deriveKey(c Credentials, salt []byte, p KDFParams) []byte {
	// Domain-separated encoding of password and key file digest.
	material := []byte("herakles-kdf-v1")
	var l [8]byte
	binary.LittleEndian.PutUint64(l[:], uint64(len(c.Password)))
	material = append(material, l[:]...)
	material = append(material, c.Password...)
	if c.HasKeyFile() {
		sum := sha256.Sum256(c.KeyFileData)
		material = append(material, 1)
		material = append(material, sum[:]...)
	} else {
		material = append(material, 0)
	}
	return argon2.IDKey(material, salt, p.Time, p.Memory, p.Threads, KeySize)
}

// Wrap encrypts the master key with the given credentials.
func Wrap(master *Key, c Credentials, label string) (*KeyEnvelope, error) {
	if c.Empty() {
		return nil, errors.New("a password or key file is required")
	}
	env := &KeyEnvelope{
		Created:     time.Now().UTC(),
		KDF:         "argon2id",
		Params:      DefaultKDFParams,
		Salt:        make([]byte, 32),
		UsePassword: c.HasPassword(),
		UseKeyFile:  c.HasKeyFile(),
		Label:       label,
	}
	mustRand(env.Salt)
	if h, err := os.Hostname(); err == nil {
		env.Hostname = h
	}
	env.Username = currentUser()
	raw, err := json.Marshal(master)
	if err != nil {
		return nil, err
	}
	aead, err := newGCM(deriveKey(c, env.Salt, env.Params))
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, NonceSize)
	mustRand(nonce)
	env.Data = aead.Seal(nonce, nonce, raw, nil)
	return env, nil
}

// ErrWrongCredentials is returned when an envelope cannot be opened.
var ErrWrongCredentials = errors.New("wrong password or key file")

// Unwrap decrypts the master key using the given credentials.
func (env *KeyEnvelope) Unwrap(c Credentials) (*Key, error) {
	if env.KDF != "argon2id" {
		return nil, fmt.Errorf("unsupported KDF %q", env.KDF)
	}
	if env.UsePassword != c.HasPassword() || env.UseKeyFile != c.HasKeyFile() {
		return nil, ErrWrongCredentials
	}
	aead, err := newGCM(deriveKey(c, env.Salt, env.Params))
	if err != nil {
		return nil, err
	}
	if len(env.Data) < NonceSize+aead.Overhead() {
		return nil, ErrWrongCredentials
	}
	raw, err := aead.Open(nil, env.Data[:NonceSize], env.Data[NonceSize:], nil)
	if err != nil {
		return nil, ErrWrongCredentials
	}
	k := &Key{}
	if err := json.Unmarshal(raw, k); err != nil {
		return nil, fmt.Errorf("decode master key: %w", err)
	}
	if err := k.Init(); err != nil {
		return nil, err
	}
	return k, nil
}

// Describe returns a human readable description of the required credentials.
func (env *KeyEnvelope) Describe() string {
	switch {
	case env.UsePassword && env.UseKeyFile:
		return "password+keyfile"
	case env.UseKeyFile:
		return "keyfile"
	default:
		return "password"
	}
}
