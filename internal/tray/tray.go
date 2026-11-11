// Package tray shows Herakles in the system tray (notification area) with a
// menu to open the GUI and to quit. It runs on Linux and the BSDs through the
// StatusNotifierItem D-Bus protocol (KDE, Xfce, Cinnamon, MATE, LXQt, GNOME
// with the AppIndicator extension, waybar, ...) and natively on Windows.
package tray

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/lamisator/herakles/internal/daemon"
	"github.com/lamisator/herakles/internal/ui"
)

// State selects the icon badge.
type State int

const (
	Idle State = iota
	Busy
	Locked
	Failed
)

// Status is what the tray shows: the icon state and a one-line summary
// used for the disabled status menu entry and the tooltip.
type Status struct {
	State State
	Line  string
}

// ErrUnsupported is returned by Run on platforms without tray support.
var ErrUnsupported = errors.New("system tray not supported on this platform")

// Options configure the tray icon.
type Options struct {
	// Status is polled every Interval (default 2s) to update icon and menu.
	Status   func() Status
	Interval time.Duration
	// Open is called by "Open Herakles" and, where supported, a left click.
	Open func()
	// Quit is called by "Quit Herakles" before the tray goes away.
	Quit func()
	// HostWait is how long to wait for the desktop's tray host before
	// calling Warn (default 30s); the icon still appears if one starts later.
	HostWait time.Duration
	Warn     func(msg string)
}

// Run shows the tray icon and blocks until ctx is cancelled or the user
// picks Quit. It must be called from the main goroutine.
func Run(ctx context.Context, opts Options) error {
	if opts.Interval <= 0 {
		opts.Interval = 2 * time.Second
	}
	if opts.HostWait <= 0 {
		opts.HostWait = 30 * time.Second
	}
	if opts.Status == nil {
		opts.Status = func() Status { return Status{Line: "Running"} }
	}
	return run(ctx, opts)
}

// Summarize turns the daemon status into the tray status. locked lists
// repositories that still need a password before scheduled backups work.
// Running tasks win over locked repositories, which win over failures.
func Summarize(st daemon.Status, locked []string, now time.Time) Status {
	if len(st.Active) > 0 {
		r := st.Active[0]
		line := taskLabel(r.Kind, r.Target)
		if r.State == "queued" {
			line += " (queued)"
		} else if r.ProgressStats != nil {
			if p := r.ProgressStats.Percent(); p >= 0 {
				line += fmt.Sprintf(" %.0f %%", p)
			}
		}
		if n := len(st.Active) - 1; n > 0 {
			line += fmt.Sprintf(" (+%d more)", n)
		}
		return Status{State: Busy, Line: line}
	}
	if len(locked) > 0 {
		return Status{State: Locked, Line: "Locked: " + strings.Join(locked, ", ") + " (open Herakles to unlock)"}
	}
	var failed []string
	for job, r := range st.Last {
		if r != nil && r.Status == "failure" {
			failed = append(failed, job)
		}
	}
	if len(failed) > 0 {
		sort.Strings(failed)
		return Status{State: Failed, Line: "Last backup failed: " + strings.Join(failed, ", ")}
	}
	var next *daemon.Entry
	for i := range st.Schedule {
		e := &st.Schedule[i]
		if e.Kind == daemon.KindBackup && e.Next.After(now) && (next == nil || e.Next.Before(next.Next)) {
			next = e
		}
	}
	if next == nil {
		return Status{State: Idle, Line: "No backups scheduled"}
	}
	return Status{State: Idle, Line: fmt.Sprintf("Next backup: %s in %s", next.Target, ui.Until(next.Next))}
}

func taskLabel(kind, target string) string {
	switch kind {
	case daemon.KindBackup:
		return "Backing up " + target
	case daemon.KindPrune:
		return "Pruning " + target
	case daemon.KindCheck:
		return "Checking " + target
	case daemon.KindForget:
		return "Applying retention to " + target
	}
	return kind + " " + target
}
