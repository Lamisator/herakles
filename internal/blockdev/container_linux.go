package blockdev

import (
	"bytes"
	"os"
	"strings"
)

// containerFiles are read to recognise a container; tests replace them.
var containerFiles = struct{ systemd, pid1Env, docker, podman, lxc string }{
	"/run/systemd/container", "/proc/1/environ", "/.dockerenv", "/run/.containerenv", "/dev/.lxc-boot-id",
}

// Container returns the kind of container Herakles runs in ("lxc",
// "docker", "podman", …) or "" on a real or virtual machine. Containers see
// the host's disks in /sys but cannot open them unless the host passes
// them in.
func Container() string {
	if b, err := os.ReadFile(containerFiles.systemd); err == nil {
		if s := strings.TrimSpace(string(b)); s != "" {
			return s
		}
	}
	if b, err := os.ReadFile(containerFiles.pid1Env); err == nil {
		for _, kv := range bytes.Split(b, []byte{0}) {
			if v, ok := strings.CutPrefix(string(kv), "container="); ok && v != "" {
				return v
			}
		}
	}
	if v := os.Getenv("container"); v != "" {
		return v
	}
	switch {
	case exists(containerFiles.lxc):
		return "lxc"
	case exists(containerFiles.docker):
		return "docker"
	case exists(containerFiles.podman):
		return "podman"
	}
	return ""
}

func exists(p string) bool { _, err := os.Lstat(p); return err == nil }
