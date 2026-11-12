//go:build !windows

package daemon

import "context"

// runAsService is only needed on Windows; elsewhere the service manager
// runs the daemon like any program.
func runAsService(fn func(ctx context.Context) error) error { return fn(context.Background()) }
