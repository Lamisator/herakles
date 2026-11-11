package cli

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/lamisator/herakles/internal/archiver"
	"github.com/lamisator/herakles/internal/ui"
)

// progressPrinter renders a live status line on a terminal.
type progressPrinter struct {
	a    *App
	stop chan struct{}
	wg   sync.WaitGroup
}

func (a *App) startProgress(label string, p *archiver.Progress) *progressPrinter {
	pp := &progressPrinter{a: a, stop: make(chan struct{})}
	if a.quiet || a.jsonOut || !a.isTTY() {
		return pp
	}
	pp.wg.Add(1)
	go func() {
		defer pp.wg.Done()
		t := time.NewTicker(250 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-pp.stop:
				fmt.Fprint(a.err, "\r\033[K")
				return
			case <-t.C:
				fmt.Fprint(a.err, "\r\033[K"+progressLine(label, p.Stats()))
			}
		}
	}()
	return pp
}

func (pp *progressPrinter) Stop() {
	select {
	case <-pp.stop:
	default:
		close(pp.stop)
	}
	pp.wg.Wait()
}

func progressLine(label string, s archiver.ProgressStats) string {
	pct := ""
	if p := s.Percent(); p >= 0 {
		pct = fmt.Sprintf("%5.1f%% ", p)
	}
	total := ""
	if s.ScanDone {
		total = "/" + ui.Bytes(s.BytesTotal)
	}
	cur := s.Current
	if len(cur) > 40 {
		cur = "…" + cur[len(cur)-39:]
	}
	line := fmt.Sprintf("%s [%s] %s%d files, %s%s read, %s new", label, ui.Duration(s.Elapsed), pct, s.FilesDone, ui.Bytes(s.BytesDone), total, ui.Bytes(s.BytesNew))
	if s.Errors > 0 {
		line += fmt.Sprintf(", %d errors", s.Errors)
	}
	return strings.TrimSpace(line + "  " + cur)
}
