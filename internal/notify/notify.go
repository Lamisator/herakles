// Package notify delivers job results via webhooks, e-mail or commands.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/smtp"
	"strconv"
	"strings"
	"time"

	"github.com/lamisator/herakles/internal/config"
)

// Event is the payload sent to notification targets.
type Event struct {
	Job        string    `json:"job"`
	Repository string    `json:"repository"`
	Status     string    `json:"status"` // success, warning, failure
	Type       string    `json:"type"`
	Snapshot   string    `json:"snapshot,omitempty"`
	Error      string    `json:"error,omitempty"`
	Start      time.Time `json:"start"`
	End        time.Time `json:"end"`
	Files      int64     `json:"files"`
	Bytes      int64     `json:"bytes"`
	BytesAdded int64     `json:"bytes_added"`
	Errors     int64     `json:"errors"`
	Hostname   string    `json:"hostname"`
}

func wants(n *config.Notification, ev Event) bool {
	on := n.On
	if len(on) == 0 {
		on = []string{"failure", "warning"}
	}
	ok := false
	for _, o := range on {
		if o == ev.Status {
			ok = true
		}
	}
	if !ok {
		return false
	}
	if len(n.Jobs) == 0 {
		return true
	}
	for _, j := range n.Jobs {
		if j == ev.Job {
			return true
		}
	}
	return false
}

// Send delivers ev to every matching target and returns combined errors.
func Send(ctx context.Context, targets []*config.Notification, ev Event) error {
	var errs []error
	for _, n := range targets {
		if !wants(n, ev) {
			continue
		}
		var err error
		switch n.Type {
		case "webhook":
			err = webhook(ctx, n.URL, ev)
		case "email":
			err = email(n, ev)
		case "command":
			err = command(ctx, n.Command, ev)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%s notification: %w", n.Type, err))
		}
	}
	return errors.Join(errs...)
}

// Subject renders a one-line summary.
func Subject(ev Event) string {
	return fmt.Sprintf("[herakles] %s: job %s on %s", strings.ToUpper(ev.Status), ev.Job, ev.Hostname)
}

// Body renders a human readable message.
func Body(ev Event) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Job:        %s\nRepository: %s\nStatus:     %s\nType:       %s\n", ev.Job, ev.Repository, ev.Status, ev.Type)
	fmt.Fprintf(&b, "Started:    %s\nFinished:   %s\n", ev.Start.Format(time.RFC1123), ev.End.Format(time.RFC1123))
	if ev.Snapshot != "" {
		fmt.Fprintf(&b, "Snapshot:   %s\nFiles:      %d (%d bytes, %d new bytes)\n", ev.Snapshot, ev.Files, ev.Bytes, ev.BytesAdded)
	}
	if ev.Errors > 0 {
		fmt.Fprintf(&b, "File errors: %d\n", ev.Errors)
	}
	if ev.Error != "" {
		fmt.Fprintf(&b, "\nError: %s\n", ev.Error)
	}
	return b.String()
}

func webhook(ctx context.Context, url string, ev Event) error {
	payload := map[string]any{"text": Subject(ev), "event": ev}
	body, _ := json.Marshal(payload)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("webhook returned %s", resp.Status)
	}
	return nil
}

func email(n *config.Notification, ev Event) error {
	port := n.SMTPPort
	if port == 0 {
		port = 587
	}
	addr := n.SMTPHost + ":" + strconv.Itoa(port)
	var auth smtp.Auth
	if n.SMTPUser != "" {
		auth = smtp.PlainAuth("", n.SMTPUser, n.SMTPPassword, n.SMTPHost)
	}
	msg := "From: " + n.From + "\r\nTo: " + strings.Join(n.To, ", ") + "\r\nSubject: " + Subject(ev) +
		"\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n" + strings.ReplaceAll(Body(ev), "\n", "\r\n")
	return smtp.SendMail(addr, auth, n.From, n.To, []byte(msg))
}

func command(ctx context.Context, cmdline string, ev Event) error {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	cmd := config.ShellCommand(cmdline)
	body, _ := json.Marshal(ev)
	cmd.Stdin = bytes.NewReader(body)
	cmd.Env = append(cmd.Environ(), "HERAKLES_JOB="+ev.Job, "HERAKLES_STATUS="+ev.Status, "HERAKLES_SUBJECT="+Subject(ev))
	done := make(chan error, 1)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		return ctx.Err()
	}
}
