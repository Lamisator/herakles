package repository

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/user"
	"sync"
	"time"

	"github.com/lamisator/herakles/internal/backend"
	"github.com/lamisator/herakles/internal/id"
)

// Lock timing.
var (
	LockRefreshInterval = 5 * time.Minute
	LockStaleAfter      = 30 * time.Minute
)

// LockInfo is the content of a lock file.
type LockInfo struct {
	Time      time.Time `json:"time"`
	Exclusive bool      `json:"exclusive"`
	Hostname  string    `json:"hostname"`
	Username  string    `json:"username"`
	PID       int       `json:"pid"`
	Operation string    `json:"operation"`
	Name      string    `json:"-"`
}

// Stale reports whether the lock should be ignored.
func (l LockInfo) Stale() bool {
	if time.Since(l.Time) > LockStaleAfter {
		return true
	}
	host, _ := os.Hostname()
	if l.Hostname == host && l.PID > 0 && !processExists(l.PID) {
		return true
	}
	return false
}

func (l LockInfo) String() string {
	kind := "shared"
	if l.Exclusive {
		kind = "exclusive"
	}
	return fmt.Sprintf("%s lock by %s@%s (pid %d, %s) since %s", kind, l.Username, l.Hostname, l.PID, l.Operation, l.Time.Local().Format(time.RFC3339))
}

// Lock is a held repository lock.
type Lock struct {
	r      *Repository
	info   LockInfo
	name   string
	stop   chan struct{}
	wg     sync.WaitGroup
	mu     sync.Mutex
	failed error
}

// ErrLocked is returned when a conflicting lock exists.
var ErrLocked = errors.New("repository is locked")

// ListLocks returns all lock files.
func (r *Repository) ListLocks(ctx context.Context) ([]LockInfo, error) {
	names, err := r.ListNames(ctx, backend.LockFile)
	if err != nil {
		return nil, err
	}
	var out []LockInfo
	for _, n := range names {
		var li LockInfo
		if err := r.LoadUnpacked(ctx, backend.LockFile, n, &li); err != nil {
			if backend.IsNotFound(err) {
				continue
			}
			// Unreadable lock (e.g. written with a different key): treat as stale-able.
			li = LockInfo{Name: n, Operation: "unreadable", Time: time.Time{}}
		}
		li.Name = n
		out = append(out, li)
	}
	return out, nil
}

func conflicting(locks []LockInfo, exclusive bool, self string) *LockInfo {
	for i := range locks {
		l := locks[i]
		if l.Name == self || l.Stale() {
			continue
		}
		if exclusive || l.Exclusive {
			return &l
		}
	}
	return nil
}

// Lock acquires a shared or exclusive lock for operation.
func (r *Repository) Lock(ctx context.Context, exclusive bool, operation string) (*Lock, error) {
	locks, err := r.ListLocks(ctx)
	if err != nil {
		return nil, err
	}
	if c := conflicting(locks, exclusive, ""); c != nil {
		return nil, fmt.Errorf("%w: %s", ErrLocked, c)
	}
	host, _ := os.Hostname()
	uname := ""
	if u, err := user.Current(); err == nil {
		uname = u.Username
	}
	l := &Lock{r: r, info: LockInfo{Time: time.Now(), Exclusive: exclusive, Hostname: host, Username: uname, PID: os.Getpid(), Operation: operation}, name: id.Random().String(), stop: make(chan struct{})}
	if err := r.saveUnpackedAs(ctx, backend.LockFile, l.name, l.info); err != nil {
		return nil, fmt.Errorf("create lock: %w", err)
	}
	// Re-check to resolve races between concurrent lockers.
	locks, err = r.ListLocks(ctx)
	if err == nil {
		if c := conflicting(locks, exclusive, l.name); c != nil {
			// Deterministic tie-break: the lock with the smaller name wins
			// when both are of the same age class; otherwise back off.
			_ = r.be.Remove(ctx, backend.Handle{Type: backend.LockFile, Name: l.name})
			return nil, fmt.Errorf("%w: %s", ErrLocked, c)
		}
	}
	l.wg.Add(1)
	go l.refresher()
	return l, nil
}

func (l *Lock) refresher() {
	defer l.wg.Done()
	t := time.NewTicker(LockRefreshInterval)
	defer t.Stop()
	for {
		select {
		case <-l.stop:
			return
		case <-t.C:
			l.info.Time = time.Now()
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			err := l.r.saveUnpackedAs(ctx, backend.LockFile, l.name, l.info)
			cancel()
			if err != nil {
				l.mu.Lock()
				l.failed = err
				l.mu.Unlock()
			}
		}
	}
}

// Unlock releases the lock.
func (l *Lock) Unlock() error {
	if l == nil {
		return nil
	}
	close(l.stop)
	l.wg.Wait()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	return l.r.be.Remove(ctx, backend.Handle{Type: backend.LockFile, Name: l.name})
}

// RemoveLocks deletes stale locks (or all locks if all is true).
func (r *Repository) RemoveLocks(ctx context.Context, all bool) (int, error) {
	locks, err := r.ListLocks(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, l := range locks {
		if all || l.Stale() {
			if err := r.be.Remove(ctx, backend.Handle{Type: backend.LockFile, Name: l.Name}); err != nil && !backend.IsNotFound(err) {
				return n, err
			}
			n++
		}
	}
	return n, nil
}
