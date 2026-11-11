// Package retention decides which snapshots to keep (grandfather-father-son
// and more) and which to forget.
package retention

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/lamisator/herakles/internal/repository"
)

// Duration is a calendar duration such as "1y6m", "2w", "36h", "90d".
type Duration struct {
	Years, Months, Days, Hours int
}

// ParseDuration parses y (years), m (months), w (weeks), d (days), h (hours).
func ParseDuration(s string) (Duration, error) {
	var d Duration
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" || s == "0" {
		return d, nil
	}
	num := ""
	for _, c := range s {
		if c >= '0' && c <= '9' {
			num += string(c)
			continue
		}
		if num == "" {
			return d, fmt.Errorf("invalid duration %q", s)
		}
		n, _ := strconv.Atoi(num)
		num = ""
		switch c {
		case 'y':
			d.Years += n
		case 'm':
			d.Months += n
		case 'w':
			d.Days += 7 * n
		case 'd':
			d.Days += n
		case 'h':
			d.Hours += n
		default:
			return d, fmt.Errorf("invalid duration unit %q in %q (use y, m, w, d, h)", c, s)
		}
	}
	if num != "" {
		return d, fmt.Errorf("duration %q is missing a unit (y, m, w, d, h)", s)
	}
	return d, nil
}

// IsZero reports whether the duration is empty.
func (d Duration) IsZero() bool { return d == Duration{} }

// Before returns t minus the duration.
func (d Duration) Before(t time.Time) time.Time {
	return t.AddDate(-d.Years, -d.Months, -d.Days).Add(-time.Duration(d.Hours) * time.Hour)
}

func (d Duration) String() string {
	var b strings.Builder
	for _, x := range []struct {
		n int
		u string
	}{{d.Years, "y"}, {d.Months, "m"}, {d.Days, "d"}, {d.Hours, "h"}} {
		if x.n > 0 {
			fmt.Fprintf(&b, "%d%s", x.n, x.u)
		}
	}
	if b.Len() == 0 {
		return "0"
	}
	return b.String()
}

// MarshalText implements encoding.TextMarshaler.
func (d Duration) MarshalText() ([]byte, error) { return []byte(d.String()), nil }

// UnmarshalText implements encoding.TextUnmarshaler.
func (d *Duration) UnmarshalText(b []byte) error {
	x, err := ParseDuration(string(b))
	if err != nil {
		return err
	}
	*d = x
	return nil
}

// Policy describes what to keep. A snapshot is kept if any rule keeps it.
type Policy struct {
	KeepLast    int `yaml:"keep_last,omitempty" json:"keep_last,omitempty"`
	KeepHourly  int `yaml:"keep_hourly,omitempty" json:"keep_hourly,omitempty"`
	KeepDaily   int `yaml:"keep_daily,omitempty" json:"keep_daily,omitempty"`
	KeepWeekly  int `yaml:"keep_weekly,omitempty" json:"keep_weekly,omitempty"`
	KeepMonthly int `yaml:"keep_monthly,omitempty" json:"keep_monthly,omitempty"`
	KeepYearly  int `yaml:"keep_yearly,omitempty" json:"keep_yearly,omitempty"`
	// KeepFull keeps the N most recent full backups.
	KeepFull int `yaml:"keep_full,omitempty" json:"keep_full,omitempty"`
	// KeepWithin keeps everything newer than the duration (relative to the
	// newest snapshot of the group, so stalled backups never empty a repository).
	KeepWithin        Duration `yaml:"keep_within,omitempty" json:"keep_within,omitempty"`
	KeepWithinHourly  Duration `yaml:"keep_within_hourly,omitempty" json:"keep_within_hourly,omitempty"`
	KeepWithinDaily   Duration `yaml:"keep_within_daily,omitempty" json:"keep_within_daily,omitempty"`
	KeepWithinWeekly  Duration `yaml:"keep_within_weekly,omitempty" json:"keep_within_weekly,omitempty"`
	KeepWithinMonthly Duration `yaml:"keep_within_monthly,omitempty" json:"keep_within_monthly,omitempty"`
	KeepWithinYearly  Duration `yaml:"keep_within_yearly,omitempty" json:"keep_within_yearly,omitempty"`
	KeepTags          []string `yaml:"keep_tags,omitempty" json:"keep_tags,omitempty"`
	// GroupBy partitions snapshots before applying the policy:
	// any combination of "job", "host", "paths", "tags" (default "job,host").
	GroupBy string `yaml:"group_by,omitempty" json:"group_by,omitempty"`
}

// Empty reports whether no keep rule is set.
func (p Policy) Empty() bool {
	return p.KeepLast == 0 && p.KeepHourly == 0 && p.KeepDaily == 0 && p.KeepWeekly == 0 &&
		p.KeepMonthly == 0 && p.KeepYearly == 0 && p.KeepFull == 0 && p.KeepWithin.IsZero() &&
		p.KeepWithinHourly.IsZero() && p.KeepWithinDaily.IsZero() && p.KeepWithinWeekly.IsZero() &&
		p.KeepWithinMonthly.IsZero() && p.KeepWithinYearly.IsZero() && len(p.KeepTags) == 0
}

// Validate checks the policy.
func (p Policy) Validate() error {
	for _, n := range []int{p.KeepLast, p.KeepHourly, p.KeepDaily, p.KeepWeekly, p.KeepMonthly, p.KeepYearly, p.KeepFull} {
		if n < -1 {
			return fmt.Errorf("retention counts must be >= 0 (or -1 for unlimited)")
		}
	}
	for _, g := range splitGroup(p.GroupBy) {
		switch g {
		case "job", "host", "paths", "tags":
		default:
			return fmt.Errorf("invalid group_by value %q (use job, host, paths, tags)", g)
		}
	}
	return nil
}

func splitGroup(s string) []string {
	if strings.TrimSpace(s) == "" {
		return []string{"job", "host"}
	}
	var out []string
	for _, x := range strings.Split(s, ",") {
		if x = strings.TrimSpace(x); x != "" && x != "none" {
			out = append(out, x)
		}
	}
	return out
}

// Decision is the outcome for one snapshot.
type Decision struct {
	Snapshot *repository.Snapshot `json:"-"`
	Keep     bool                 `json:"keep"`
	Reasons  []string             `json:"reasons,omitempty"`
}

// Group is a set of snapshots evaluated together.
type Group struct {
	Key       string      `json:"key"`
	Decisions []*Decision `json:"decisions"` // newest first
}

// GroupKey computes the grouping key of a snapshot.
func GroupKey(s *repository.Snapshot, groupBy string) string {
	var parts []string
	for _, g := range splitGroup(groupBy) {
		switch g {
		case "job":
			parts = append(parts, "job="+s.Job)
		case "host":
			parts = append(parts, "host="+s.Hostname)
		case "paths":
			p := append([]string(nil), s.Paths...)
			sort.Strings(p)
			parts = append(parts, "paths="+strings.Join(p, ":"))
		case "tags":
			t := append([]string(nil), s.Tags...)
			sort.Strings(t)
			parts = append(parts, "tags="+strings.Join(t, ":"))
		}
	}
	return strings.Join(parts, " ")
}

type bucketRule struct {
	name  string
	count int
	key   func(time.Time) string
	last  string
}

// Apply evaluates the policy. If the policy is empty every snapshot is kept.
func Apply(snaps []*repository.Snapshot, p Policy) []*Group {
	groups := map[string][]*repository.Snapshot{}
	var keys []string
	for _, s := range snaps {
		k := GroupKey(s, p.GroupBy)
		if _, ok := groups[k]; !ok {
			keys = append(keys, k)
		}
		groups[k] = append(groups[k], s)
	}
	sort.Strings(keys)
	var out []*Group
	for _, k := range keys {
		out = append(out, &Group{Key: k, Decisions: applyGroup(groups[k], p)})
	}
	return out
}

func applyGroup(snaps []*repository.Snapshot, p Policy) []*Decision {
	sorted := append([]*repository.Snapshot(nil), snaps...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Time.After(sorted[j].Time) })
	decisions := make([]*Decision, len(sorted))
	for i, s := range sorted {
		decisions[i] = &Decision{Snapshot: s}
	}
	if p.Empty() {
		for _, d := range decisions {
			d.Keep, d.Reasons = true, []string{"no policy"}
		}
		return decisions
	}
	ref := sorted[0].Time
	rules := []*bucketRule{
		{name: "last", count: p.KeepLast, key: nil},
		{name: "hourly", count: p.KeepHourly, key: func(t time.Time) string { return t.Local().Format("2006-01-02 15") }},
		{name: "daily", count: p.KeepDaily, key: func(t time.Time) string { return t.Local().Format("2006-01-02") }},
		{name: "weekly", count: p.KeepWeekly, key: func(t time.Time) string {
			y, w := t.Local().ISOWeek()
			return fmt.Sprintf("%04d-W%02d", y, w)
		}},
		{name: "monthly", count: p.KeepMonthly, key: func(t time.Time) string { return t.Local().Format("2006-01") }},
		{name: "yearly", count: p.KeepYearly, key: func(t time.Time) string { return t.Local().Format("2006") }},
	}
	within := []struct {
		name string
		d    Duration
		rule *bucketRule
	}{
		{"within-hourly", p.KeepWithinHourly, &bucketRule{key: rules[1].key}},
		{"within-daily", p.KeepWithinDaily, &bucketRule{key: rules[2].key}},
		{"within-weekly", p.KeepWithinWeekly, &bucketRule{key: rules[3].key}},
		{"within-monthly", p.KeepWithinMonthly, &bucketRule{key: rules[4].key}},
		{"within-yearly", p.KeepWithinYearly, &bucketRule{key: rules[5].key}},
	}
	fullLeft := p.KeepFull
	for i, d := range decisions {
		s := d.Snapshot
		keep := func(reason string) { d.Keep = true; d.Reasons = append(d.Reasons, reason) }
		for _, r := range rules {
			if r.count == 0 {
				continue
			}
			if r.key == nil {
				if r.count < 0 || i < r.count {
					keep("last")
				}
				continue
			}
			k := r.key(s.Time)
			if k != r.last {
				r.last = k
				keep(r.name)
				if r.count > 0 {
					r.count--
				}
			}
		}
		if !p.KeepWithin.IsZero() && !s.Time.Before(p.KeepWithin.Before(ref)) {
			keep("within " + p.KeepWithin.String())
		}
		for _, w := range within {
			if w.d.IsZero() || s.Time.Before(w.d.Before(ref)) {
				continue
			}
			k := w.rule.key(s.Time)
			if k != w.rule.last {
				w.rule.last = k
				keep(w.name + " " + w.d.String())
			}
		}
		if fullLeft != 0 && s.Type == repository.TypeFull {
			keep("full")
			if fullLeft > 0 {
				fullLeft--
			}
		}
		for _, t := range p.KeepTags {
			if s.HasTag(t) {
				keep("tag " + t)
				break
			}
		}
	}
	return decisions
}

// Forget returns the snapshots to remove.
func Forget(groups []*Group) []*repository.Snapshot {
	var out []*repository.Snapshot
	for _, g := range groups {
		for _, d := range g.Decisions {
			if !d.Keep {
				out = append(out, d.Snapshot)
			}
		}
	}
	return out
}
