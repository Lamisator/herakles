package daemon

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lamisator/herakles/internal/config"
	"github.com/lamisator/herakles/internal/engine"
	tu "github.com/lamisator/herakles/internal/testutil"
)

func setup(t *testing.T) (*engine.Engine, context.Context) {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	tu.MakeTree(t, src)
	cfg := config.New(filepath.Join(dir, "h.yaml"))
	cfg.StateDir = filepath.Join(dir, "state")
	cfg.Daemon.ControlListen = "127.0.0.1:0"
	cfg.Repositories = []*config.Repository{{Name: "r", Location: filepath.Join(dir, "repo"), Password: "pw",
		Maintenance: config.Maintenance{PruneSchedule: "@daily", CheckSchedule: "@weekly", CheckReadData: "100%"}}}
	cfg.Jobs = []*config.Job{
		{Name: "a", Repository: "r", Paths: []string{src}, CatchUp: true, Schedules: []config.Schedule{{Cron: "0 2 * * *", Type: "incremental"}, {Cron: "0 2 * * 0", Type: "full"}}},
		{Name: "broken", Repository: "r", Paths: []string{filepath.Join(dir, "missing")}, Retry: config.Retry{Attempts: 2, Delay: "10ms"}, Schedules: []config.Schedule{{Cron: "@hourly"}}},
		{Name: "off", Repository: "r", Paths: []string{src}, Disabled: true, Schedules: []config.Schedule{{Cron: "@hourly"}}},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	e := engine.New(cfg, nil)
	rc := cfg.Repositories[0]
	creds, _ := rc.Credentials()
	if _, err := e.Init(context.Background(), rc, creds); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return e, ctx
}

func wait(t *testing.T, r *Run) {
	t.Helper()
	select {
	case <-r.Done():
	case <-time.After(30 * time.Second):
		t.Fatal("task did not finish")
	}
}

func TestRunnerTasks(t *testing.T) {
	e, ctx := setup(t)
	rn := NewRunner(ctx, e, 2)
	r, err := rn.Enqueue(KindBackup, "a", "full", "manual")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rn.Enqueue(KindBackup, "a", "", "manual"); err != ErrAlreadyQueued {
		t.Fatalf("duplicate enqueue: %v", err)
	}
	wait(t, r)
	if r.Error != "" || r.Result == nil || r.Result.Type != "full" {
		t.Fatalf("backup run: %+v", r)
	}
	for _, kind := range []string{KindCheck, KindPrune} {
		x, err := rn.Enqueue(kind, "r", "", "manual")
		if err != nil {
			t.Fatal(err)
		}
		wait(t, x)
		if x.Error != "" {
			t.Fatalf("%s: %s", kind, x.Error)
		}
	}
	if _, err := rn.Enqueue(KindBackup, "nope", "", "manual"); err == nil {
		t.Fatal("unknown job accepted")
	}
	if _, err := rn.Enqueue("explode", "r", "", "manual"); err == nil {
		t.Fatal("unknown kind accepted")
	}
	if len(rn.Recent()) != 3 || len(rn.Active()) != 0 {
		t.Fatalf("recent %d active %d", len(rn.Recent()), len(rn.Active()))
	}
}

func TestSchedulerFiresStrongestTypeAndRetries(t *testing.T) {
	e, ctx := setup(t)
	rn := NewRunner(ctx, e, 1)
	s := NewScheduler(rn)
	entries := s.Entries()
	// a: 2 schedules, broken: 1, prune+check: 2; "off" is disabled.
	if len(entries) != 5 {
		t.Fatalf("%d entries", len(entries))
	}
	// Pretend it is Sunday 02:00 — both schedules of job a are due.
	sunday := time.Date(2030, 1, 6, 2, 0, 0, 0, time.Local)
	s.now = func() time.Time { return sunday }
	for _, en := range s.entries {
		en.Next = sunday.AddDate(1, 0, 0)
		if en.Target == "a" {
			en.Next = sunday
		}
	}
	s.fireDue()
	act := rn.Active()
	if len(act) != 1 || act[0].Type != "full" {
		t.Fatalf("expected one full run of a, got %+v", act)
	}
	wait(t, act[0].Run)
	// broken: scheduled failure is retried twice, then given up.
	for _, en := range s.entries {
		en.Next = sunday.AddDate(1, 0, 0)
		if en.Target == "broken" {
			en.Next = sunday
		}
	}
	s.fireDue()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		n := 0
		for _, r := range rn.Recent() {
			if r.Target == "broken" {
				n++
			}
		}
		if n == 3 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	var triggers []string
	for _, r := range rn.Recent() {
		if r.Target == "broken" {
			triggers = append(triggers, r.Trigger)
			if r.Error == "" {
				t.Fatal("broken job succeeded")
			}
		}
	}
	if strings.Join(triggers, ",") != "retry,retry,schedule" {
		t.Fatalf("triggers: %v", triggers)
	}
	time.Sleep(100 * time.Millisecond)
	if len(rn.Active()) != 0 {
		t.Fatal("retried more than configured")
	}
}

func TestCatchUp(t *testing.T) {
	e, ctx := setup(t)
	rn := NewRunner(ctx, e, 1)
	s := NewScheduler(rn)
	if got := s.CatchUp(); len(got) != 0 {
		t.Fatal("catch-up without history")
	}
	r, _ := rn.Enqueue(KindBackup, "a", "", "manual")
	wait(t, r)
	// Two days later the daily schedule has been missed.
	s.now = func() time.Time { return time.Now().Add(48 * time.Hour) }
	got := s.CatchUp()
	if len(got) != 1 || got[0] != "a" {
		t.Fatalf("catch-up: %v", got)
	}
	rn.Wait()
}

func TestControlAPI(t *testing.T) {
	e, ctx := setup(t)
	d, err := New(ctx, e, Options{Version: "test", Reload: func() (*config.Config, error) { return e.Config(), nil }})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(d.Handler())
	defer ts.Close()
	c := &Client{Addr: strings.TrimPrefix(ts.URL, "http://"), Token: d.Token(), http: ts.Client()}
	st, err := c.Status()
	if err != nil || st.Version != "test" || len(st.Schedule) != 5 {
		t.Fatalf("status: %+v %v", st, err)
	}
	run, err := c.Run(KindBackup, "a", "full")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 300; i++ {
		v, err := c.GetRun(run.ID)
		if err == nil && v.State == "done" {
			if v.Error != "" {
				t.Fatal(v.Error)
			}
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err := c.Reload(); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Run(KindBackup, "missing", ""); err == nil {
		t.Fatal("unknown job accepted")
	}
	bad := &Client{Addr: c.Addr, Token: "wrong", http: ts.Client()}
	if _, err := bad.Status(); err == nil {
		t.Fatal("wrong token accepted")
	}
	st, _ = c.Status()
	if st.Last["a"] == nil || st.Last["a"].Status != "success" {
		t.Fatalf("last results: %+v", st.Last)
	}
}

func TestDaemonRunWritesToken(t *testing.T) {
	e, ctx := setup(t)
	ctx, cancel := context.WithCancel(ctx)
	d, err := New(ctx, e, Options{})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	var c *Client
	for i := 0; i < 100; i++ {
		if c, err = NewClient(e.Config()); err == nil {
			if _, err = c.Status(); err == nil {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatal("client could not reach daemon:", err)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := NewClient(e.Config()); err != ErrNotRunning {
		t.Fatal("token file not removed on shutdown")
	}
}
