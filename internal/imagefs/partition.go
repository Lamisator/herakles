package imagefs

import (
	"encoding/binary"
	"fmt"
	"io"
	"strings"
	"unicode/utf16"
)

type part struct {
	index       int
	start, size int64
	name, kind  string
}

// partitions reads a GPT (512 or 4096 byte sectors) or an MBR with its
// logical partitions.
func partitions(r io.ReaderAt, size int64) ([]part, string, error) {
	for _, sector := range []int64{512, 4096} {
		if hdr := readAt(r, sector, 512); has(hdr, 0, "EFI PART") {
			p, err := gpt(r, size, sector, hdr)
			return p, "gpt", err
		}
	}
	mbr := readAt(r, 0, 512)
	if len(mbr) < 512 || mbr[510] != 0x55 || mbr[511] != 0xAA {
		return nil, "", nil
	}
	var out []part
	for i := 0; i < 4; i++ {
		e := mbr[446+16*i:]
		status, typ := e[0], e[4]
		start := int64(binary.LittleEndian.Uint32(e[8:])) * 512
		n := int64(binary.LittleEndian.Uint32(e[12:])) * 512
		if typ == 0 || n == 0 {
			continue
		}
		if status != 0 && status != 0x80 || start >= size {
			return nil, "", nil // not a partition table (e.g. a boot sector)
		}
		if typ == 0xEE {
			return nil, "", fmt.Errorf("protective MBR without a readable GPT")
		}
		if typ == 0x05 || typ == 0x0F || typ == 0x85 {
			out = append(out, logical(r, size, start)...)
			continue
		}
		out = append(out, part{index: i + 1, start: start, size: min(n, size-start), kind: mbrKinds[typ]})
	}
	return out, "mbr", nil
}

// logical follows the chain of extended boot records (partitions 5, 6, ...).
func logical(r io.ReaderAt, size, ext int64) []part {
	var out []part
	ebr := ext
	for i := 0; i < 128; i++ {
		b := readAt(r, ebr, 512)
		if len(b) < 512 || b[510] != 0x55 || b[511] != 0xAA {
			break
		}
		e1, e2 := b[446:], b[462:]
		if start, n := int64(binary.LittleEndian.Uint32(e1[8:]))*512, int64(binary.LittleEndian.Uint32(e1[12:]))*512; e1[4] != 0 && n > 0 && ebr+start < size {
			out = append(out, part{index: 5 + i, start: ebr + start, size: min(n, size-ebr-start), kind: mbrKinds[e1[4]]})
		}
		next := int64(binary.LittleEndian.Uint32(e2[8:])) * 512
		if e2[4] == 0 || next == 0 || ext+next <= ebr {
			break
		}
		ebr = ext + next
	}
	return out
}

func gpt(r io.ReaderAt, size, sector int64, hdr []byte) ([]part, error) {
	lba := int64(binary.LittleEndian.Uint64(hdr[72:]))
	count := int(binary.LittleEndian.Uint32(hdr[80:]))
	esize := int(binary.LittleEndian.Uint32(hdr[84:]))
	if esize < 128 || esize > 4096 || count <= 0 || count > 4096 {
		return nil, fmt.Errorf("damaged GPT header")
	}
	table := readAt(r, lba*sector, count*esize)
	var out []part
	for i := 0; i+1 <= count && (i+1)*esize <= len(table); i++ {
		e := table[i*esize : (i+1)*esize]
		typ := guid(e[0:16])
		if typ == "00000000-0000-0000-0000-000000000000" {
			continue
		}
		first := int64(binary.LittleEndian.Uint64(e[32:]))
		last := int64(binary.LittleEndian.Uint64(e[40:]))
		if last < first || first*sector >= size {
			continue
		}
		u := make([]uint16, 36)
		for j := range u {
			u[j] = binary.LittleEndian.Uint16(e[56+2*j:])
		}
		name := strings.TrimRight(string(utf16.Decode(u)), "\x00")
		kind := gptKinds[typ]
		out = append(out, part{index: i + 1, start: first * sector, size: min((last-first+1)*sector, size-first*sector), name: name, kind: kind})
	}
	return out, nil
}

// guid formats a mixed-endian GPT GUID.
func guid(b []byte) string {
	return fmt.Sprintf("%08X-%04X-%04X-%X-%X", binary.LittleEndian.Uint32(b[0:]), binary.LittleEndian.Uint16(b[4:]),
		binary.LittleEndian.Uint16(b[6:]), b[8:10], b[10:16])
}

var gptKinds = map[string]string{
	"C12A7328-F81F-11D2-BA4B-00A0C93EC93B": "EFI system",
	"21686148-6449-6E6F-744E-656564454649": "BIOS boot",
	"E3C9E316-0B5C-4DB8-817D-F92DF00215AE": "Microsoft reserved",
	"EBD0A0A2-B9E5-4433-87C0-68B6B72699C7": "Basic data",
	"DE94BBA4-06D1-4D40-A16A-BFD50179D6AC": "Windows recovery",
	"0FC63DAF-8483-4772-8E79-3D69D8477DE4": "Linux file system",
	"4F68BCE3-E8CD-4DB1-96E7-FBCAF984B709": "Linux root (x86-64)",
	"B921B045-1DF0-41C3-AF44-4C6F280D3FAE": "Linux root (ARM64)",
	"933AC7E1-2EB4-4F13-B844-0E14E2AEF915": "Linux home",
	"BC13C2FF-59E6-4262-A352-B275FD6F7172": "Linux boot",
	"0657FD6D-A4AB-43C4-84E5-0933C84B4F4F": "Linux swap",
	"E6D6D379-F507-44C2-A23C-238F2A3DF928": "Linux LVM",
	"A19D880F-05FC-4D3B-A006-743F0F84911E": "Linux RAID",
	"CA7D7CCB-63ED-4C53-861C-1742536059CC": "Linux LUKS",
	"7C3457EF-0000-11AA-AA11-00306543ECAC": "Apple APFS",
	"48465300-0000-11AA-AA11-00306543ECAC": "Apple HFS+",
	"6A898CC3-1DD2-11B2-99A6-080020736631": "ZFS",
}

var mbrKinds = map[byte]string{
	0x01: "FAT12", 0x04: "FAT16", 0x06: "FAT16", 0x07: "NTFS / exFAT", 0x0B: "FAT32", 0x0C: "FAT32",
	0x0E: "FAT16", 0x27: "Windows recovery", 0x82: "Linux swap", 0x83: "Linux", 0x8E: "Linux LVM",
	0xA5: "FreeBSD", 0xEF: "EFI system", 0xFD: "Linux RAID",
}
