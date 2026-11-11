package backend

import (
	"context"
	"io"

	"golang.org/x/time/rate"
)

// Limited throttles another backend's upload/download bandwidth.
type Limited struct {
	Backend
	up, down *rate.Limiter
}

// NewLimited wraps b with limits in KiB/s (0 = unlimited).
func NewLimited(b Backend, upKiB, downKiB int) *Limited {
	l := &Limited{Backend: b}
	if upKiB > 0 {
		l.up = rate.NewLimiter(rate.Limit(upKiB*1024), 256*1024)
	}
	if downKiB > 0 {
		l.down = rate.NewLimiter(rate.Limit(downKiB*1024), 256*1024)
	}
	return l
}

func wait(ctx context.Context, l *rate.Limiter, n int) error {
	for n > 0 {
		c := n
		if c > l.Burst() {
			c = l.Burst()
		}
		if err := l.WaitN(ctx, c); err != nil {
			return err
		}
		n -= c
	}
	return nil
}

// Save implements Backend.
func (l *Limited) Save(ctx context.Context, h Handle, data []byte) error {
	if l.up != nil {
		if err := wait(ctx, l.up, len(data)); err != nil {
			return err
		}
	}
	return l.Backend.Save(ctx, h, data)
}

type limitedReader struct {
	io.ReadCloser
	ctx context.Context
	l   *rate.Limiter
}

func (r limitedReader) Read(p []byte) (int, error) {
	if len(p) > r.l.Burst() {
		p = p[:r.l.Burst()]
	}
	n, err := r.ReadCloser.Read(p)
	if n > 0 {
		if werr := r.l.WaitN(r.ctx, n); werr != nil {
			return n, werr
		}
	}
	return n, err
}

// Load implements Backend.
func (l *Limited) Load(ctx context.Context, h Handle, offset, length int64) (io.ReadCloser, error) {
	rd, err := l.Backend.Load(ctx, h, offset, length)
	if err != nil || l.down == nil {
		return rd, err
	}
	return limitedReader{rd, ctx, l.down}, nil
}
