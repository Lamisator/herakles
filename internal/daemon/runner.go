// Package daemon runs scheduled backups and maintenance in the background
// and exposes a local control API.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lamisator/herakles/internal/archiver"
	"github.com/lamisator/herakles/internal/config"
	"github.com/lamisator/herakles/internal/engine"
)

// Task kinds.
const (
	KindBackup = "backup"
	KindPrune  = "prune"
	KindCheck  = "check"
	KindForget = "forget"
)

// Run is a queued or running task.
type Run struct {
	ID       string             `json:"id"`
	Kind     string             `json:"kind"`
	Target   string             `json:"target"` // job or repository name
	Type     string             `json:"type,omitempty"`
	Trigger  string             `json:"trigger"`
	Queued   time.Time          `json:"queued"`
	Started  time.Time          `json:"started,omitempty"`
	State    string             `json:"state"` // queued, running, done
	Progress *archiver.Progress `json:"-"`
	Message  string             `json:"message,omitempty"`
	Result   *engine.JobResult  `json:"result,omitempty"`
	Error    string             `json:"error,omitempty"`
	attempt  int
	cancel   context.CancelFunc
	done     chan struct{}
}

// Key identifies tasks that must not run twice concurrently.
func (r *Run) Key() string { return r.Kind + ":" + r.Target }

// Done returns a channel closed when the run finishes.
func (r *Run) Done() <-chan struct{} { return r.done }

// RunView is a JSON friendly snapshot of a run.
type RunView struct {
	*Run
	ProgressStats *archiver.ProgressStats `json:"progress,omitempty"`
}

// ErrAlreadyQueued is returned when the same task is already pending.
var ErrAlreadyQueued = errors.New("task is already queued or running")

// Runner executes tasks with a concurrency limit.
type Runner struct {
	eng *engine.Engine
	sem chan struct{}

	mu     sync.Mutex
	active map[string]*Run
	recent []*Run
	seq    atomic.Int64
	wg     sync.WaitGroup
	ctx    context.Context

	// OnFinish is called after each task (used for retries).
	OnFinish func(r *Run, err error)
}

// NewRunner creates a runner.
func NewRunner(ctx context.Context, eng *engine.Engine, maxConcurrent int) *Runner {
	if maxConcurrent <= 0 {
		maxConcurrent = 1
	}
	return &Runner{eng: eng, sem: make(chan struct{}, maxConcurrent), active: map[string]*Run{}, ctx: ctx}
}

// Engine returns the engine.
func (rn *Runner) Engine() *engine.Engine { return rn.eng }

// Enqueue schedules a task.
func (rn *Runner) Enqueue(kind, target, typ, trigger string) (*Run, error) {
	return rn.enqueue(kind, target, typ, trigger, 0)
}

func (rn *Runner) enqueue(kind, target, typ, trigger string, attempt int) (*Run, error) {
	cfg := rn.eng.Config()
	switch kind {
	case KindBackup:
		if cfg.Job(target) == nil {
			return nil, fmt.Errorf("unknown job %q", target)
		}
		if !config.ValidBackupType(typ) {
			return nil, fmt.Errorf("invalid backup type %q", typ)
		}
	case KindPrune, KindCheck:
		if cfg.Repo(target) == nil {
			return nil, fmt.Errorf("unknown repository %q", target)
		}
	case KindForget:
		if cfg.Job(target) == nil {
			return nil, fmt.Errorf("unknown job %q", target)
		}
	default:
		return nil, fmt.Errorf("unknown task kind %q", kind)
	}
	r := &Run{ID: strconv.FormatInt(rn.seq.Add(1), 10), Kind: kind, Target: target, Type: typ, Trigger: trigger,
		Queued: time.Now(), State: "queued", done: make(chan struct{}), attempt: attempt}
	if kind == KindBackup {
		r.Progress = &archiver.Progress{}
	}
	rn.mu.Lock()
	if _, busy := rn.active[r.Key()]; busy {
		rn.mu.Unlock()
		return nil, ErrAlreadyQueued
	}
	ctx, cancel := context.WithCancel(rn.ctx)
	r.cancel = cancel
	rn.active[r.Key()] = r
	rn.mu.Unlock()
	rn.wg.Add(1)
	go rn.execute(ctx, r)
	return r, nil
}

func (rn *Runner) execute(ctx context.Context, r *Run) {
	defer rn.wg.Done()
	defer close(r.done)
	select {
	case rn.sem <- struct{}{}:
	case <-ctx.Done():
		rn.finish(r, ctx.Err())
		return
	}
	defer func() { <-rn.sem }()
	rn.mu.Lock()
	r.State, r.Started = "running", time.Now()
	rn.mu.Unlock()
	err := rn.do(ctx, r)
	rn.finish(r, err)
}

func (rn *Runner) setMessage(r *Run, msg string) {
	rn.mu.Lock()
	r.Message = msg
	rn.mu.Unlock()
}

func (rn *Runner) do(ctx context.Context, r *Run) error {
	e := rn.eng
	log := e.Log.With("task", r.Kind, "target", r.Target, "trigger", r.Trigger)
	switch r.Kind {
	case KindBackup:
		res, err := e.RunJob(ctx, r.Target, engine.RunOptions{Type: r.Type, Trigger: r.Trigger, Progress: r.Progress})
		rn.mu.Lock()
		r.Result = res
		rn.mu.Unlock()
		return err
	case KindForget:
		job := e.Config().Job(r.Target)
		repo, _, err := e.OpenByName(ctx, job.Repository)
		if err != nil {
			return err
		}
		defer repo.Close()
		_, err = e.ApplyRetention(ctx, repo, job.Retention, repoFilter(job), false)
		return err
	case KindPrune:
		repo, rc, err := e.OpenByName(ctx, r.Target)
		if err != nil {
			return err
		}
		defer repo.Close()
		// Apply retention of all jobs of this repository first.
		for _, j := range e.Config().Jobs {
			if j.Repository == r.Target && !j.Retention.Empty() {
				if _, err := e.ApplyRetention(ctx, repo, j.Retention, repoFilter(j), false); err != nil {
					return err
				}
			}
		}
		st, err := e.Prune(ctx, repo, rc.Maintenance.RepackThreshold, false, func(m string) { rn.setMessage(r, m) })
		if err == nil {
			log.Info("prune finished", "packs_deleted", st.PacksDeleted, "packs_repacked", st.PacksRepacked, "bytes_freed", st.BytesFreed)
		}
		return err
	case KindCheck:
		repo, rc, err := e.OpenByName(ctx, r.Target)
		if err != nil {
			return err
		}
		defer repo.Close()
		frac, _ := config.ParsePercent(rc.Maintenance.CheckReadData)
		res, err := e.Check(ctx, repo, frac, func(m string) { rn.setMessage(r, m) })
		if err != nil {
			return err
		}
		if !res.OK() {
			return fmt.Errorf("repository check found %d errors (first: %s)", len(res.Errors), res.Errors[0])
		}
		log.Info("check finished", "snapshots", res.Snapshots, "packs_read", res.PacksRead)
		return nil
	}
	return nil
}

func (rn *Runner) finish(r *Run, err error) {
	rn.mu.Lock()
	r.State = "done"
	if err != nil {
		r.Error = err.Error()
	}
	delete(rn.active, r.Key())
	rn.recent = append([]*Run{r}, rn.recent...)
	if len(rn.recent) > 50 {
		rn.recent = rn.recent[:50]
	}
	rn.mu.Unlock()
	if err != nil && r.Kind != KindBackup {
		rn.eng.Log.Error("task failed", "task", r.Kind, "target", r.Target, "err", err)
	}
	if rn.OnFinish != nil {
		rn.OnFinish(r, err)
	}
}

// Cancel stops a queued or running task by run ID or key.
func (rn *Runner) Cancel(idOrKey string) bool {
	rn.mu.Lock()
	defer rn.mu.Unlock()
	for k, r := range rn.active {
		if r.ID == idOrKey || k == idOrKey || r.Target == idOrKey {
			r.cancel()
			return true
		}
	}
	return false
}

// Active returns queued and running tasks.
func (rn *Runner) Active() []RunView {
	rn.mu.Lock()
	defer rn.mu.Unlock()
	var out []RunView
	for _, r := range rn.active {
		cp := *r
		v := RunView{Run: &cp}
		if r.Progress != nil && r.State == "running" {
			st := r.Progress.Stats()
			v.ProgressStats = &st
		}
		out = append(out, v)
	}
	sortRuns(out)
	return out
}

// Recent returns finished tasks, newest first.
func (rn *Runner) Recent() []RunView {
	rn.mu.Lock()
	defer rn.mu.Unlock()
	out := make([]RunView, 0, len(rn.recent))
	for _, r := range rn.recent {
		cp := *r
		out = append(out, RunView{Run: &cp})
	}
	return out
}

// Get returns a run by ID.
func (rn *Runner) Get(id string) (RunView, bool) {
	for _, v := range rn.Active() {
		if v.ID == id {
			return v, true
		}
	}
	for _, v := range rn.Recent() {
		if v.ID == id {
			return v, true
		}
	}
	return RunView{}, false
}

// IsActive reports whether a task with the key is queued or running.
func (rn *Runner) IsActive(kind, target string) bool {
	rn.mu.Lock()
	defer rn.mu.Unlock()
	_, ok := rn.active[kind+":"+target]
	return ok
}

// Wait blocks until all tasks finished.
func (rn *Runner) Wait() { rn.wg.Wait() }

func sortRuns(v []RunView) {
	for i := 1; i < len(v); i++ {
		for j := i; j > 0 && v[j].Queued.Before(v[j-1].Queued); j-- {
			v[j], v[j-1] = v[j-1], v[j]
		}
	}
}
