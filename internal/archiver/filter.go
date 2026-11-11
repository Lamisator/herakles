package archiver

import (
	"path/filepath"
	"runtime"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// Filter decides which paths are excluded.
type Filter struct {
	patterns []string
}

// NewFilter compiles exclude patterns. Pattern rules:
//   - "*.tmp" (no slash) matches the base name anywhere
//   - "/var/cache" or "C:/Temp" (absolute) matches that path and below
//   - "**/node_modules" / "cache/*" (relative with slash) match anywhere
//
// Matching is case-insensitive on Windows.
func NewFilter(patterns []string) (*Filter, error) {
	f := &Filter{}
	for _, p := range patterns {
		p = strings.TrimSpace(p)
		if p == "" || strings.HasPrefix(p, "#") {
			continue
		}
		p = normalize(p)
		p = strings.TrimSuffix(p, "/")
		if !doublestar.ValidatePattern(p) {
			return nil, &PatternError{Pattern: p}
		}
		f.patterns = append(f.patterns, p)
	}
	return f, nil
}

// PatternError reports an invalid pattern.
type PatternError struct{ Pattern string }

func (e *PatternError) Error() string { return "invalid exclude pattern " + e.Pattern }

func normalize(p string) string {
	p = filepath.ToSlash(p)
	if runtime.GOOS == "windows" {
		p = strings.ToLower(p)
	}
	return p
}

func isAbs(p string) bool {
	return strings.HasPrefix(p, "/") || (len(p) >= 2 && p[1] == ':')
}

// Excluded reports whether path (absolute, OS form) is excluded.
func (f *Filter) Excluded(path string) bool {
	if f == nil || len(f.patterns) == 0 {
		return false
	}
	p := normalize(path)
	base := p[strings.LastIndex(p, "/")+1:]
	for _, pat := range f.patterns {
		switch {
		case !strings.Contains(pat, "/"):
			if ok, _ := doublestar.Match(pat, base); ok {
				return true
			}
		case isAbs(pat):
			if ok, _ := doublestar.Match(pat, p); ok {
				return true
			}
		default:
			if ok, _ := doublestar.Match("**/"+strings.TrimPrefix(pat, "**/"), strings.TrimPrefix(p, "/")); ok {
				return true
			}
		}
	}
	return false
}
