//go:build ((linux || freebsd || openbsd || netbsd) && !android) || windows || (darwin && cgo)

package tray

import (
	"context"
	"sync"
	"time"

	"fyne.io/systray"
)

func run(ctx context.Context, opts Options) error {
	if err := available(); err != nil {
		return err
	}
	if !hostReady(ctx, opts.HostWait) && opts.Warn != nil {
		opts.Warn(noHostWarning)
	}
	var once sync.Once
	quit := func() {
		once.Do(func() {
			if opts.Quit != nil {
				opts.Quit()
			}
			systray.Quit()
		})
	}
	onReady := func() {
		systray.SetTitle("Herakles")
		systray.SetOnTapped(opts.Open)
		status := systray.AddMenuItem("Herakles", "")
		status.Disable()
		systray.AddSeparator()
		open := systray.AddMenuItem("Open Herakles", "Open the Herakles window")
		upd := systray.AddMenuItem("Update", "Download and install the new Herakles version, then restart it")
		upd.Hide()
		systray.AddSeparator()
		exit := systray.AddMenuItem(opts.QuitLabel, opts.QuitHint)

		last := Status{State: -1}
		lastUpd := ""
		update := func() {
			if opts.Update != nil {
				if label := opts.Update(); label != lastUpd {
					if label == "" {
						upd.Hide()
					} else {
						upd.SetTitle(label)
						upd.Show()
					}
					lastUpd = label
				}
			}
			st := opts.Status()
			if st == last {
				return
			}
			if st.State != last.State {
				systray.SetIcon(iconBytes(st.State))
			}
			status.SetTitle(st.Line)
			systray.SetTooltip("Herakles: " + st.Line)
			last = st
		}
		update()
		go promoteIcon(ctx)
		go func() {
			t := time.NewTicker(opts.Interval)
			defer t.Stop()
			for {
				select {
				case <-open.ClickedCh:
					if opts.Open != nil {
						opts.Open()
					}
				case <-upd.ClickedCh:
					if opts.Install != nil {
						go opts.Install()
					}
				case <-exit.ClickedCh:
					quit()
					return
				case <-ctx.Done():
					quit()
					return
				case <-t.C:
					update()
				}
			}
		}()
	}
	systray.Run(onReady, nil)
	return nil
}
