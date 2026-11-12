//go:build linux

package sysutil

import (
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestProcessAliveZombie(t *testing.T) {
	if !ProcessAlive(os.Getpid()) {
		t.Fatal("this process runs")
	}
	cmd := exec.Command("true")
	if err := cmd.Start(); err != nil {
		t.Skip(err)
	}
	pid := cmd.Process.Pid
	time.Sleep(300 * time.Millisecond) // exited, not collected: a zombie
	if ProcessAlive(pid) {
		t.Fatal("a zombie counts as running")
	}
	_ = cmd.Wait()
}
