package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// LogLine is one buffered log record.
type LogLine struct {
	Time    time.Time `json:"time"`
	Level   string    `json:"level"`
	Message string    `json:"message"`
	Attrs   string    `json:"attrs,omitempty"`
}

// LogBuffer keeps the most recent log records in memory.
type LogBuffer struct {
	mu    sync.Mutex
	lines []LogLine
	max   int
}

// NewLogBuffer creates a ring buffer of n lines.
func NewLogBuffer(n int) *LogBuffer { return &LogBuffer{max: n} }

// Lines returns buffered lines, oldest first.
func (b *LogBuffer) Lines() []LogLine {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]LogLine(nil), b.lines...)
}

func (b *LogBuffer) add(l LogLine) {
	b.mu.Lock()
	b.lines = append(b.lines, l)
	if len(b.lines) > b.max {
		b.lines = b.lines[len(b.lines)-b.max:]
	}
	b.mu.Unlock()
}

// TeeHandler forwards records to a handler and a LogBuffer.
type TeeHandler struct {
	next  slog.Handler
	buf   *LogBuffer
	attrs []slog.Attr
}

// NewTeeHandler wraps next.
func NewTeeHandler(next slog.Handler, buf *LogBuffer) *TeeHandler {
	return &TeeHandler{next: next, buf: buf}
}

// Enabled implements slog.Handler.
func (h *TeeHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return l >= slog.LevelInfo || h.next.Enabled(ctx, l)
}

// Handle implements slog.Handler.
func (h *TeeHandler) Handle(ctx context.Context, r slog.Record) error {
	if r.Level >= slog.LevelInfo {
		var sb strings.Builder
		for _, a := range h.attrs {
			fmt.Fprintf(&sb, "%s=%v ", a.Key, a.Value)
		}
		r.Attrs(func(a slog.Attr) bool {
			fmt.Fprintf(&sb, "%s=%v ", a.Key, a.Value)
			return true
		})
		h.buf.add(LogLine{Time: r.Time, Level: r.Level.String(), Message: r.Message, Attrs: strings.TrimSpace(sb.String())})
	}
	if h.next.Enabled(ctx, r.Level) {
		return h.next.Handle(ctx, r)
	}
	return nil
}

// WithAttrs implements slog.Handler.
func (h *TeeHandler) WithAttrs(as []slog.Attr) slog.Handler {
	return &TeeHandler{next: h.next.WithAttrs(as), buf: h.buf, attrs: append(append([]slog.Attr(nil), h.attrs...), as...)}
}

// WithGroup implements slog.Handler.
func (h *TeeHandler) WithGroup(name string) slog.Handler {
	return &TeeHandler{next: h.next.WithGroup(name), buf: h.buf, attrs: h.attrs}
}
