package hub

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/lamisator/herakles/internal/config"
)

// SharedRepository is a repository the hub passes to its servers, with
// the secrets resolved: the servers cannot read the hub's password files.
type SharedRepository struct {
	Repository *config.Repository `json:"repository"`
	Password   string             `json:"password,omitempty"`
	// Files holds the contents of key_file and the remote identity_file,
	// known_hosts and ca_cert.
	Files map[string][]byte `json:"files,omitempty"`
}

// Shared resolves the repositories in cfg.Hub.Repositories.
func Shared(cfg *config.Config) ([]SharedRepository, error) {
	var out []SharedRepository
	for _, name := range cfg.Hub.Repositories {
		rc := cfg.Repo(name)
		if rc == nil {
			return nil, fmt.Errorf("hub: unknown repository %q", name)
		}
		sr, err := share(rc)
		if err != nil {
			return nil, fmt.Errorf("repository %s: %w", name, err)
		}
		out = append(out, sr)
	}
	return out, nil
}

func share(rc *config.Repository) (SharedRepository, error) {
	r := *rc
	sr := SharedRepository{Files: map[string][]byte{}}
	if rc.Password != "" || rc.PasswordFile != "" || rc.PasswordEnv != "" || rc.PasswordCommand != "" {
		c, err := rc.Credentials()
		if err != nil {
			return sr, err
		}
		sr.Password = c.Password
	}
	r.Password, r.PasswordFile, r.PasswordEnv, r.PasswordCommand, r.KeyFile = "", "", "", "", ""
	read := func(key, path string) error {
		if path == "" {
			return nil
		}
		b, err := os.ReadFile(config.ExpandHome(path))
		if err != nil {
			return err
		}
		sr.Files[key] = b
		return nil
	}
	if err := read("key_file", rc.KeyFile); err != nil {
		return sr, err
	}
	if sr.Password == "" && len(sr.Files["key_file"]) == 0 {
		return sr, fmt.Errorf("no password source (the hub cannot pass the password on)")
	}
	r.Remote.Password = rc.BackendOptions().Password
	r.Remote.PasswordEnv = ""
	for key, path := range map[string]string{"identity_file": rc.Remote.IdentityFile, "known_hosts": rc.Remote.KnownHosts, "ca_cert": rc.Remote.CACert} {
		if err := read(key, path); err != nil {
			return sr, err
		}
	}
	r.Remote.IdentityFile, r.Remote.KnownHosts, r.Remote.CACert = "", "", ""
	// The hub keeps the repository clean: pruning and checks run there,
	// not on every server.
	r.Maintenance = config.Maintenance{}
	r.FromHub = true
	sr.Repository = &r
	return sr, nil
}

// SyncResult reports what applying the shared repositories changed.
type SyncResult struct {
	Added     []string `json:"added,omitempty"`
	Updated   []string `json:"updated,omitempty"`
	Removed   []string `json:"removed,omitempty"`
	Conflicts []string `json:"conflicts,omitempty"`
	Kept      []string `json:"kept,omitempty"`
}

// Changed reports whether the configuration changed.
func (r SyncResult) Changed() bool {
	return len(r.Added)+len(r.Updated)+len(r.Removed) > 0
}

// SecretsDir holds the passwords and key files of shared repositories.
func SecretsDir(stateDir string) string { return filepath.Join(stateDir, "hub", "repositories") }

// Apply puts the shared repositories into cfg (marked hub: true): their
// secrets go to files in the state directory. Repositories the hub no
// longer shares are removed unless a job still uses them. A repository of
// this machine with the same name wins over the hub's.
func Apply(cfg *config.Config, shared []SharedRepository) (SyncResult, error) {
	var res SyncResult
	dir := SecretsDir(cfg.GetStateDir())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return res, err
	}
	want := map[string]bool{}
	for _, sr := range shared {
		if sr.Repository == nil || sr.Repository.Name == "" || strings.ContainsAny(sr.Repository.Name, `/\`) || strings.HasPrefix(sr.Repository.Name, ".") {
			continue
		}
		r := *sr.Repository
		name := r.Name
		old := cfg.Repo(name)
		if old != nil && !old.FromHub {
			res.Conflicts = append(res.Conflicts, name)
			continue
		}
		want[name] = true
		r.FromHub = true
		r.Maintenance = config.Maintenance{}
		r.Password, r.PasswordEnv, r.PasswordCommand, r.PasswordFile, r.KeyFile = "", "", "", "", ""
		write := func(suffix string, data []byte) (string, error) {
			p := filepath.Join(dir, name+suffix)
			if cur, err := os.ReadFile(p); err == nil && bytes.Equal(cur, data) {
				return p, nil
			}
			return p, os.WriteFile(p, data, 0o600)
		}
		var err error
		if sr.Password != "" {
			if r.PasswordFile, err = write(".pw", []byte(sr.Password+"\n")); err != nil {
				return res, err
			}
		}
		files := map[string]*string{"key_file": &r.KeyFile, "identity_file": &r.Remote.IdentityFile, "known_hosts": &r.Remote.KnownHosts, "ca_cert": &r.Remote.CACert}
		for key, field := range files {
			*field = ""
			if data := sr.Files[key]; len(data) > 0 {
				if *field, err = write("."+key, data); err != nil {
					return res, err
				}
			}
		}
		switch {
		case old == nil:
			cfg.Repositories = append(cfg.Repositories, &r)
			res.Added = append(res.Added, name)
		case !sameRepo(old, &r):
			*old = r
			res.Updated = append(res.Updated, name)
		}
	}
	keep := cfg.Repositories[:0]
	for _, r := range cfg.Repositories {
		if !r.FromHub || want[r.Name] {
			keep = append(keep, r)
			continue
		}
		if used := jobsUsing(cfg, r.Name); len(used) > 0 {
			res.Kept = append(res.Kept, r.Name+" (used by "+strings.Join(used, ", ")+")")
			keep = append(keep, r)
			continue
		}
		res.Removed = append(res.Removed, r.Name)
		removeSecrets(dir, r.Name)
	}
	cfg.Repositories = keep
	return res, nil
}

func removeSecrets(dir, name string) {
	for _, suffix := range []string{".pw", ".key_file", ".identity_file", ".known_hosts", ".ca_cert"} {
		_ = os.Remove(filepath.Join(dir, name+suffix))
	}
}

// Release turns the hub's repositories into ordinary ones of this machine
// (after leaving the hub): their secrets stay where they are.
func Release(cfg *config.Config) []string {
	var out []string
	for _, r := range cfg.Repositories {
		if r.FromHub {
			r.FromHub = false
			out = append(out, r.Name)
		}
	}
	return out
}

func jobsUsing(cfg *config.Config, repo string) []string {
	var out []string
	for _, j := range cfg.Jobs {
		if j.Repository == repo {
			out = append(out, j.Name)
		}
	}
	return out
}

func sameRepo(a, b *config.Repository) bool {
	x, _ := yaml.Marshal(a)
	y, _ := yaml.Marshal(b)
	return bytes.Equal(x, y)
}
