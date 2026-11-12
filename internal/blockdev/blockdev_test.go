package blockdev

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, p, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func link(t *testing.T, target, p string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.Symlink(target, p); err != nil {
		t.Fatal(err)
	}
}

// fakeSystem: a SATA disk with an EFI and a root partition (root on it), a
// USB stick, a LUKS volume with LVM on the USB stick, a loop device.
func fakeSystem(t *testing.T) linuxRoots {
	d := t.TempDir()
	r := linuxRoots{filepath.Join(d, "sys"), filepath.Join(d, "dev"), filepath.Join(d, "udev"), filepath.Join(d, "mountinfo"), filepath.Join(d, "swaps"), false}
	blk := filepath.Join(r.sys, "block")
	dev := func(name, num, size string) string {
		dir := filepath.Join(blk, name)
		write(t, filepath.Join(dir, "dev"), num+"\n")
		write(t, filepath.Join(dir, "size"), size+"\n")
		write(t, filepath.Join(dir, "ro"), "0\n")
		return dir
	}
	sda := dev("sda", "8:0", "1953525168")
	write(t, filepath.Join(sda, "device", "vendor"), "ATA     \n")
	write(t, filepath.Join(sda, "device", "model"), "Samsung SSD 870\n")
	write(t, filepath.Join(sda, "removable"), "0\n")
	for _, p := range [][3]string{{"sda1", "8:1", "1048576"}, {"sda2", "8:2", "1952474592"}, {"sda10", "8:10", "2048"}} {
		dir := filepath.Join(sda, p[0])
		write(t, filepath.Join(dir, "dev"), p[1])
		write(t, filepath.Join(dir, "size"), p[2])
		write(t, filepath.Join(dir, "partition"), "1")
	}
	sdb := dev("sdb", "8:16", "60437492")
	write(t, filepath.Join(sdb, "removable"), "1\n")
	write(t, filepath.Join(sdb, "sdb1", "dev"), "8:17")
	write(t, filepath.Join(sdb, "sdb1", "size"), "60435456")
	write(t, filepath.Join(sdb, "sdb1", "partition"), "1")
	dm0 := dev("dm-0", "254:0", "60400000")
	write(t, filepath.Join(dm0, "dm", "name"), "usbcrypt\n")
	write(t, filepath.Join(dm0, "dm", "uuid"), "CRYPT-LUKS2-abc\n")
	os.MkdirAll(filepath.Join(dm0, "slaves", "sdb1"), 0o755)
	dm1 := dev("dm-1", "254:1", "60000000")
	write(t, filepath.Join(dm1, "dm", "name"), "vg-data\n")
	write(t, filepath.Join(dm1, "dm", "uuid"), "LVM-xyz\n")
	os.MkdirAll(filepath.Join(dm1, "slaves", "dm-0"), 0o755)
	dev("loop0", "7:0", "1000")
	dev("sr0", "11:0", "0")

	link(t, "../../sda", filepath.Join(r.dev, "disk", "by-id", "ata-Samsung_SSD_870_S123"))
	link(t, "../../sda", filepath.Join(r.dev, "disk", "by-id", "wwn-0x5002538"))
	link(t, "../../sda2", filepath.Join(r.dev, "disk", "by-id", "ata-Samsung_SSD_870_S123-part2"))
	link(t, "../../sda2", filepath.Join(r.dev, "disk", "by-uuid", "1111-root"))
	link(t, "../../sda1", filepath.Join(r.dev, "disk", "by-partuuid", "aaaa-01"))
	link(t, "../../sdb1", filepath.Join(r.dev, "disk", "by-label", `My\x20Stick`))
	link(t, "../../dm-1", filepath.Join(r.dev, "disk", "by-uuid", "2222-data"))
	write(t, filepath.Join(r.udev, "b8:1"), "S:disk/by-partuuid/aaaa-01\nE:ID_FS_TYPE=vfat\nE:ID_FS_LABEL=EFI\n")
	write(t, filepath.Join(r.udev, "b8:16"), "E:ID_BUS=usb\nE:ID_MODEL=Ultra_Fit\n")
	write(t, filepath.Join(r.udev, "b254:1"), "E:ID_FS_TYPE=ext4\n")
	write(t, r.mountinfo, strings.Join([]string{
		"22 1 8:2 / / rw,relatime shared:1 - ext4 /dev/sda2 rw",
		"23 22 8:1 / /boot/efi rw - vfat /dev/sda1 rw",
		"40 22 0:45 / /mnt/my\\040data rw - btrfs " + filepath.Join(r.dev, "disk", "by-uuid", "2222-data") + " rw",
		"41 22 0:5 / /proc rw - proc proc rw",
	}, "\n")+"\n")
	write(t, r.swaps, "Filename\tType\tSize\tUsed\tPriority\n")
	return r
}

func TestListLinux(t *testing.T) {
	r := fakeSystem(t)
	devs, err := listLinux(r)
	if err != nil {
		t.Fatal(err)
	}
	devs = order(devs)
	var names []string
	by := map[string]Device{}
	for _, d := range devs {
		names = append(names, d.Name)
		by[d.Name] = d
	}
	// Children follow their parent: the LUKS volume on the stick's partition, LVM inside it.
	if got := strings.Join(names, " "); got != "sda sda1 sda2 sda10 sdb sdb1 dm-0 dm-1" {
		t.Fatalf("order: %s", got)
	}
	sda := by["sda"]
	if sda.Model != "ATA Samsung SSD 870" || sda.Size != 1953525168*512 || !sda.System || sda.Removable ||
		sda.Stable != filepath.Join(r.dev, "disk/by-id/ata-Samsung_SSD_870_S123") {
		t.Fatalf("sda: %+v", sda)
	}
	if p := by["sda2"]; p.FSType != "ext4" || len(p.Mounts) != 1 || p.Mounts[0] != "/" || !p.System || p.UUID != "1111-root" ||
		p.Stable != filepath.Join(r.dev, "disk/by-id/ata-Samsung_SSD_870_S123-part2") || p.Parent != "sda" {
		t.Fatalf("sda2: %+v", p)
	}
	if p := by["sda1"]; p.FSType != "vfat" || p.Label != "EFI" || !p.System || p.Stable != filepath.Join(r.dev, "disk/by-partuuid/aaaa-01") {
		t.Fatalf("sda1: %+v", p)
	}
	if p := by["sdb"]; !p.USB || !p.Removable || p.System || p.Model != "Ultra Fit" {
		t.Fatalf("sdb: %+v", p)
	}
	if p := by["sdb1"]; p.Label != "My Stick" || !p.USB {
		t.Fatalf("sdb1: %+v", p)
	}
	if p := by["dm-0"]; p.Type != "crypt" || p.Path != filepath.Join(r.dev, "mapper/usbcrypt") || p.Parent != "sdb1" {
		t.Fatalf("dm-0: %+v", p)
	}
	if p := by["dm-1"]; p.Type != "lvm" || p.FSType != "ext4" || len(p.Mounts) != 1 || p.Mounts[0] != "/mnt/my data" || p.Best() != filepath.Join(r.dev, "mapper/vg-data") {
		t.Fatalf("dm-1: %+v", p)
	}
	if _, ok := by["loop0"]; ok {
		t.Fatal("loop device listed")
	}
	if _, ok := by["sr0"]; ok {
		t.Fatal("empty optical drive listed")
	}
}

func TestNatLess(t *testing.T) {
	if !natLess("sda2", "sda10") || natLess("sda10", "sda2") || !natLess("nvme0n1p9", "nvme0n1p10") || !natLess("sda", "sdb") {
		t.Fatal("natural order")
	}
}
