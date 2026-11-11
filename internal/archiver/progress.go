package archiver

import (
	"sync"
	"sync/atomic"
	"time"
)

// Progress tracks a running backup. All methods are safe for concurrent use.
type Progress struct {
	Start time.Time

	filesDone   atomic.Int64
	bytesDone   atomic.Int64
	filesTotal  atomic.Int64
	bytesTotal  atomic.Int64
	scanDone    atomic.Bool
	bytesNew    atomic.Int64
	bytesStored atomic.Int64
	errors      atomic.Int64

	mu      sync.Mutex
	current string
}

// ProgressStats is a point-in-time copy of Progress.
type ProgressStats struct {
	Elapsed     time.Duration `json:"elapsed"`
	FilesDone   int64         `json:"files_done"`
	BytesDone   int64         `json:"bytes_done"`
	FilesTotal  int64         `json:"files_total"`
	BytesTotal  int64         `json:"bytes_total"`
	ScanDone    bool          `json:"scan_done"`
	BytesNew    int64         `json:"bytes_new"`
	BytesStored int64         `json:"bytes_stored"`
	Errors      int64         `json:"errors"`
	Current     string        `json:"current"`
}

// Percent returns completion in [0,100] or -1 if unknown.
func (s ProgressStats) Percent() float64 {
	if !s.ScanDone || s.BytesTotal == 0 {
		if s.ScanDone && s.FilesTotal > 0 {
			return 100 * float64(s.FilesDone) / float64(s.FilesTotal)
		}
		return -1
	}
	p := 100 * float64(s.BytesDone) / float64(s.BytesTotal)
	if p > 100 {
		p = 100
	}
	return p
}

// Stats returns the current values.
func (p *Progress) Stats() ProgressStats {
	if p == nil {
		return ProgressStats{}
	}
	p.mu.Lock()
	cur := p.current
	p.mu.Unlock()
	return ProgressStats{
		Elapsed:     time.Since(p.Start),
		FilesDone:   p.filesDone.Load(),
		BytesDone:   p.bytesDone.Load(),
		FilesTotal:  p.filesTotal.Load(),
		BytesTotal:  p.bytesTotal.Load(),
		ScanDone:    p.scanDone.Load(),
		BytesNew:    p.bytesNew.Load(),
		BytesStored: p.bytesStored.Load(),
		Errors:      p.errors.Load(),
		Current:     cur,
	}
}

func (p *Progress) setCurrent(s string) {
	p.mu.Lock()
	p.current = s
	p.mu.Unlock()
}
