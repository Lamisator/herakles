package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/lamisator/herakles/internal/backend"
	"github.com/lamisator/herakles/internal/compress"
	"github.com/lamisator/herakles/internal/id"
)

// Backup types recorded in snapshots.
const (
	TypeFull         = "full"
	TypeIncremental  = "incremental"
	TypeDifferential = "differential"
)

// Summary holds statistics recorded at backup time.
type Summary struct {
	Start           time.Time `json:"start"`
	End             time.Time `json:"end"`
	FilesNew        int64     `json:"files_new"`
	FilesChanged    int64     `json:"files_changed"`
	FilesUnmodified int64     `json:"files_unmodified"`
	DirsNew         int64     `json:"dirs_new"`
	DirsChanged     int64     `json:"dirs_changed"`
	DirsUnmodified  int64     `json:"dirs_unmodified"`
	TotalFiles      int64     `json:"total_files"`
	TotalBytes      int64     `json:"total_bytes"`
	BlobsAdded      int64     `json:"blobs_added"`
	BytesAdded      int64     `json:"bytes_added"`  // plaintext bytes of new blobs
	BytesStored     int64     `json:"bytes_stored"` // bytes written after compression+encryption
	Errors          int64     `json:"errors"`
	ErrorSamples    []string  `json:"error_samples,omitempty"`
}

// Snapshot describes one backup.
type Snapshot struct {
	Time     time.Time `json:"time"`
	Tree     id.ID     `json:"tree"`
	Parent   *id.ID    `json:"parent,omitempty"`
	Paths    []string  `json:"paths"`
	Hostname string    `json:"hostname"`
	Username string    `json:"username,omitempty"`
	Tags     []string  `json:"tags,omitempty"`
	Job      string    `json:"job,omitempty"`
	Type     string    `json:"type"`
	Original *id.ID    `json:"original,omitempty"`
	Version  string    `json:"program_version,omitempty"`
	Summary  *Summary  `json:"summary,omitempty"`
	// Docker is set on snapshots of a Docker project.
	Docker *DockerInfo `json:"docker,omitempty"`
	// Databases lists the database dumps stored under DatabasesDir.
	Databases []DatabaseDump `json:"databases,omitempty"`

	id     id.ID
	bundle string // backup file holding the snapshot; "" for snapshots/
}

// DockerInfo describes the Docker project a snapshot belongs to and where
// its data lives on the host.
type DockerInfo struct {
	Project     string            `json:"project"`
	Standalone  bool              `json:"standalone,omitempty"`
	WorkingDir  string            `json:"working_dir,omitempty"`
	Home        string            `json:"home,omitempty"` // project directory incl. neighbouring bind mounts
	ConfigFiles []string          `json:"config_files,omitempty"`
	Volumes     map[string]string `json:"volumes,omitempty"` // volume name -> host directory
	Binds       []string          `json:"binds,omitempty"`   // bind mounts outside the project directory
	// MetaDir holds the database dumps and docker inspect output.
	MetaDir string `json:"meta_dir,omitempty"`
	// Stopped means the project was stopped during the backup, so its
	// files (including database files) are consistent.
	Stopped    bool              `json:"stopped,omitempty"`
	Dumps      []DockerDump      `json:"dumps,omitempty"`
	Containers []DockerContainer `json:"containers,omitempty"`
}

// DatabasesDir is the top-level snapshot directory of database dumps.
const DatabasesDir = "herakles-databases"

// DatabaseDump is a dump of one database made by a job's databases section.
type DatabaseDump struct {
	Source   string `json:"source"`             // name of the databases entry
	Kind     string `json:"kind"`               // postgres, mysql, mongodb, sqlite
	Database string `json:"database,omitempty"` // "" for a whole server (MongoDB, Postgres roles)
	Path     string `json:"path,omitempty"`     // in the snapshot, e.g. /herakles-databases/postgres/app.sql
	Size     int64  `json:"size,omitempty"`
	Error    string `json:"error,omitempty"`
}

// DockerDump is a database dump stored in DockerInfo.MetaDir.
type DockerDump struct {
	Container string `json:"container"`
	Service   string `json:"service,omitempty"`
	Kind      string `json:"kind"`               // postgres, mysql, mongo
	File      string `json:"file,omitempty"`     // file name in MetaDir
	DataDir   string `json:"data_dir,omitempty"` // host directory of the database files
	Error     string `json:"error,omitempty"`
}

// DockerContainer is a container of a project at backup time.
type DockerContainer struct {
	Name    string `json:"name"`
	Service string `json:"service,omitempty"`
	Image   string `json:"image"`
	Running bool   `json:"running"`
}

// ID returns the snapshot ID (its file name).
func (s *Snapshot) ID() id.ID { return s.id }

// File returns the name of the backup file holding the snapshot, or "" for
// a snapshot of the older repository format.
func (s *Snapshot) File() string { return s.bundle }

// SetID is used by tests and copy operations.
func (s *Snapshot) SetID(i id.ID) { s.id = i }

// HasTag reports whether the snapshot carries tag t.
func (s *Snapshot) HasTag(t string) bool {
	for _, x := range s.Tags {
		if x == t {
			return true
		}
	}
	return false
}

// SaveSnapshot completes the backup file of the running backup with s (a
// backup without new data gets a file of its own too) and returns the
// snapshot ID.
func (r *Repository) SaveSnapshot(ctx context.Context, s *Snapshot) (id.ID, error) {
	raw, err := json.Marshal(s)
	if err != nil {
		return id.Null, err
	}
	if err := r.ensureWriter(ctx); err != nil {
		return id.Null, err
	}
	ct := r.key.Seal(compress.Compress(r.opts.Compression, raw))
	b, err := r.commitBundle(ctx, ct)
	if err != nil {
		return id.Null, err
	}
	s.id, s.bundle = b.Snapshot.id, b.Name
	return s.id, nil
}

// LoadSnapshot loads a snapshot by full ID.
func (r *Repository) LoadSnapshot(ctx context.Context, i id.ID) (*Snapshot, error) {
	if s := r.bundleSnapshot(i); s != nil {
		return s, nil
	}
	s := &Snapshot{}
	err := r.LoadUnpacked(ctx, backend.SnapFile, i.String(), s)
	if backend.IsNotFound(err) {
		if _, berr := r.Bundles(ctx); berr == nil {
			if s := r.bundleSnapshot(i); s != nil {
				return s, nil
			}
		}
	}
	if err != nil {
		return nil, err
	}
	s.id = i
	return s, nil
}

func (r *Repository) bundleSnapshot(i id.ID) *Snapshot {
	r.bmu.Lock()
	defer r.bmu.Unlock()
	for _, b := range r.bundles {
		if b.Snapshot != nil && b.Snapshot.id == i {
			c := *b.Snapshot
			return &c
		}
	}
	return nil
}

// RemoveSnapshot forgets a snapshot. A snapshot in a backup file gets a
// marker; prune deletes the file once no other backup needs it.
func (r *Repository) RemoveSnapshot(ctx context.Context, i id.ID) error {
	if r.bundleSnapshot(i) == nil {
		err := r.be.Remove(ctx, backend.Handle{Type: backend.SnapFile, Name: i.String()})
		if !backend.IsNotFound(err) {
			return err
		}
		if _, berr := r.Bundles(ctx); berr != nil || r.bundleSnapshot(i) == nil {
			return err
		}
	}
	err := r.be.Save(ctx, backend.Handle{Type: backend.ForgetFile, Name: i.String()}, []byte("forgotten\n"))
	if err != nil && strings.Contains(err.Error(), "already exists") {
		return nil
	}
	return err
}

// Snapshots loads all snapshots sorted by time (oldest first): those of the
// older format in snapshots/ and those in backup files that were not
// forgotten.
func (r *Repository) Snapshots(ctx context.Context) ([]*Snapshot, error) {
	names, err := r.ListNames(ctx, backend.SnapFile)
	if err != nil {
		return nil, err
	}
	type res struct {
		s   *Snapshot
		err error
	}
	ch := make(chan res, len(names))
	sem := make(chan struct{}, 8)
	for _, n := range names {
		go func(n string) {
			sem <- struct{}{}
			defer func() { <-sem }()
			i, err := id.Parse(n)
			if err != nil {
				ch <- res{nil, nil}
				return
			}
			s := &Snapshot{}
			if err := r.LoadUnpacked(ctx, backend.SnapFile, n, s); err != nil {
				ch <- res{nil, err}
				return
			}
			s.id = i
			ch <- res{s, nil}
		}(n)
	}
	var out []*Snapshot
	for range names {
		x := <-ch
		if x.err != nil {
			return nil, x.err
		}
		if x.s != nil {
			out = append(out, x.s)
		}
	}
	bundles, err := r.Bundles(ctx)
	if err != nil {
		return nil, err
	}
	forgotten, err := r.forgottenSet(ctx)
	if err != nil {
		return nil, err
	}
	for _, b := range bundles {
		if b.Snapshot != nil && !forgotten.Has(b.Snapshot.id) {
			c := *b.Snapshot
			out = append(out, &c)
		}
	}
	SortSnapshots(out)
	return out, nil
}

// SortSnapshots sorts by time, oldest first.
func SortSnapshots(s []*Snapshot) {
	sort.SliceStable(s, func(i, j int) bool {
		if s[i].Time.Equal(s[j].Time) {
			return s[i].id.String() < s[j].id.String()
		}
		return s[i].Time.Before(s[j].Time)
	})
}

// SnapshotFilter selects snapshots.
type SnapshotFilter struct {
	Job      string
	Hostname string
	Tags     []string
	Paths    []string
}

// Match reports whether s satisfies the filter.
func (f SnapshotFilter) Match(s *Snapshot) bool {
	if f.Job != "" && s.Job != f.Job {
		return false
	}
	if f.Hostname != "" && s.Hostname != f.Hostname {
		return false
	}
	for _, t := range f.Tags {
		if !s.HasTag(t) {
			return false
		}
	}
	if len(f.Paths) > 0 {
		want := map[string]bool{}
		for _, p := range s.Paths {
			want[p] = true
		}
		for _, p := range f.Paths {
			if !want[p] {
				return false
			}
		}
	}
	return true
}

// FilterSnapshots returns snapshots matching f.
func FilterSnapshots(all []*Snapshot, f SnapshotFilter) []*Snapshot {
	var out []*Snapshot
	for _, s := range all {
		if f.Match(s) {
			out = append(out, s)
		}
	}
	return out
}

// FindSnapshot resolves "latest" or an ID prefix (at least 4 hex chars).
func (r *Repository) FindSnapshot(ctx context.Context, spec string, f SnapshotFilter) (*Snapshot, error) {
	all, err := r.Snapshots(ctx)
	if err != nil {
		return nil, err
	}
	return ResolveSnapshot(all, spec, f)
}

// ResolveSnapshot finds a snapshot in a list.
func ResolveSnapshot(all []*Snapshot, spec string, f SnapshotFilter) (*Snapshot, error) {
	if spec == "" || spec == "latest" {
		matches := FilterSnapshots(all, f)
		if len(matches) == 0 {
			return nil, fmt.Errorf("no matching snapshot found")
		}
		return matches[len(matches)-1], nil
	}
	spec = strings.ToLower(spec)
	if len(spec) < 4 {
		return nil, fmt.Errorf("snapshot ID prefix %q is too short", spec)
	}
	var found *Snapshot
	for _, s := range all {
		if strings.HasPrefix(s.id.String(), spec) {
			if found != nil {
				return nil, fmt.Errorf("snapshot ID prefix %q is ambiguous", spec)
			}
			found = s
		}
	}
	if found == nil {
		return nil, fmt.Errorf("snapshot %q not found", spec)
	}
	return found, nil
}
