// Package engine implements high level operations (running jobs, applying
// retention, maintenance, copying snapshots) shared by the CLI, daemon, TUI
// and GUI.
package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/lamisator/herakles/internal/archiver"
	"github.com/lamisator/herakles/internal/backend"
	"github.com/lamisator/herakles/internal/compress"
	"github.com/lamisator/herakles/internal/config"
	"github.com/lamisator/herakles/internal/crypto"
	"github.com/lamisator/herakles/internal/notify"
	"github.com/lamisator/herakles/internal/repository"
	"github.com/lamisator/herakles/internal/retention"
)

// Engine ties configuration and repositories together.
type Engine struct {
	mu  sync.RWMutex
	cfg *config.Config
	// Prompt is used when a repository has no configured credential source.
	Prompt func(repo string) (crypto.Credentials, error)
	Log    *slog.Logger

	credMu sync.Mutex
	creds  map[string]crypto.Credentials // remembered interactive credentials
}

// New creates an engine.
func New(cfg *config.Config, log *slog.Logger) *Engine {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Engine{cfg: cfg, Log: log, creds: map[string]crypto.Credentials{}}
}

// Config returns the current configuration.
func (e *Engine) Config() *config.Config {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.cfg
}

// SetConfig replaces the configuration (e.g. after editing in a UI).
func (e *Engine) SetConfig(c *config.Config) {
	e.mu.Lock()
	e.cfg = c
	e.mu.Unlock()
}

// RememberCredentials caches credentials for a repository for this session.
func (e *Engine) RememberCredentials(repo string, c crypto.Credentials) {
	e.credMu.Lock()
	e.creds[repo] = c
	e.credMu.Unlock()
}

// ForgetCredentials drops remembered credentials of a repository.
func (e *Engine) ForgetCredentials(repo string) {
	e.credMu.Lock()
	delete(e.creds, repo)
	e.credMu.Unlock()
}

// Credentials resolves credentials for a repository.
func (e *Engine) Credentials(rc *config.Repository) (crypto.Credentials, error) {
	e.credMu.Lock()
	c, ok := e.creds[rc.Name]
	e.credMu.Unlock()
	if ok && !c.Empty() {
		return c, nil
	}
	c, err := rc.Credentials()
	if errors.Is(err, config.ErrNoPassword) && e.Prompt != nil {
		c, err = e.Prompt(rc.Name)
		if err == nil {
			e.RememberCredentials(rc.Name, c)
		}
	}
	return c, err
}

// Locked lists the configured repositories used by jobs that have no
// credential source and no remembered credentials, i.e. that cannot be
// opened without asking for a password.
func (e *Engine) Locked() []string {
	cfg := e.Config()
	used := map[string]bool{}
	for _, j := range cfg.Jobs {
		used[j.Repository] = true
	}
	var out []string
	for _, rc := range cfg.Repositories {
		if !used[rc.Name] || rc.HasCredentialSource() {
			continue
		}
		e.credMu.Lock()
		c, ok := e.creds[rc.Name]
		e.credMu.Unlock()
		if !ok || c.Empty() {
			out = append(out, rc.Name)
		}
	}
	return out
}

// RepoConfig finds a repository by name or treats the argument as a location.
func (e *Engine) RepoConfig(nameOrLocation string) (*config.Repository, error) {
	if rc := e.Config().Repo(nameOrLocation); rc != nil {
		return rc, nil
	}
	if nameOrLocation == "" {
		repos := e.Config().Repositories
		if len(repos) == 1 {
			return repos[0], nil
		}
		return nil, errors.New("no repository given (use --repo); the configuration defines several or none")
	}
	return &config.Repository{Name: nameOrLocation, Location: nameOrLocation}, nil
}

// RepoOptions converts repository settings into client options.
func RepoOptions(rc *config.Repository) (repository.Options, error) {
	opts := repository.DefaultOptions()
	if rc.Compression != "" {
		m, err := compress.Parse(rc.Compression)
		if err != nil {
			return opts, err
		}
		opts.Compression = m
	}
	if rc.PackSizeMiB > 0 {
		opts.PackSize = rc.PackSizeMiB << 20
	}
	if rc.Connections > 0 {
		opts.Connections = rc.Connections
	}
	return opts, nil
}

// OpenBackend opens the storage backend of a repository.
func OpenBackend(rc *config.Repository) (backend.Backend, error) {
	return backend.Open(config.ExpandHome(rc.Location), rc.BackendOptions())
}

// Open opens and unlocks a repository.
func (e *Engine) Open(ctx context.Context, rc *config.Repository) (*repository.Repository, error) {
	creds, err := e.Credentials(rc)
	if err != nil {
		return nil, fmt.Errorf("repository %s: %w", rc.Name, err)
	}
	opts, err := RepoOptions(rc)
	if err != nil {
		return nil, err
	}
	be, err := OpenBackend(rc)
	if err != nil {
		return nil, err
	}
	r, err := repository.Open(ctx, be, creds, opts)
	if err != nil {
		be.Close()
		return nil, fmt.Errorf("repository %s: %w", rc.Name, err)
	}
	return r, nil
}

// OpenByName opens a configured repository (or a location).
func (e *Engine) OpenByName(ctx context.Context, name string) (*repository.Repository, *config.Repository, error) {
	rc, err := e.RepoConfig(name)
	if err != nil {
		return nil, nil, err
	}
	r, err := e.Open(ctx, rc)
	return r, rc, err
}

// Init creates a new repository.
func (e *Engine) Init(ctx context.Context, rc *config.Repository, creds crypto.Credentials) (*repository.Repository, error) {
	be, err := OpenBackend(rc)
	if err != nil {
		return nil, err
	}
	r, err := repository.Init(ctx, be, creds, rc.ChunkerParams())
	if err != nil {
		be.Close()
		return nil, err
	}
	return r, nil
}

// JobResult describes one job run.
type JobResult struct {
	Job        string                 `json:"job"`
	Repository string                 `json:"repository"`
	Type       string                 `json:"type"`
	Status     string                 `json:"status"` // success, warning, failure
	Error      string                 `json:"error,omitempty"`
	Start      time.Time              `json:"start"`
	End        time.Time              `json:"end"`
	Snapshot   string                 `json:"snapshot,omitempty"`
	Summary    *repository.Summary    `json:"summary,omitempty"`
	Forgotten  int                    `json:"forgotten,omitempty"`
	Pruned     *repository.PruneStats `json:"pruned,omitempty"`
	Trigger    string                 `json:"trigger,omitempty"`
	// Projects holds the per-project results of a Docker job.
	Projects []ProjectResult `json:"projects,omitempty"`
}

// RunOptions customise a job run.
type RunOptions struct {
	Type            string // auto (default), full, incremental, differential
	Trigger         string // manual, schedule, catch-up, retry
	Progress        *archiver.Progress
	DryRunRetention bool
	OnError         func(path string, err error)
	// Projects limits a Docker job to these projects.
	Projects []string
}

// ResolveType decides the effective backup type and parent snapshot.
func ResolveType(job *config.Job, requested string, snaps []*repository.Snapshot, host string, now time.Time) (string, *repository.Snapshot) {
	var mine []*repository.Snapshot
	for _, s := range snaps {
		if s.Job == job.Name && s.Hostname == host {
			mine = append(mine, s)
		}
	}
	repository.SortSnapshots(mine)
	var latest, latestFull *repository.Snapshot
	if len(mine) > 0 {
		latest = mine[len(mine)-1]
	}
	for i := len(mine) - 1; i >= 0; i-- {
		if mine[i].Type == repository.TypeFull {
			latestFull = mine[i]
			break
		}
	}
	typ := requested
	if typ == "" || typ == "auto" {
		typ = repository.TypeIncremental
		if job.FullInterval != "" {
			d, _ := retention.ParseDuration(job.FullInterval)
			if latestFull == nil || latestFull.Time.Before(d.Before(now)) {
				typ = repository.TypeFull
			}
		}
	}
	switch typ {
	case repository.TypeFull:
		return typ, nil
	case repository.TypeDifferential:
		if latestFull == nil {
			return repository.TypeFull, nil
		}
		return typ, latestFull
	default:
		if latest == nil {
			// Nothing to compare against: this is a full backup.
			return repository.TypeFull, nil
		}
		return repository.TypeIncremental, latest
	}
}

// RunJob runs a configured backup job, applying hooks, retention, prune,
// history and notifications.
//
// For a Docker job, name may be "job/project" to back up one project.
func (e *Engine) RunJob(ctx context.Context, name string, ro RunOptions) (*JobResult, error) {
	cfg := e.Config()
	name, project := config.SplitUnit(name)
	job := cfg.Job(name)
	if job == nil {
		return nil, fmt.Errorf("unknown job %q", name)
	}
	if project != "" {
		if job.Docker == nil {
			return nil, fmt.Errorf("job %s has no docker section; %q is not a valid target", name, name+"/"+project)
		}
		ro.Projects = append(ro.Projects, project)
	}
	rc := cfg.Repo(job.Repository)
	if rc == nil {
		return nil, fmt.Errorf("job %s: unknown repository %q", name, job.Repository)
	}
	res := &JobResult{Job: job.Name, Repository: rc.Name, Start: time.Now(), Trigger: ro.Trigger, Type: ro.Type}
	if res.Trigger == "" {
		res.Trigger = "manual"
	}
	log := e.Log.With("job", job.Name, "repository", rc.Name)
	log.Info("job started", "type", ro.Type, "trigger", res.Trigger)
	env := []string{"HERAKLES_JOB=" + job.Name, "HERAKLES_REPOSITORY=" + rc.Name}

	err := runHooks(ctx, job.Hooks, job.Hooks.Before, env, log)
	switch {
	case err != nil:
		err = fmt.Errorf("before hook: %w", err)
	case job.Docker != nil:
		err = e.runDockerBackup(ctx, job, rc, ro, res, log)
	default:
		err = e.runBackup(ctx, job, rc, ro, res, log)
	}
	res.End = time.Now()
	switch {
	case err != nil:
		res.Status, res.Error = "failure", err.Error()
	case res.Error != "" || (res.Summary != nil && res.Summary.Errors > 0):
		res.Status = "warning"
	default:
		res.Status = "success"
	}
	env = append(env, "HERAKLES_STATUS="+res.Status, "HERAKLES_SNAPSHOT="+res.Snapshot, "HERAKLES_TYPE="+res.Type, "HERAKLES_ERROR="+res.Error)
	if err == nil {
		if herr := runHooks(ctx, job.Hooks, job.Hooks.OnSuccess, env, log); herr != nil {
			log.Warn("on_success hook failed", "err", herr)
		}
	} else {
		if herr := runHooks(context.WithoutCancel(ctx), job.Hooks, job.Hooks.OnFailure, env, log); herr != nil {
			log.Warn("on_failure hook failed", "err", herr)
		}
	}
	if herr := runHooks(context.WithoutCancel(ctx), job.Hooks, job.Hooks.After, env, log); herr != nil {
		log.Warn("after hook failed", "err", herr)
	}
	if herr := e.History().Add(res); herr != nil {
		log.Warn("could not record history", "err", herr)
	}
	ev := notify.Event{Job: res.Job, Repository: res.Repository, Status: res.Status, Type: res.Type, Snapshot: res.Snapshot, Error: res.Error, Start: res.Start, End: res.End}
	ev.Hostname, _ = os.Hostname()
	if res.Summary != nil {
		ev.Files, ev.Bytes, ev.BytesAdded, ev.Errors = res.Summary.TotalFiles, res.Summary.TotalBytes, res.Summary.BytesAdded, res.Summary.Errors
	}
	if nerr := notify.Send(context.WithoutCancel(ctx), cfg.Notifications, ev); nerr != nil {
		log.Warn("notification failed", "err", nerr)
	}
	if err != nil {
		log.Error("job failed", "err", err)
		return res, err
	}
	log.Info("job finished", "status", res.Status, "snapshot", res.Snapshot, "duration", res.End.Sub(res.Start).Round(time.Second))
	return res, nil
}

func (e *Engine) runBackup(ctx context.Context, job *config.Job, rc *config.Repository, ro RunOptions, res *JobResult, log *slog.Logger) error {
	repo, err := e.Open(ctx, rc)
	if err != nil {
		return err
	}
	defer repo.Close()
	var paths []string
	for _, p := range job.Paths {
		paths = append(paths, config.ExpandHome(p))
	}
	u, err := e.backupUnit(ctx, repo, job, unit{Name: job.Name, Paths: paths, Devices: job.Devices}, ro, log)
	if err != nil {
		return err
	}
	res.Type, res.Snapshot, res.Summary, res.Forgotten = u.typ, u.snap.ID().String(), u.snap.Summary, u.forgotten
	if u.forgotten > 0 && job.Prune && !ro.DryRunRetention {
		st, err := e.Prune(ctx, repo, rc.Maintenance.RepackThreshold, false, nil)
		if err != nil {
			return fmt.Errorf("prune: %w", err)
		}
		res.Pruned = &st
	}
	return nil
}

// unit is one snapshot of a job: the job itself, or one Docker project.
type unit struct {
	Name     string // snapshot job name: "job" or "job/project"
	Paths    []string
	Devices  []string
	Excludes []string // in addition to the job's
	Docker   *repository.DockerInfo
}

type unitResult struct {
	snap      *repository.Snapshot
	typ       string
	forgotten int
}

// backupUnit creates one snapshot and applies the job's retention to the
// snapshots of the same unit.
func (e *Engine) backupUnit(ctx context.Context, repo *repository.Repository, job *config.Job, u unit, ro RunOptions, log *slog.Logger) (*unitResult, error) {
	lock, err := repo.Lock(ctx, false, "backup "+u.Name)
	if err != nil {
		return nil, err
	}
	snaps, err := repo.Snapshots(ctx)
	if err != nil {
		lock.Unlock()
		return nil, err
	}
	host := job.Hostname
	if host == "" {
		host, _ = os.Hostname()
	}
	uj := *job
	uj.Name = u.Name
	typ, parent := ResolveType(&uj, ro.Type, snaps, host, time.Now())
	maxSize, _ := config.ParseSize(job.ExcludeLargerThan)
	a, err := archiver.New(repo, archiver.Options{
		Paths: u.Paths, Devices: u.Devices, Excludes: append(append([]string(nil), job.Exclude...), u.Excludes...), ExcludeIfPresent: job.ExcludeIfPresent,
		ExcludeCaches: job.ExcludeCaches, ExcludeLargerThan: maxSize, OneFileSystem: job.OneFileSystem,
		Type: typ, Parent: parent, Job: u.Name, Hostname: host, Tags: job.Tags, Docker: u.Docker,
		FileWorkers: job.FileWorkers, Progress: ro.Progress,
		OnError: func(p string, err error) {
			log.Warn("file error", "path", p, "err", err)
			if ro.OnError != nil {
				ro.OnError(p, err)
			}
		},
	})
	if err != nil {
		lock.Unlock()
		return nil, err
	}
	snap, err := a.Run(ctx)
	lock.Unlock()
	if err != nil {
		return nil, err
	}
	r := &unitResult{snap: snap, typ: typ}
	if !job.Retention.Empty() {
		n, err := e.ApplyRetention(ctx, repo, job.Retention, repository.SnapshotFilter{Job: u.Name, Hostname: host}, ro.DryRunRetention)
		if err != nil {
			return r, fmt.Errorf("retention: %w", err)
		}
		r.forgotten = n
	}
	return r, nil
}

// ApplyRetention forgets snapshots matching filter according to policy and
// returns how many were removed.
func (e *Engine) ApplyRetention(ctx context.Context, repo *repository.Repository, p retention.Policy, f repository.SnapshotFilter, dryRun bool) (int, error) {
	if p.Empty() {
		return 0, nil
	}
	lock, err := repo.Lock(ctx, false, "forget")
	if err != nil {
		return 0, err
	}
	defer lock.Unlock()
	all, err := repo.Snapshots(ctx)
	if err != nil {
		return 0, err
	}
	groups := retention.Apply(repository.FilterSnapshots(all, f), p)
	remove := retention.Forget(groups)
	if dryRun {
		return len(remove), nil
	}
	for _, s := range remove {
		if err := repo.RemoveSnapshot(ctx, s.ID()); err != nil && !backend.IsNotFound(err) {
			return 0, err
		}
		e.Log.Info("forgot snapshot", "snapshot", s.ID().Short(), "time", s.Time)
	}
	return len(remove), nil
}

// Prune runs garbage collection under an exclusive lock.
func (e *Engine) Prune(ctx context.Context, repo *repository.Repository, threshold float64, dryRun bool, progress func(string)) (repository.PruneStats, error) {
	lock, err := repo.Lock(ctx, true, "prune")
	if err != nil {
		return repository.PruneStats{}, err
	}
	defer lock.Unlock()
	return repo.Prune(ctx, repository.PruneOptions{RepackThreshold: threshold, DryRun: dryRun, Progress: progress})
}

// Check verifies a repository under a shared lock.
func (e *Engine) Check(ctx context.Context, repo *repository.Repository, readData float64, progress func(string)) (*repository.CheckResult, error) {
	lock, err := repo.Lock(ctx, false, "check")
	if err != nil {
		return nil, err
	}
	defer lock.Unlock()
	return repo.Check(ctx, repository.CheckOptions{ReadData: readData, Progress: progress})
}
