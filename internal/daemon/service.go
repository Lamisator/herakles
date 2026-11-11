package daemon

import (
	"context"
	"fmt"
	"runtime"

	"github.com/kardianos/service"
)

// ServiceName is the name of the installed system service.
const ServiceName = "herakles"

type program struct {
	run    func(ctx context.Context) error
	cancel context.CancelFunc
	done   chan error
}

func (p *program) Start(s service.Service) error {
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	p.done = make(chan error, 1)
	go func() { p.done <- p.run(ctx) }()
	return nil
}

func (p *program) Stop(s service.Service) error {
	if p.cancel != nil {
		p.cancel()
		return <-p.done
	}
	return nil
}

func newService(configPath string, userService bool, run func(ctx context.Context) error) (service.Service, error) {
	args := []string{"daemon", "run"}
	if configPath != "" {
		args = append(args, "--config", configPath)
	}
	cfg := &service.Config{
		Name:        ServiceName,
		DisplayName: "Herakles Backup",
		Description: "Herakles backup daemon: runs scheduled, deduplicated and encrypted backups.",
		Arguments:   args,
		Option:      service.KeyValue{},
	}
	if userService && runtime.GOOS != "windows" {
		cfg.Option["UserService"] = true
	}
	if runtime.GOOS == "linux" {
		cfg.Dependencies = []string{"After=network-online.target", "Wants=network-online.target"}
	}
	return service.New(&program{run: run}, cfg)
}

// ServiceControl performs install, uninstall, start, stop, restart or status.
func ServiceControl(action, configPath string, userService bool) (string, error) {
	s, err := newService(configPath, userService, nil)
	if err != nil {
		return "", err
	}
	if action == "status" {
		st, err := s.Status()
		if err != nil {
			return "", err
		}
		switch st {
		case service.StatusRunning:
			return "running", nil
		case service.StatusStopped:
			return "stopped", nil
		default:
			return "unknown", nil
		}
	}
	if err := service.Control(s, action); err != nil {
		return "", fmt.Errorf("%s service: %w", action, err)
	}
	return action + " ok", nil
}

// Interactive reports whether we run in a terminal rather than under a
// service manager.
func Interactive() bool { return service.Interactive() }

// RunService runs fn under the platform service manager.
func RunService(configPath string, fn func(ctx context.Context) error) error {
	s, err := newService(configPath, false, fn)
	if err != nil {
		return err
	}
	return s.Run()
}
