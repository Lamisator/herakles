package daemon

import (
	"context"
	"fmt"

	"golang.org/x/sys/windows/svc"
)

// winService runs fn for the service control manager.
type winService struct {
	run func(ctx context.Context) error
}

func (w winService) Execute(_ []string, req <-chan svc.ChangeRequest, st chan<- svc.Status) (bool, uint32) {
	st <- svc.Status{State: svc.StartPending}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- w.run(ctx) }()
	st <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for {
		select {
		case err := <-done:
			if err != nil {
				return true, 1
			}
			return false, 0
		case c := <-req:
			switch c.Cmd {
			case svc.Interrogate:
				st <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				st <- svc.Status{State: svc.StopPending}
				cancel()
				<-done
				return false, 0
			}
		}
	}
}

// runAsService runs fn as the Windows service without asking Windows
// whether this is one (the check fails under some environments, e.g. Wine).
func runAsService(fn func(ctx context.Context) error) error {
	if err := svc.Run(ServiceName, winService{run: fn}); err != nil {
		return fmt.Errorf("not started by the service control manager: %w", err)
	}
	return nil
}
