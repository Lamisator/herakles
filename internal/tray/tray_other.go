//go:build !(((linux || freebsd || openbsd || netbsd) && !android) || windows || (darwin && cgo))

package tray

import "context"

func run(ctx context.Context, opts Options) error { return ErrUnsupported }
