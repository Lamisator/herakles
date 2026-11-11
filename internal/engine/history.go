package engine

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// History stores recent job results in the state directory.
type History struct {
	path string
	mu   sync.Mutex
}

var histMu sync.Mutex // serialises writers within one process

// MaxHistoryPerJob bounds stored results per job.
const MaxHistoryPerJob = 100

// History returns the run history store.
func (e *Engine) History() *History {
	return &History{path: filepath.Join(e.Config().GetStateDir(), "history.json")}
}

// Load returns all stored results, newest first.
func (h *History) Load() ([]*JobResult, error) {
	data, err := os.ReadFile(h.path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []*JobResult
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Start.After(out[j].Start) })
	return out, nil
}

// Add records a result.
func (h *History) Add(r *JobResult) error {
	histMu.Lock()
	defer histMu.Unlock()
	all, err := h.Load()
	if err != nil {
		all = nil // corrupted history is replaced
	}
	all = append([]*JobResult{r}, all...)
	count := map[string]int{}
	var kept []*JobResult
	for _, x := range all {
		count[x.Job]++
		if count[x.Job] <= MaxHistoryPerJob {
			kept = append(kept, x)
		}
	}
	data, err := json.MarshalIndent(kept, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(h.path), 0o700); err != nil {
		return err
	}
	tmp := h.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, h.path)
}

// Last returns the most recent result of a job (or nil).
func (h *History) Last(job string) *JobResult {
	all, _ := h.Load()
	for _, r := range all {
		if r.Job == job {
			return r
		}
	}
	return nil
}
