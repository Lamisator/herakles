package daemon

import (
	"context"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/lamisator/herakles/internal/config"
	"github.com/lamisator/herakles/internal/repository"
)

// Entry is one scheduled item.
type Entry struct {
	Kind   string    `json:"kind"`
	Target string    `json:"target"`
	Type   string    `json:"type,omitempty"`
	Cron   string    `json:"cron"`
	Next   time.Time `json:"next"`
	sched  cron.Schedule
}

// Scheduler triggers tasks according to the configuration.
type Scheduler struct {
	rn  *Runner
	now func() time.Time

	mu      sync.Mutex
	entries []*Entry
	wake    chan struct{}
	retries map[string]int
}

// NewScheduler creates a scheduler for the runner's configuration.
func NewScheduler(rn *Runner) *Scheduler {
	s := &Scheduler{rn: rn, now: time.Now, wake: make(chan struct{}, 1), retries: map[string]int{}}
	rn.OnFinish = s.onFinish
	s.Reload()
	return s
}

func repoFilter(j *config.Job) repository.SnapshotFilter {
	host := j.Hostname
	if host == "" {
		host, _ = os.Hostname()
	}
	return repository.SnapshotFilter{Job: j.Name, Hostname: host}
}

// Reload rebuilds the schedule from the current configuration.
func (s *Scheduler) Reload() {
	cfg := s.rn.Engine().Config()
	now := s.now()
	var entries []*Entry
	for _, j := range cfg.Jobs {
		if j.Disabled {
			continue
		}
		for _, sc := range j.Schedules {
			cs, err := config.ParseCron(sc.Cron)
			if err != nil {
				continue
			}
			entries = append(entries, &Entry{Kind: KindBackup, Target: j.Name, Type: sc.Type, Cron: sc.Cron, sched: cs, Next: cs.Next(now)})
		}
	}
	for _, r := range cfg.Repositories {
		for kind, expr := range map[string]string{KindPrune: r.Maintenance.PruneSchedule, KindCheck: r.Maintenance.CheckSchedule} {
			if expr == "" {
				continue
			}
			cs, err := config.ParseCron(expr)
			if err != nil {
				continue
			}
			entries = append(entries, &Entry{Kind: kind, Target: r.Name, Cron: expr, sched: cs, Next: cs.Next(now)})
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Next.Before(entries[j].Next) })
	s.mu.Lock()
	s.entries = entries
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Entries returns the schedule sorted by next run.
func (s *Scheduler) Entries() []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Entry, len(s.entries))
	for i, e := range s.entries {
		out[i] = *e
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Next.Before(out[j].Next) })
	return out
}

// CatchUp runs jobs whose last successful run is older than their most
// recent scheduled time (missed while the daemon was not running).
func (s *Scheduler) CatchUp() []string {
	cfg := s.rn.Engine().Config()
	hist := s.rn.Engine().History()
	now := s.now()
	var started []string
	for _, j := range cfg.Jobs {
		if j.Disabled || !j.CatchUp || len(j.Schedules) == 0 {
			continue
		}
		last := hist.Last(j.Name)
		if last == nil {
			continue
		}
		missed := false
		typ := ""
		for _, sc := range j.Schedules {
			cs, err := config.ParseCron(sc.Cron)
			if err != nil {
				continue
			}
			if next := cs.Next(last.Start); next.Before(now) {
				missed = true
				// Prefer the "strongest" missed type.
				if sc.Type == "full" || typ == "" {
					typ = sc.Type
				}
			}
		}
		if missed {
			if _, err := s.rn.Enqueue(KindBackup, j.Name, typ, "catch-up"); err == nil {
				started = append(started, j.Name)
			}
		}
	}
	return started
}

func (s *Scheduler) onFinish(r *Run, err error) {
	if r.Kind != KindBackup {
		return
	}
	j := s.rn.Engine().Config().Job(r.Target)
	if j == nil {
		return
	}
	if err == nil || r.Trigger == "manual" || r.attempt >= j.Retry.Attempts {
		return
	}
	delay := 5 * time.Minute
	if j.Retry.Delay != "" {
		if d, perr := time.ParseDuration(j.Retry.Delay); perr == nil {
			delay = d
		}
	}
	s.rn.Engine().Log.Info("scheduling retry", "job", j.Name, "attempt", r.attempt+1, "delay", delay)
	go func() {
		t := time.NewTimer(delay)
		defer t.Stop()
		select {
		case <-t.C:
			_, _ = s.rn.enqueue(KindBackup, r.Target, r.Type, "retry", r.attempt+1, nil)
		case <-s.rn.ctx.Done():
		}
	}()
}

// Run loops until ctx is cancelled, firing due entries.
func (s *Scheduler) Run(ctx context.Context) {
	for {
		s.mu.Lock()
		var next time.Time
		for _, e := range s.entries {
			if next.IsZero() || e.Next.Before(next) {
				next = e.Next
			}
		}
		s.mu.Unlock()
		wait := time.Hour
		if !next.IsZero() {
			wait = time.Until(next)
			if wait < 0 {
				wait = 0
			}
			if wait > time.Hour {
				wait = time.Hour // re-evaluate regularly (clock changes, suspend)
			}
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-s.wake:
			t.Stop()
			continue
		case <-t.C:
		}
		s.fireDue()
	}
}

func (s *Scheduler) fireDue() {
	now := s.now()
	s.mu.Lock()
	var due []*Entry
	for _, e := range s.entries {
		if !e.Next.After(now) {
			due = append(due, e)
			e.Next = e.sched.Next(now)
		}
	}
	s.mu.Unlock()
	// If several schedules of the same job fire together, run the strongest.
	byTarget := map[string]*Entry{}
	rank := map[string]int{"full": 3, "differential": 2, "auto": 1, "": 1, "incremental": 0}
	var order []string
	for _, e := range due {
		k := e.Kind + ":" + e.Target
		if prev, ok := byTarget[k]; !ok {
			byTarget[k] = e
			order = append(order, k)
		} else if rank[e.Type] > rank[prev.Type] {
			byTarget[k] = e
		}
	}
	for _, k := range order {
		e := byTarget[k]
		if _, err := s.rn.Enqueue(e.Kind, e.Target, e.Type, "schedule"); err != nil {
			s.rn.Engine().Log.Warn("scheduled task skipped", "task", e.Kind, "target", e.Target, "err", err)
		}
	}
}
