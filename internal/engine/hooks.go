package engine

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/lamisator/herakles/internal/config"
)

func runHooks(ctx context.Context, h config.Hooks, cmds []string, env []string, log *slog.Logger) error {
	timeout := 30 * time.Minute
	if h.Timeout != "" {
		if d, err := time.ParseDuration(h.Timeout); err == nil {
			timeout = d
		}
	}
	for _, c := range cmds {
		if strings.TrimSpace(c) == "" {
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, timeout)
		cmd := config.ShellCommand(c)
		cmd.Env = append(cmd.Environ(), env...)
		var out bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &out
		err := cmd.Start()
		if err == nil {
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case err = <-done:
			case <-cctx.Done():
				_ = cmd.Process.Kill()
				<-done
				err = fmt.Errorf("timed out after %s", timeout)
			}
		}
		cancel()
		log.Info("hook executed", "command", c, "output", strings.TrimSpace(out.String()), "err", err)
		if err != nil {
			msg := strings.TrimSpace(out.String())
			if len(msg) > 500 {
				msg = msg[:500] + "..."
			}
			if msg != "" {
				return fmt.Errorf("%q: %w: %s", c, err, msg)
			}
			return fmt.Errorf("%q: %w", c, err)
		}
	}
	return nil
}
