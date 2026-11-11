//go:build linux && !race

// Not built with -race: fyne.io/systray updates the icon property while
// godbus may be encoding it for a reply, which the race detector reports
// inside those libraries.

package tray

import (
	"bufio"
	"context"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

// watcher is a minimal StatusNotifierWatcher, the part of a desktop panel
// that tray icons register with.
type watcher struct{ items chan string }

func (w *watcher) RegisterStatusNotifierItem(sender dbus.Sender, path string) *dbus.Error {
	w.items <- string(sender)
	return nil
}

// privateBus starts a dbus-daemon for the test and points the session bus
// address at it.
func privateBus(t *testing.T) {
	bin, err := exec.LookPath("dbus-daemon")
	if err != nil {
		t.Skip("dbus-daemon not installed")
	}
	cmd := exec.Command(bin, "--session", "--nofork", "--print-address")
	out, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	addr, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", strings.TrimSpace(addr))
}

type layout struct {
	ID       int32
	Props    map[string]dbus.Variant
	Children []dbus.Variant
}

func menuLabels(t *testing.T, menu dbus.BusObject) (labels map[string]int32, disabled map[string]bool) {
	var rev uint32
	var root layout
	if err := menu.Call("com.canonical.dbusmenu.GetLayout", 0, int32(0), int32(-1), []string{}).Store(&rev, &root); err != nil {
		t.Fatal(err)
	}
	labels, disabled = map[string]int32{}, map[string]bool{}
	for _, c := range root.Children {
		var it layout
		if err := dbus.Store([]any{c.Value()}, &it); err != nil {
			t.Fatal(err)
		}
		if l, ok := it.Props["label"]; ok {
			s := l.Value().(string)
			labels[s] = it.ID
			if e, ok := it.Props["enabled"]; ok && !e.Value().(bool) {
				disabled[s] = true
			}
		}
	}
	return labels, disabled
}

// TestTrayOverDBus runs the real tray against a private session bus and a
// fake panel: it registers, publishes icon, tooltip and menu, reacts to
// clicks on "Open Herakles" and a left click, and quits from the menu.
// The panel starts after the tray, as it may at login.
func TestTrayOverDBus(t *testing.T) {
	privateBus(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// Start before the panel, as at login: Run warns, then registers once
	// the watcher appears.
	var opened, quit atomic.Int32
	warned := make(chan string, 1)
	status := atomic.Value{}
	status.Store(Status{Idle, "Next backup: home in 2.0 h"})
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{
			Status:   func() Status { return status.Load().(Status) },
			Interval: 50 * time.Millisecond,
			Open:     func() { opened.Add(1) },
			Quit:     func() { quit.Add(1) },
			HostWait: 200 * time.Millisecond,
			Warn:     func(m string) { warned <- m },
		})
	}()
	select {
	case m := <-warned:
		if !strings.Contains(m, "no system tray") {
			t.Fatal(m)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no warning without a tray host")
	}
	time.Sleep(200 * time.Millisecond)

	host, err := dbus.ConnectSessionBus()
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	w := &watcher{items: make(chan string, 4)}
	if err := host.Export(w, "/StatusNotifierWatcher", "org.kde.StatusNotifierWatcher"); err != nil {
		t.Fatal(err)
	}
	if r, err := host.RequestName("org.kde.StatusNotifierWatcher", dbus.NameFlagDoNotQueue); err != nil || r != dbus.RequestNameReplyPrimaryOwner {
		t.Fatal(r, err)
	}

	var item string
	select {
	case item = <-w.items:
	case <-time.After(10 * time.Second):
		t.Fatal("tray never registered with the watcher")
	}
	sni := host.Object(item, "/StatusNotifierItem")
	menu := host.Object(item, "/StatusNotifierItem/menu")

	waitFor := func(what string, cond func() bool) {
		t.Helper()
		for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
			if cond() {
				return
			}
		}
		t.Fatal("timed out waiting for " + what)
	}

	waitFor("icon", func() bool {
		v, err := sni.GetProperty("org.kde.StatusNotifierItem.IconPixmap")
		if err != nil {
			return false
		}
		px, ok := v.Value().([][]any)
		return ok && len(px) == 1 && px[0][0].(int32) == 64
	})
	waitFor("tooltip", func() bool {
		v, err := sni.GetProperty("org.kde.StatusNotifierItem.ToolTip")
		return err == nil && strings.Contains(v.String(), "Herakles: Next backup: home in 2.0 h")
	})

	labels, disabled := menuLabels(t, menu)
	for _, l := range []string{"Next backup: home in 2.0 h", "Open Herakles", "Quit Herakles"} {
		if _, ok := labels[l]; !ok {
			t.Fatalf("menu lacks %q: %v", l, labels)
		}
	}
	if !disabled["Next backup: home in 2.0 h"] || disabled["Open Herakles"] {
		t.Fatalf("status line must be disabled, actions enabled: %v", disabled)
	}

	// The status line follows the daemon.
	status.Store(Status{Busy, "Backing up home 42 %"})
	waitFor("status update", func() bool {
		l, _ := menuLabels(t, menu)
		_, ok := l["Backing up home 42 %"]
		return ok
	})

	click := func(id int32) {
		t.Helper()
		if err := menu.Call("com.canonical.dbusmenu.Event", 0, id, "clicked", dbus.MakeVariant(""), uint32(0)).Err; err != nil {
			t.Fatal(err)
		}
	}
	labels, _ = menuLabels(t, menu)
	click(labels["Open Herakles"])
	waitFor("open from menu", func() bool { return opened.Load() == 1 })
	if err := sni.Call("org.kde.StatusNotifierItem.Activate", 0, int32(0), int32(0)).Err; err != nil {
		t.Fatal(err)
	}
	waitFor("open on left click", func() bool { return opened.Load() == 2 })

	// The tray may leave the bus before answering this click.
	_ = menu.Call("com.canonical.dbusmenu.Event", 0, labels["Quit Herakles"], "clicked", dbus.MakeVariant(""), uint32(0)).Err
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after Quit")
	}
	if quit.Load() != 1 {
		t.Fatalf("Quit callback ran %d times", quit.Load())
	}
}
