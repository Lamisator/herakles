package retention

import (
	"testing"
	"time"

	"github.com/lamisator/herakles/internal/id"
	"github.com/lamisator/herakles/internal/repository"
)

func daily(n int, start time.Time) []*repository.Snapshot {
	var out []*repository.Snapshot
	for i := 0; i < n; i++ {
		t := start.AddDate(0, 0, i)
		typ := repository.TypeIncremental
		if t.Weekday() == time.Sunday {
			typ = repository.TypeFull
		}
		s := &repository.Snapshot{Time: t, Job: "home", Hostname: "h", Type: typ}
		s.SetID(id.Random())
		out = append(out, s)
	}
	return out
}

func kept(groups []*Group) map[string]bool {
	m := map[string]bool{}
	for _, g := range groups {
		for _, d := range g.Decisions {
			if d.Keep {
				m[d.Snapshot.Time.Format("2006-01-02")] = true
			}
		}
	}
	return m
}

func TestParseDuration(t *testing.T) {
	d, err := ParseDuration("1y2m3w4d5h")
	if err != nil || d != (Duration{1, 2, 25, 5}) {
		t.Fatalf("%+v %v", d, err)
	}
	for _, bad := range []string{"5", "x", "3q", "d"} {
		if _, err := ParseDuration(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	ref := time.Date(2024, 3, 31, 12, 0, 0, 0, time.UTC)
	if got := (Duration{Months: 1}).Before(ref); got.Month() != time.March || got.Day() != 2 {
		// Go normalises Feb 31 -> Mar 2; document the behaviour.
		t.Logf("one month before %v is %v", ref, got)
	}
}

func TestEmptyPolicyKeepsAll(t *testing.T) {
	snaps := daily(10, time.Date(2024, 1, 1, 2, 0, 0, 0, time.Local))
	if len(Forget(Apply(snaps, Policy{}))) != 0 {
		t.Fatal("empty policy removed snapshots")
	}
}

func TestKeepLastAndDaily(t *testing.T) {
	snaps := daily(30, time.Date(2024, 1, 1, 2, 0, 0, 0, time.Local))
	// Add a second snapshot on the last day.
	extra := &repository.Snapshot{Time: snaps[29].Time.Add(time.Hour), Job: "home", Hostname: "h"}
	extra.SetID(id.Random())
	snaps = append(snaps, extra)
	g := Apply(snaps, Policy{KeepLast: 2, KeepDaily: 5})
	k := kept(g)
	if len(Forget(g)) != 31-6 { // last 2 (same day) + 5 days with first being the newest
		t.Fatalf("forget %d, kept %v", len(Forget(g)), k)
	}
	for _, day := range []string{"2024-01-30", "2024-01-29", "2024-01-26"} {
		if !k[day] {
			t.Errorf("%s not kept", day)
		}
	}
}

func TestWeeklyMonthlyFull(t *testing.T) {
	// A year of daily backups: full on Sundays, incremental otherwise.
	snaps := daily(365, time.Date(2023, 1, 1, 3, 0, 0, 0, time.Local))
	p := Policy{KeepDaily: 7, KeepWeekly: 4, KeepMonthly: 12, KeepFull: 2}
	g := Apply(snaps, p)
	k := kept(g)
	// 7 daily + weekly (4, newest week overlaps) + 12 monthly (overlaps) + 2 full
	if len(k) < 7+3+11 || len(k) > 7+4+12+2 {
		t.Fatalf("kept %d snapshots: %v", len(k), k)
	}
	// Newest snapshot is always kept.
	if !k["2023-12-31"] {
		t.Fatal("newest not kept")
	}
	// The last month's representative (newest snapshot of each month) is kept.
	for _, m := range []string{"2023-01-31", "2023-06-30", "2023-11-30"} {
		if !k[m] {
			t.Errorf("monthly %s not kept", m)
		}
	}
	// Two newest full backups are kept.
	full := 0
	for _, d := range g[0].Decisions {
		for _, r := range d.Reasons {
			if r == "full" {
				full++
			}
		}
	}
	if full != 2 {
		t.Fatalf("kept %d full backups", full)
	}
}

func TestWithinAndTags(t *testing.T) {
	snaps := daily(60, time.Date(2024, 1, 1, 2, 0, 0, 0, time.Local))
	snaps[3].Tags = []string{"important"}
	g := Apply(snaps, Policy{KeepWithin: Duration{Days: 10}, KeepTags: []string{"important"}, KeepWithinWeekly: Duration{Months: 1}})
	k := kept(g)
	if !k[snaps[3].Time.Format("2006-01-02")] {
		t.Fatal("tagged snapshot not kept")
	}
	for i := 49; i < 60; i++ {
		if !k[snaps[i].Time.Format("2006-01-02")] {
			t.Errorf("snapshot within 10 days not kept: %v", snaps[i].Time)
		}
	}
	if k[snaps[10].Time.Format("2006-01-02")] {
		t.Error("old snapshot kept")
	}
}

func TestGrouping(t *testing.T) {
	a := daily(5, time.Date(2024, 1, 1, 2, 0, 0, 0, time.Local))
	b := daily(5, time.Date(2024, 1, 1, 2, 0, 0, 0, time.Local))
	for _, s := range b {
		s.Job = "other"
	}
	g := Apply(append(a, b...), Policy{KeepLast: 1})
	if len(g) != 2 || len(Forget(g)) != 8 {
		t.Fatalf("groups %d forget %d", len(g), len(Forget(g)))
	}
	g = Apply(append(a, b...), Policy{KeepLast: 1, GroupBy: "host"})
	if len(g) != 1 || len(Forget(g)) != 9 {
		t.Fatalf("host grouping: groups %d forget %d", len(g), len(Forget(g)))
	}
	if (Policy{GroupBy: "bogus"}).Validate() == nil {
		t.Fatal("invalid group_by accepted")
	}
}
