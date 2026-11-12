// Package blockdev discovers disks and partitions that can be backed up
// as raw images.
package blockdev

import (
	"errors"
	"sort"
	"strings"
)

// Device is a disk, a partition or a mapped volume (LVM, LUKS, RAID).
type Device struct {
	Path      string   `json:"path"`             // kernel path, e.g. /dev/sda1 or \\.\C:
	Stable    string   `json:"stable,omitempty"` // path that survives reboots, e.g. /dev/disk/by-id/…-part1
	Name      string   `json:"name"`             // sda1
	Type      string   `json:"type"`             // disk, part, lvm, crypt, raid, dm, volume
	Size      uint64   `json:"size"`             // bytes
	Model     string   `json:"model,omitempty"`
	Serial    string   `json:"serial,omitempty"`
	FSType    string   `json:"fstype,omitempty"`
	Label     string   `json:"label,omitempty"`
	UUID      string   `json:"uuid,omitempty"`
	Mounts    []string `json:"mounts,omitempty"`
	Parent    string   `json:"parent,omitempty"` // Name of the containing disk or volume
	Removable bool     `json:"removable,omitempty"`
	USB       bool     `json:"usb,omitempty"`
	ReadOnly  bool     `json:"read_only,omitempty"`
	System    bool     `json:"system,omitempty"` // holds the running system (/, /boot, /usr, swap) or contains such a device
	// Unavailable says why the device cannot be read here, e.g. a host
	// disk seen from inside a container.
	Unavailable string `json:"unavailable,omitempty"`
}

// ErrUnsupported is returned where discovery is not implemented; enter
// device names by hand there.
var ErrUnsupported = errors.New("listing disks is not supported on this system; enter the device name")

// Best returns the path to store in a job: the stable one when known.
func (d Device) Best() string {
	if d.Stable != "" {
		return d.Stable
	}
	return d.Path
}

// List returns all disks with their partitions and volumes: each disk is
// followed by its children.
func List() ([]Device, error) {
	devs, err := list()
	if err != nil {
		return nil, err
	}
	return order(devs), nil
}

var systemMounts = map[string]bool{"/": true, "/boot": true, "/boot/efi": true, "/efi": true, "/usr": true, "/var": true, "[SWAP]": true}

// order sorts devices into a tree walk (disk, its partitions, volumes on
// them…) and marks every ancestor of a system device as system too.
func order(devs []Device) []Device {
	byName := map[string]int{}
	for i, d := range devs {
		byName[d.Name] = i
	}
	for i := range devs {
		for _, m := range devs[i].Mounts {
			if systemMounts[m] {
				devs[i].System = true
			}
		}
	}
	for i := range devs {
		if !devs[i].System {
			continue
		}
		for p, seen := devs[i].Parent, 0; p != "" && seen < 8; seen++ {
			j, ok := byName[p]
			if !ok {
				break
			}
			devs[j].System = true
			p = devs[j].Parent
		}
	}
	children := map[string][]int{}
	var roots []int
	for i, d := range devs {
		if _, ok := byName[d.Parent]; d.Parent != "" && ok {
			children[d.Parent] = append(children[d.Parent], i)
		} else {
			roots = append(roots, i)
		}
	}
	less := func(ids []int) {
		sort.SliceStable(ids, func(a, b int) bool { return natLess(devs[ids[a]].Name, devs[ids[b]].Name) })
	}
	less(roots)
	out := make([]Device, 0, len(devs))
	done := map[int]bool{}
	var walk func(i int)
	walk = func(i int) {
		if done[i] {
			return
		}
		done[i] = true
		out = append(out, devs[i])
		kids := children[devs[i].Name]
		less(kids)
		for _, k := range kids {
			walk(k)
		}
	}
	for _, r := range roots {
		walk(r)
	}
	return out
}

// natLess compares names with embedded numbers numerically (sda2 < sda10).
func natLess(a, b string) bool {
	for a != "" && b != "" {
		da, db := digits(a), digits(b)
		if da > 0 && db > 0 {
			na, nb := strings.TrimLeft(a[:da], "0"), strings.TrimLeft(b[:db], "0")
			if len(na) != len(nb) {
				return len(na) < len(nb)
			}
			if na != nb {
				return na < nb
			}
			a, b = a[da:], b[db:]
			continue
		}
		if a[0] != b[0] {
			return a[0] < b[0]
		}
		a, b = a[1:], b[1:]
	}
	return len(a) < len(b)
}

func digits(s string) int {
	n := 0
	for n < len(s) && s[n] >= '0' && s[n] <= '9' {
		n++
	}
	return n
}

// Describe sums up a device in a few words: type, model, file system,
// label and mount points, e.g. `partition · ext4 "data" · /mnt/data`.
func (d Device) Describe() string {
	kind := map[string]string{"disk": "disk", "part": "partition", "lvm": "LVM volume", "crypt": "encrypted volume",
		"raid": "RAID", "dm": "mapped volume", "volume": "volume"}[d.Type]
	if kind == "" {
		kind = d.Type
	}
	if d.USB {
		kind = "USB " + kind
	} else if d.Removable && d.Type == "disk" {
		kind = "removable " + kind
	}
	parts := []string{kind}
	if d.Model != "" {
		parts = append(parts, d.Model)
	}
	fs := d.FSType
	if fs == "swap" && len(d.Mounts) == 1 && d.Mounts[0] == "[SWAP]" {
		fs = "" // "swap in use" says it all
	}
	if d.Label != "" {
		fs = strings.TrimSpace(fs + ` "` + d.Label + `"`)
	}
	if fs != "" {
		parts = append(parts, fs)
	}
	if len(d.Mounts) > 0 {
		parts = append(parts, strings.ReplaceAll(strings.Join(d.Mounts, ", "), "[SWAP]", "swap in use"))
	}
	if d.System {
		parts = append(parts, "system")
	}
	return strings.Join(parts, " · ")
}
