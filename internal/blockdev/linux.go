package blockdev

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// sysfs locations; tests point them into a temporary tree.
type linuxRoots struct {
	sys       string // /sys
	dev       string // /dev
	udev      string // /run/udev/data
	mountinfo string // /proc/self/mountinfo
	swaps     string // /proc/swaps
}

var defaultRoots = linuxRoots{"/sys", "/dev", "/run/udev/data", "/proc/self/mountinfo", "/proc/swaps"}

func listLinux(r linuxRoots) ([]Device, error) {
	ents, err := os.ReadDir(filepath.Join(r.sys, "block"))
	if err != nil {
		return nil, err
	}
	links := r.diskLinks()
	mounts := r.mounts()
	var out []Device
	add := func(d Device, sysDir string) Device {
		num := readTrim(filepath.Join(sysDir, "dev"))
		d.Size = readUint(filepath.Join(sysDir, "size")) * 512
		d.ReadOnly = readTrim(filepath.Join(sysDir, "ro")) == "1"
		r.udevInfo(num, &d)
		for _, l := range links[d.Name] {
			switch {
			case strings.HasPrefix(l, "disk/by-label/") && d.Label == "":
				d.Label = unescape(strings.TrimPrefix(l, "disk/by-label/"))
			case strings.HasPrefix(l, "disk/by-uuid/") && d.UUID == "":
				d.UUID = strings.TrimPrefix(l, "disk/by-uuid/")
			}
		}
		if d.Stable == "" {
			d.Stable = stableLink(links[d.Name], r.dev)
		}
		ms := append(mounts[num], mounts[d.Name]...)
		for _, m := range ms {
			if m.fstype != "" && d.FSType == "" {
				d.FSType = m.fstype
			}
			d.Mounts = appendUniq(d.Mounts, m.dir)
		}
		out = append(out, d)
		return d
	}
	for _, e := range ents {
		name := e.Name()
		dir := filepath.Join(r.sys, "block", name)
		if skipDevice(name) || readUint(filepath.Join(dir, "size")) == 0 {
			continue
		}
		d := Device{Name: name, Path: filepath.Join(r.dev, name), Type: "disk"}
		d.Removable = readTrim(filepath.Join(dir, "removable")) == "1"
		if target, err := filepath.EvalSymlinks(dir); err == nil && strings.Contains(target, "/usb") {
			d.USB, d.Removable = true, true
		}
		d.Model = strings.TrimSpace(readTrim(filepath.Join(dir, "device", "vendor")) + " " + readTrim(filepath.Join(dir, "device", "model")))
		if d.Model == "" {
			d.Model = readTrim(filepath.Join(dir, "device", "name")) // MMC cards
		}
		d.Serial = readTrim(filepath.Join(dir, "device", "serial"))
		switch {
		case strings.HasPrefix(name, "dm-"):
			d.Type = "dm"
			uuid := readTrim(filepath.Join(dir, "dm", "uuid"))
			switch {
			case strings.HasPrefix(uuid, "LVM-"):
				d.Type = "lvm"
			case strings.HasPrefix(uuid, "CRYPT-"):
				d.Type = "crypt"
			}
			if n := readTrim(filepath.Join(dir, "dm", "name")); n != "" {
				d.Path = filepath.Join(r.dev, "mapper", n)
				d.Stable = d.Path
				d.Model = ""
			}
			d.Parent = firstEntry(filepath.Join(dir, "slaves"))
		case strings.HasPrefix(name, "md"):
			d.Type, d.Model = "raid", "RAID "+readTrim(filepath.Join(dir, "md", "level"))
			d.Parent = firstEntry(filepath.Join(dir, "slaves"))
		}
		d = add(d, dir)
		parts, _ := os.ReadDir(dir)
		for _, p := range parts {
			pdir := filepath.Join(dir, p.Name())
			if _, err := os.Stat(filepath.Join(pdir, "partition")); err != nil {
				continue
			}
			add(Device{Name: p.Name(), Path: filepath.Join(r.dev, p.Name()), Type: "part", Parent: name,
				Removable: d.Removable, USB: d.USB}, pdir)
		}
	}
	return out, nil
}

// skipDevice leaves out devices that are no disks: RAM disks, compressed
// swap, loop devices (images of files), optical drives and floppies.
func skipDevice(name string) bool {
	for _, p := range []string{"loop", "ram", "zram", "sr", "fd", "nbd"} {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// diskLinks maps device names to their /dev/disk/by-* symlinks (relative
// to dev, e.g. "disk/by-id/ata-…").
func (r linuxRoots) diskLinks() map[string][]string {
	out := map[string][]string{}
	dirs, _ := filepath.Glob(filepath.Join(r.dev, "disk", "by-*"))
	for _, d := range dirs {
		ents, _ := os.ReadDir(d)
		for _, e := range ents {
			target, err := os.Readlink(filepath.Join(d, e.Name()))
			if err != nil {
				continue
			}
			name := filepath.Base(target)
			out[name] = append(out[name], filepath.Join("disk", filepath.Base(d), e.Name()))
		}
	}
	return out
}

// stableLink picks the best persistent name: by-id (model + serial, not the
// opaque wwn/eui forms), then by-partuuid, by-uuid, by-path.
func stableLink(links []string, dev string) string {
	rank := func(l string) int {
		base := filepath.Base(l)
		switch {
		case strings.HasPrefix(l, "disk/by-id/") && !strings.HasPrefix(base, "wwn-") && !strings.HasPrefix(base, "nvme-eui.") && !strings.HasPrefix(base, "lvm-pv-"):
			return 1
		case strings.HasPrefix(l, "disk/by-id/"):
			return 2
		case strings.HasPrefix(l, "disk/by-partuuid/"):
			return 3
		case strings.HasPrefix(l, "disk/by-uuid/"):
			return 4
		case strings.HasPrefix(l, "disk/by-path/"):
			return 5
		}
		return 0
	}
	best, br := "", 99
	for _, l := range links {
		if k := rank(l); k > 0 && (k < br || k == br && l < best) {
			best, br = l, k
		}
	}
	if best == "" {
		return ""
	}
	return filepath.Join(dev, best)
}

// udevInfo reads what udev found out about the device (no root needed).
func (r linuxRoots) udevInfo(num string, d *Device) {
	if num == "" {
		return
	}
	f, err := os.Open(filepath.Join(r.udev, "b"+num))
	if err != nil {
		return
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		k, v, ok := strings.Cut(strings.TrimPrefix(s.Text(), "E:"), "=")
		if !ok || !strings.HasPrefix(s.Text(), "E:") {
			continue
		}
		switch k {
		case "ID_FS_TYPE":
			d.FSType = v
		case "ID_FS_LABEL":
			d.Label = v
		case "ID_FS_UUID":
			d.UUID = v
		case "ID_MODEL":
			if d.Model == "" && d.Type == "disk" {
				d.Model = strings.ReplaceAll(v, "_", " ")
			}
		case "ID_SERIAL_SHORT":
			if d.Serial == "" && d.Type == "disk" {
				d.Serial = v
			}
		case "ID_BUS":
			if v == "usb" {
				d.USB, d.Removable = true, true
			}
		}
	}
}

type mount struct{ dir, fstype string }

// mounts maps "major:minor" and device names to mount points; swap
// devices get "[SWAP]".
func (r linuxRoots) mounts() map[string][]mount {
	out := map[string][]mount{}
	if f, err := os.Open(r.mountinfo); err == nil {
		s := bufio.NewScanner(f)
		s.Buffer(make([]byte, 64<<10), 1<<20)
		for s.Scan() {
			pre, post, ok := strings.Cut(s.Text(), " - ")
			fs, pf := strings.Fields(pre), strings.Fields(post)
			if !ok || len(fs) < 5 || len(pf) < 2 {
				continue
			}
			m := mount{unescape(fs[4]), pf[0]}
			out[fs[2]] = append(out[fs[2]], m)
			if strings.HasPrefix(pf[1], "/") {
				// btrfs and others report a virtual major:minor; the source names the device.
				name := devName(pf[1])
				out[name] = append(out[name], m)
			}
		}
		f.Close()
	}
	if f, err := os.Open(r.swaps); err == nil {
		s := bufio.NewScanner(f)
		for s.Scan() {
			fs := strings.Fields(s.Text())
			if len(fs) > 1 && strings.HasPrefix(fs[0], "/dev/") {
				name := devName(fs[0])
				out[name] = append(out[name], mount{"[SWAP]", "swap"})
			}
		}
		f.Close()
	}
	return out
}

// devName resolves a device path such as /dev/mapper/vg-root or
// /dev/disk/by-uuid/… to the kernel name (dm-0).
func devName(p string) string {
	if t, err := filepath.EvalSymlinks(p); err == nil {
		return filepath.Base(t)
	}
	if t, err := os.Readlink(p); err == nil {
		return filepath.Base(t)
	}
	return filepath.Base(p)
}

// unescape decodes \040-style octal escapes used in mountinfo and \x20
// in udev link names.
func unescape(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) && s[i+1] == 'x' {
			if n, err := strconv.ParseUint(s[i+2:i+4], 16, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		if s[i] == '\\' && i+3 < len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func readTrim(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func readUint(p string) uint64 {
	n, _ := strconv.ParseUint(readTrim(p), 10, 64)
	return n
}

func firstEntry(dir string) string {
	ents, err := os.ReadDir(dir)
	if err != nil || len(ents) == 0 {
		return ""
	}
	return ents[0].Name()
}

func appendUniq(list []string, s string) []string {
	for _, x := range list {
		if x == s {
			return list
		}
	}
	return append(list, s)
}
