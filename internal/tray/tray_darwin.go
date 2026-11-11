//go:build darwin && cgo

package tray

import (
	"context"
	"time"
)

func iconBytes(s State) []byte { return PNG(64, s) }

func available() error { return nil }

func hostReady(context.Context, time.Duration) bool { return true }

const noHostWarning = ""
