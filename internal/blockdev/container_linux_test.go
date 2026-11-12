package blockdev

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestContainer(t *testing.T) {
	d := t.TempDir()
	old := containerFiles
	defer func() { containerFiles = old }()
	none := filepath.Join(d, "none")
	containerFiles.systemd, containerFiles.pid1Env, containerFiles.docker, containerFiles.podman, containerFiles.lxc = none, none, none, none, none
	t.Setenv("container", "")
	if c := Container(); c != "" {
		t.Fatalf("no container: %q", c)
	}
	containerFiles.pid1Env = filepath.Join(d, "environ")
	write(t, containerFiles.pid1Env, "PATH=/bin\x00container=lxc\x00TERM=linux\x00")
	if c := Container(); c != "lxc" {
		t.Fatalf("environ: %q", c)
	}
	containerFiles.systemd = filepath.Join(d, "container")
	write(t, containerFiles.systemd, "lxc-libvirt\n")
	if c := Container(); c != "lxc-libvirt" {
		t.Fatalf("systemd: %q", c)
	}
}

// Host disks listed in /sys whose nodes are missing are marked.
func TestUnavailable(t *testing.T) {
	d := t.TempDir()
	if r := unavailable(filepath.Join(d, "sda"), "lxc"); !strings.Contains(r, "not passed into the container (lxc)") {
		t.Fatal(r)
	}
	write(t, filepath.Join(d, "img"), "x")
	if r := unavailable(filepath.Join(d, "img"), "lxc"); r != "" {
		t.Fatal(r)
	}
}
