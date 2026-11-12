// Package imagefs reads the files inside disk and partition images: it
// finds the partitions (MBR, GPT) and opens their file systems read-only —
// ext2/3/4, FAT12/16/32 and NTFS. Other file systems and containers (XFS,
// btrfs, exFAT, LUKS, LVM, ...) are recognised and reported as not
// browsable. Nothing is written; journals are not replayed.
package imagefs

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"strings"
	"time"
)

// Entry is a file, directory or link inside a file system.
type Entry struct {
	Name    string      `json:"name"`
	Type    string      `json:"type"` // file, dir, symlink, other
	Size    int64       `json:"size"`
	Mode    fs.FileMode `json:"mode"`
	ModTime time.Time   `json:"mtime"`
	Link    string      `json:"link,omitempty"`
}

// IsDir reports whether e is a directory.
func (e Entry) IsDir() bool { return e.Type == "dir" }

// FS is a read-only file system. Paths are slash-separated and relative to
// its root ("" or "." is the root).
type FS interface {
	ReadDir(p string) ([]Entry, error)
	Stat(p string) (Entry, error)
	// Open returns the content of a regular file and its size.
	Open(p string) (io.ReaderAt, int64, error)
}

// Volume is one file system of an image: the whole image, or a partition.
type Volume struct {
	// Name is the path element for the volume ("part1", ...); empty when
	// the image itself holds the file system.
	Name   string `json:"name"`
	Index  int    `json:"index,omitempty"`
	Start  int64  `json:"start"`
	Size   int64  `json:"size"`
	Type   string `json:"type"`            // ext4, fat32, ntfs, xfs, luks, ... or "" (unknown)
	Label  string `json:"label,omitempty"` // file system label
	Part   string `json:"part,omitempty"`  // partition name or kind (GPT name, "EFI system", ...)
	Note   string `json:"note,omitempty"`  // why it cannot be browsed, or a caveat
	FS     FS     `json:"-"`
	Reader io.ReaderAt
}

// Browsable reports whether the volume's files can be read.
func (v *Volume) Browsable() bool { return v.FS != nil }

// ErrNotFound is returned for paths that do not exist.
var ErrNotFound = fs.ErrNotExist

// Open finds the file systems in an image of the given size.
func Open(r io.ReaderAt, size int64) ([]*Volume, error) {
	if size < 512 {
		return nil, errors.New("the image is too small to hold a file system")
	}
	if v := openVolume(r, 0, size); v.Type != "" {
		return []*Volume{v}, nil
	}
	parts, kind, err := partitions(r, size)
	if err != nil {
		return nil, err
	}
	if len(parts) == 0 {
		v := openVolume(r, 0, size)
		v.Note = "no partition table and no known file system"
		return []*Volume{v}, nil
	}
	out := make([]*Volume, 0, len(parts))
	for _, p := range parts {
		v := openVolume(r, p.start, p.size)
		v.Name, v.Index, v.Part = fmt.Sprintf("part%d", p.index), p.index, p.name
		if v.Type == "" && v.Note == "" {
			v.Note = "unknown or empty file system"
			if p.kind != "" {
				v.Note = p.kind + ": no file system that can be browsed"
			}
		}
		if v.Part == "" {
			v.Part = p.kind
		}
		out = append(out, v)
	}
	_ = kind
	return out, nil
}

// openVolume detects and opens the file system in [start, start+size).
func openVolume(r io.ReaderAt, start, size int64) (v *Volume) {
	sr := io.NewSectionReader(r, start, size)
	v = &Volume{Start: start, Size: size, Reader: sr}
	typ, label := detect(sr, size)
	v.Type, v.Label = typ, label
	defer func() {
		if p := recover(); p != nil {
			v.FS, v.Note = nil, fmt.Sprintf("cannot read the %s file system: %v", typ, p)
		}
	}()
	var err error
	switch typ {
	case "":
		return v
	case "ext2", "ext3", "ext4":
		var e *extFS
		if e, err = openExt(sr, size); err == nil {
			v.FS = e
			if e.recover {
				v.Note = "the journal was not replayed: the newest changes before the backup may be missing"
			}
		}
	case "fat12", "fat16", "fat32":
		v.FS, err = openFAT(sr, size, typ)
	case "ntfs":
		v.FS, err = openNTFS(sr, size)
	default:
		v.Note = notes[typ]
		if v.Note == "" {
			v.Note = typ + " cannot be browsed (yet); restore the image to read it"
		}
		return v
	}
	if err != nil {
		v.FS, v.Note = nil, fmt.Sprintf("cannot read the %s file system: %v", typ, err)
	}
	return v
}

var notes = map[string]string{
	"luks":      "encrypted (LUKS): restore the image and unlock it to read the files",
	"bitlocker": "encrypted (BitLocker): restore the image and unlock it to read the files",
	"lvm":       "LVM physical volume: restore the image to read its logical volumes",
	"swap":      "swap space: holds no files",
	"raid":      "software RAID member: restore the image to read it",
	"zfs":       "ZFS cannot be browsed; restore the image to read it",
}

func readAt(r io.ReaderAt, off int64, n int) []byte {
	b := make([]byte, n)
	m, _ := r.ReadAt(b, off)
	return b[:m]
}

func has(b []byte, off int, magic string) bool {
	return len(b) >= off+len(magic) && string(b[off:off+len(magic)]) == magic
}

// detect recognises a file system or container by its signature.
func detect(r io.ReaderAt, size int64) (typ, label string) {
	head := readAt(r, 0, 4096)
	if len(head) < 512 {
		return "", ""
	}
	sb := readAt(r, 1024, 1024)
	switch {
	case has(head, 0, "LUKS\xba\xbe"):
		return "luks", ""
	case has(head, 3, "-FVE-FS-"):
		return "bitlocker", ""
	case has(head, 3, "NTFS    "):
		return "ntfs", ""
	case has(head, 3, "EXFAT   "):
		return "exfat", ""
	case has(head, 3, "ReFS"):
		return "refs", ""
	case has(head, 0, "XFSB"):
		return "xfs", strings.TrimRight(string(head[108:120]), "\x00 ")
	case has(head, 0, "hsqs"):
		return "squashfs", ""
	case has(head, 512, "LABELONE") || has(head, 0, "LABELONE"):
		return "lvm", ""
	case len(sb) >= 64 && binary.LittleEndian.Uint16(sb[56:]) == 0xEF53:
		return extKind(sb), strings.TrimRight(string(sb[120:136]), "\x00 ")
	case len(sb) >= 4 && binary.LittleEndian.Uint32(sb[0:]) == 0xF2F52010:
		return "f2fs", ""
	case has(sb, 0, "H+") || has(sb, 0, "HX"):
		return "hfsplus", ""
	case has(head, 32, "NXSB"):
		return "apfs", ""
	case has(head, 4086, "SWAPSPACE2") || has(head, 4086, "SWAP-SPACE"):
		return "swap", ""
	}
	if fat, label := fatKind(head); fat != "" {
		return fat, label
	}
	if size > 65536+4096 {
		if b := readAt(r, 65536, 4096); has(b, 64, "_BHRfS_M") {
			return "btrfs", strings.TrimRight(string(b[0x12b:0x12b+256]), "\x00 ")
		}
	}
	if size > 32768+8 {
		if b := readAt(r, 32768, 8); has(b, 1, "CD001") {
			return "iso9660", ""
		}
	}
	if size > 4096+8 {
		if b := readAt(r, 4096, 8); bytes.Equal(b[:4], []byte{0xfc, 0x4e, 0x2b, 0xa9}) {
			return "raid", ""
		}
	}
	return "", ""
}

// extKind tells ext2, ext3 and ext4 apart by their features.
func extKind(sb []byte) string {
	compat := binary.LittleEndian.Uint32(sb[92:])
	incompat := binary.LittleEndian.Uint32(sb[96:])
	switch {
	case incompat&(extIncompatExtents|extIncompat64bit|extIncompatFlexBG) != 0:
		return "ext4"
	case compat&0x4 != 0: // has_journal
		return "ext3"
	}
	return "ext2"
}

// fatKind recognises a FAT boot sector and its FAT width.
func fatKind(b []byte) (string, string) {
	if len(b) < 512 || b[510] != 0x55 || b[511] != 0xAA {
		return "", ""
	}
	bps := binary.LittleEndian.Uint16(b[11:])
	spc := b[13]
	reserved := binary.LittleEndian.Uint16(b[14:])
	fats := b[16]
	if (bps != 512 && bps != 1024 && bps != 2048 && bps != 4096) || spc == 0 || spc&(spc-1) != 0 || reserved == 0 || fats == 0 || fats > 4 {
		return "", ""
	}
	rootEntries := uint32(binary.LittleEndian.Uint16(b[17:]))
	total := uint32(binary.LittleEndian.Uint16(b[19:]))
	if total == 0 {
		total = binary.LittleEndian.Uint32(b[32:])
	}
	fatSize := uint32(binary.LittleEndian.Uint16(b[22:]))
	label := ""
	if fatSize == 0 {
		fatSize = binary.LittleEndian.Uint32(b[36:])
		if !has(b, 82, "FAT32") {
			return "", ""
		}
		label = strings.TrimRight(string(b[71:82]), " \x00")
	} else {
		if !has(b, 54, "FAT") {
			return "", ""
		}
		label = strings.TrimRight(string(b[43:54]), " \x00")
	}
	if label == "NO NAME" {
		label = ""
	}
	rootSectors := (rootEntries*32 + uint32(bps) - 1) / uint32(bps)
	dataSectors := int64(total) - int64(reserved) - int64(fats)*int64(fatSize) - int64(rootSectors)
	if dataSectors <= 0 {
		return "", ""
	}
	clusters := dataSectors / int64(spc)
	switch {
	case rootEntries == 0 || clusters >= 65525:
		return "fat32", label
	case clusters < 4085:
		return "fat12", label
	}
	return "fat16", label
}

// clean turns a path into the form without leading "/" ("" = root).
// Only "/" separates: a backslash is part of a name on Linux.
func clean(p string) string {
	p = path.Clean("/" + p)
	return strings.TrimPrefix(p, "/")
}

// split returns the elements of a cleaned path.
func split(p string) []string {
	if p = clean(p); p == "" {
		return nil
	}
	return strings.Split(p, "/")
}

// safely runs fn and turns a panic of a file system reader (corrupt data)
// into an error.
func safely(fn func() error) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("corrupt file system data: %v", p)
		}
	}()
	return fn()
}
