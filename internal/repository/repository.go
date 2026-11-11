// Package repository implements the Herakles repository format: encrypted,
// compressed, deduplicated blobs stored in pack files, an index mapping blobs
// to packs, encrypted snapshots and multiple key envelopes.
package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"time"

	"github.com/lamisator/herakles/internal/backend"
	"github.com/lamisator/herakles/internal/chunker"
	"github.com/lamisator/herakles/internal/compress"
	"github.com/lamisator/herakles/internal/crypto"
	"github.com/lamisator/herakles/internal/id"
)

// FormatVersion is the current repository format version.
const FormatVersion = 1

// Config is the plaintext repository configuration file.
type Config struct {
	Version int            `json:"version"`
	ID      string         `json:"id"`
	Created time.Time      `json:"created"`
	Chunker chunker.Params `json:"chunker"`
}

// Options tune client-side behaviour of an opened repository.
type Options struct {
	Compression compress.Mode
	PackSize    int // target pack size in bytes
	Connections int // number of concurrent packers/uploads
}

// DefaultOptions returns sensible defaults.
func DefaultOptions() Options {
	n := runtime.NumCPU()
	if n < 2 {
		n = 2
	}
	if n > 8 {
		n = 8
	}
	return Options{Compression: compress.Mode{Algorithm: compress.Zstd, Level: 2, Auto: true}, PackSize: 16 << 20, Connections: n}
}

// Repository is an opened, unlocked repository.
type Repository struct {
	be      backend.Backend
	cfg     Config
	key     *crypto.Key
	keyName string
	gear    *chunker.Table
	opts    Options
	idx     *Index

	packers  chan *packer
	pmu      sync.Mutex
	pending  id.Set
	unsaved  []PackInfo // uploaded packs not yet written to an index file
	treeMu   sync.Mutex
	trees    map[id.ID]*Tree
	treeList []id.ID
}

// Backend returns the underlying backend.
func (r *Repository) Backend() backend.Backend { return r.be }

// Config returns the repository configuration.
func (r *Repository) Config() Config { return r.cfg }

// Key returns the master key.
func (r *Repository) Key() *crypto.Key { return r.key }

// KeyName returns the name of the key envelope used to open the repository.
func (r *Repository) KeyName() string { return r.keyName }

// Index returns the in-memory index.
func (r *Repository) Index() *Index { return r.idx }

// ChunkerParams returns the chunker parameters.
func (r *Repository) ChunkerParams() chunker.Params { return r.cfg.Chunker }

// GearTable returns the keyed chunker table.
func (r *Repository) GearTable() *chunker.Table { return r.gear }

// Options returns the client options.
func (r *Repository) Options() Options { return r.opts }

// SetCompression changes the compression mode for new blobs.
func (r *Repository) SetCompression(m compress.Mode) { r.opts.Compression = m }

// Init creates a new repository on be protected by creds.
func Init(ctx context.Context, be backend.Backend, creds crypto.Credentials, params chunker.Params) (*Repository, error) {
	if err := params.Validate(); err != nil {
		return nil, err
	}
	if _, err := be.Stat(ctx, backend.Handle{Type: backend.ConfigFile}); err == nil {
		return nil, errors.New("repository already initialized")
	}
	if err := be.Create(ctx); err != nil {
		return nil, fmt.Errorf("create repository: %w", err)
	}
	master := crypto.NewRandomKey()
	env, err := crypto.Wrap(master, creds, "initial key")
	if err != nil {
		return nil, err
	}
	keyName, err := saveEnvelope(ctx, be, env)
	if err != nil {
		return nil, err
	}
	cfg := Config{Version: FormatVersion, ID: id.Random().String(), Created: time.Now().UTC(), Chunker: params}
	raw, _ := json.MarshalIndent(cfg, "", "  ")
	if err := be.Save(ctx, backend.Handle{Type: backend.ConfigFile}, raw); err != nil {
		return nil, fmt.Errorf("save config: %w", err)
	}
	return newRepo(be, cfg, master, keyName, DefaultOptions()), nil
}

func saveEnvelope(ctx context.Context, be backend.Backend, env *crypto.KeyEnvelope) (string, error) {
	raw, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		return "", err
	}
	name := id.Hash(raw).String()
	if err := be.Save(ctx, backend.Handle{Type: backend.KeyFile, Name: name}, raw); err != nil {
		return "", fmt.Errorf("save key: %w", err)
	}
	return name, nil
}

func newRepo(be backend.Backend, cfg Config, key *crypto.Key, keyName string, opts Options) *Repository {
	r := &Repository{be: be, cfg: cfg, key: key, keyName: keyName, gear: chunker.NewTable(key.ChunkerSeed), idx: NewIndex(), pending: id.Set{}, trees: map[id.ID]*Tree{}}
	r.applyOptions(opts)
	return r
}

func (r *Repository) applyOptions(opts Options) {
	if opts.PackSize <= 0 {
		opts.PackSize = DefaultOptions().PackSize
	}
	if opts.Connections <= 0 {
		opts.Connections = DefaultOptions().Connections
	}
	r.opts = opts
	r.packers = make(chan *packer, opts.Connections)
	for i := 0; i < opts.Connections; i++ {
		r.packers <- &packer{}
	}
}

// ErrNotARepository is returned when no repository exists at a location.
var ErrNotARepository = errors.New("no repository found (did you run 'herakles init'?)")

// LoadConfig reads the plaintext repository config.
func LoadConfig(ctx context.Context, be backend.Backend) (Config, error) {
	var cfg Config
	raw, err := backend.LoadAll(ctx, be, backend.Handle{Type: backend.ConfigFile})
	if err != nil {
		if backend.IsNotFound(err) {
			return cfg, ErrNotARepository
		}
		return cfg, err
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return cfg, fmt.Errorf("parse repository config: %w", err)
	}
	if cfg.Version != FormatVersion {
		return cfg, fmt.Errorf("unsupported repository version %d", cfg.Version)
	}
	return cfg, cfg.Chunker.Validate()
}

// Open unlocks the repository on be with creds and loads the index.
func Open(ctx context.Context, be backend.Backend, creds crypto.Credentials, opts Options) (*Repository, error) {
	cfg, err := LoadConfig(ctx, be)
	if err != nil {
		return nil, err
	}
	if creds.Empty() {
		return nil, errors.New("a password or key file is required to open the repository")
	}
	key, keyName, err := unlock(ctx, be, creds)
	if err != nil {
		return nil, err
	}
	r := newRepo(be, cfg, key, keyName, opts)
	if err := r.LoadIndex(ctx); err != nil {
		return nil, err
	}
	return r, nil
}

func unlock(ctx context.Context, be backend.Backend, creds crypto.Credentials) (*crypto.Key, string, error) {
	var names []string
	if err := be.List(ctx, backend.KeyFile, func(fi backend.FileInfo) error {
		names = append(names, fi.Name)
		return nil
	}); err != nil {
		return nil, "", err
	}
	if len(names) == 0 {
		return nil, "", errors.New("repository has no keys")
	}
	for _, n := range names {
		env, err := loadEnvelope(ctx, be, n)
		if err != nil {
			continue
		}
		if env.UsePassword != creds.HasPassword() || env.UseKeyFile != creds.HasKeyFile() {
			continue
		}
		k, err := env.Unwrap(creds)
		if err == nil {
			return k, n, nil
		}
	}
	return nil, "", crypto.ErrWrongCredentials
}

func loadEnvelope(ctx context.Context, be backend.Backend, name string) (*crypto.KeyEnvelope, error) {
	raw, err := backend.LoadAll(ctx, be, backend.Handle{Type: backend.KeyFile, Name: name})
	if err != nil {
		return nil, err
	}
	env := &crypto.KeyEnvelope{}
	if err := json.Unmarshal(raw, env); err != nil {
		return nil, err
	}
	return env, nil
}

// KeyInfo describes a key envelope.
type KeyInfo struct {
	Name     string    `json:"name"`
	Current  bool      `json:"current"`
	Created  time.Time `json:"created"`
	Hostname string    `json:"hostname"`
	Username string    `json:"username"`
	Label    string    `json:"label"`
	Kind     string    `json:"kind"`
}

// ListKeys returns all key envelopes.
func (r *Repository) ListKeys(ctx context.Context) ([]KeyInfo, error) {
	var out []KeyInfo
	err := r.be.List(ctx, backend.KeyFile, func(fi backend.FileInfo) error {
		env, err := loadEnvelope(ctx, r.be, fi.Name)
		if err != nil {
			return err
		}
		out = append(out, KeyInfo{Name: fi.Name, Current: fi.Name == r.keyName, Created: env.Created, Hostname: env.Hostname, Username: env.Username, Label: env.Label, Kind: env.Describe()})
		return nil
	})
	return out, err
}

// AddKey wraps the master key with new credentials.
func (r *Repository) AddKey(ctx context.Context, creds crypto.Credentials, label string) (string, error) {
	env, err := crypto.Wrap(r.key, creds, label)
	if err != nil {
		return "", err
	}
	return saveEnvelope(ctx, r.be, env)
}

// RemoveKey deletes a key envelope. The key in use cannot be removed.
func (r *Repository) RemoveKey(ctx context.Context, name string) error {
	if name == r.keyName {
		return errors.New("refusing to remove the key currently in use")
	}
	keys, err := r.ListKeys(ctx)
	if err != nil {
		return err
	}
	var match string
	for _, k := range keys {
		if k.Name == name || (len(name) >= 6 && len(k.Name) > len(name) && k.Name[:len(name)] == name) {
			if match != "" {
				return fmt.Errorf("key prefix %q is ambiguous", name)
			}
			match = k.Name
		}
	}
	if match == "" {
		return fmt.Errorf("key %q not found", name)
	}
	if match == r.keyName {
		return errors.New("refusing to remove the key currently in use")
	}
	return r.be.Remove(ctx, backend.Handle{Type: backend.KeyFile, Name: match})
}

// ChangeKey replaces the current key envelope with one for new credentials.
func (r *Repository) ChangeKey(ctx context.Context, creds crypto.Credentials) error {
	old := r.keyName
	name, err := r.AddKey(ctx, creds, "changed key")
	if err != nil {
		return err
	}
	r.keyName = name
	return r.be.Remove(ctx, backend.Handle{Type: backend.KeyFile, Name: old})
}

// Close flushes nothing; callers must call Flush before closing.
func (r *Repository) Close() error { return r.be.Close() }
