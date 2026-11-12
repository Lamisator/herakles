package imagefs

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"
	"sync"
	"time"
)

// ext2/3/4 read-only: superblock, group descriptors (32/64 bit, meta_bg),
// inodes, block maps and extent trees, linear directory scans (htree
// directories are read linearly too), fast and slow symlinks, inline data
// up to 60 bytes. No journal replay, no encryption.

const (
	extIncompatFiletype   = 0x2
	extIncompatRecover    = 0x4
	extIncompatMetaBG     = 0x10
	extIncompatExtents    = 0x40
	extIncompat64bit      = 0x80
	extIncompatFlexBG     = 0x200
	extIncompatInlineData = 0x8000
	extROCompatSparse     = 0x1

	extFlagEncrypt = 0x800
	extFlagExtents = 0x80000
	extFlagInline  = 0x10000000

	extRootIno = 2
)

type extFS struct {
	r          io.ReaderAt
	size       int64
	blockSize  int64
	blocks     uint64
	firstData  uint64
	perGroup   uint64 // blocks per group
	inoPerGrp  uint64
	inodes     uint64
	inodeSize  int64
	descSize   int64
	incompat   uint32
	roCompat   uint32
	firstMeta  uint64
	groups     uint64
	recover    bool
	gdtCache   map[uint64]uint64 // group -> inode table block
	mu         sync.Mutex
	dirCache   map[uint32][]extDirent
	inodeCache map[uint32]*extInode
}

type extInode struct {
	ino     uint32
	mode    uint16
	size    int64
	mtime   time.Time
	flags   uint32
	block   [60]byte
	nblocks uint64 // 512-byte sectors in use (i_blocks)
	extents []extent
}

type extent struct {
	logical, physical uint64
	length            uint64
	uninit            bool
}

type extDirent struct {
	name string
	ino  uint32
	typ  byte
}

func openExt(r io.ReaderAt, size int64) (*extFS, error) {
	sb := readAt(r, 1024, 1024)
	if len(sb) < 1024 || binary.LittleEndian.Uint16(sb[56:]) != 0xEF53 {
		return nil, errors.New("no ext superblock")
	}
	le := binary.LittleEndian
	e := &extFS{r: r, size: size, gdtCache: map[uint64]uint64{}, dirCache: map[uint32][]extDirent{}, inodeCache: map[uint32]*extInode{}}
	logBS := le.Uint32(sb[24:])
	if logBS > 6 {
		return nil, fmt.Errorf("unsupported block size 2^%d KiB", logBS)
	}
	e.blockSize = 1024 << logBS
	e.inodes = uint64(le.Uint32(sb[0:]))
	e.blocks = uint64(le.Uint32(sb[4:]))
	e.firstData = uint64(le.Uint32(sb[20:]))
	e.perGroup = uint64(le.Uint32(sb[32:]))
	e.inoPerGrp = uint64(le.Uint32(sb[40:]))
	e.incompat = le.Uint32(sb[96:])
	e.roCompat = le.Uint32(sb[100:])
	e.inodeSize = 128
	if le.Uint32(sb[76:]) >= 1 {
		e.inodeSize = int64(le.Uint16(sb[88:]))
	}
	e.descSize = 32
	if e.incompat&extIncompat64bit != 0 {
		e.blocks |= uint64(le.Uint32(sb[336:])) << 32
		if d := int64(le.Uint16(sb[254:])); d >= 64 {
			e.descSize = d
		}
	}
	e.firstMeta = uint64(le.Uint32(sb[260:]))
	e.recover = e.incompat&extIncompatRecover != 0
	if e.perGroup == 0 || e.inoPerGrp == 0 || e.inodeSize < 128 || e.inodeSize > e.blockSize {
		return nil, errors.New("damaged superblock")
	}
	e.groups = (e.blocks - e.firstData + e.perGroup - 1) / e.perGroup
	if _, err := e.inode(extRootIno); err != nil {
		return nil, fmt.Errorf("root directory: %w", err)
	}
	return e, nil
}

func (e *extFS) readBlock(n uint64) ([]byte, error) {
	if n >= e.blocks {
		return nil, fmt.Errorf("block %d beyond the file system", n)
	}
	b := make([]byte, e.blockSize)
	if _, err := e.r.ReadAt(b, int64(n)*e.blockSize); err != nil && err != io.EOF {
		return nil, err
	}
	return b, nil
}

// hasSuper reports whether a group holds a superblock backup.
func (e *extFS) hasSuper(g uint64) bool {
	if g <= 1 || e.roCompat&extROCompatSparse == 0 {
		return true
	}
	for _, base := range []uint64{3, 5, 7} {
		n := base
		for n < g {
			n *= base
		}
		if n == g {
			return true
		}
	}
	return false
}

// inodeTable returns the first block of a group's inode table.
func (e *extFS) inodeTable(g uint64) (uint64, error) {
	e.mu.Lock()
	t, ok := e.gdtCache[g]
	e.mu.Unlock()
	if ok {
		return t, nil
	}
	perBlock := uint64(e.blockSize / e.descSize)
	var off int64
	if mg := g / perBlock; e.incompat&extIncompatMetaBG != 0 && mg >= e.firstMeta {
		first := mg * perBlock
		blk := e.firstData + first*e.perGroup
		if e.hasSuper(first) {
			blk++
		}
		off = int64(blk)*e.blockSize + int64(g%perBlock)*e.descSize
	} else {
		off = int64(e.firstData+1)*e.blockSize + int64(g)*e.descSize
	}
	d := readAt(e.r, off, int(e.descSize))
	if len(d) < 32 {
		return 0, fmt.Errorf("group descriptor %d unreadable", g)
	}
	t = uint64(binary.LittleEndian.Uint32(d[8:]))
	if e.descSize >= 64 {
		t |= uint64(binary.LittleEndian.Uint32(d[0x28:])) << 32
	}
	e.mu.Lock()
	e.gdtCache[g] = t
	e.mu.Unlock()
	return t, nil
}

func (e *extFS) inode(ino uint32) (*extInode, error) {
	if ino == 0 || uint64(ino) > e.inodes {
		return nil, fmt.Errorf("bad inode %d", ino)
	}
	e.mu.Lock()
	in, ok := e.inodeCache[ino]
	e.mu.Unlock()
	if ok {
		return in, nil
	}
	g := uint64(ino-1) / e.inoPerGrp
	idx := uint64(ino-1) % e.inoPerGrp
	table, err := e.inodeTable(g)
	if err != nil {
		return nil, err
	}
	b := readAt(e.r, int64(table)*e.blockSize+int64(idx)*e.inodeSize, int(e.inodeSize))
	if len(b) < 128 {
		return nil, fmt.Errorf("inode %d unreadable", ino)
	}
	le := binary.LittleEndian
	in = &extInode{ino: ino, mode: le.Uint16(b[0:]), flags: le.Uint32(b[32:])}
	in.size = int64(le.Uint32(b[4:])) | int64(le.Uint32(b[108:]))<<32
	in.mtime = time.Unix(int64(int32(le.Uint32(b[16:]))), 0)
	in.nblocks = uint64(le.Uint32(b[28:])) | uint64(le.Uint16(b[116:]))<<32
	copy(in.block[:], b[40:100])
	if in.size < 0 {
		return nil, fmt.Errorf("inode %d: bad size", ino)
	}
	if in.flags&extFlagExtents != 0 && in.flags&extFlagInline == 0 {
		if in.extents, err = e.extents(in.block[:], 0); err != nil {
			return nil, fmt.Errorf("inode %d: %w", ino, err)
		}
	}
	e.mu.Lock()
	if len(e.inodeCache) > 4096 {
		clear(e.inodeCache)
	}
	e.inodeCache[ino] = in
	e.mu.Unlock()
	return in, nil
}

// extents reads an extent tree node (the inode's i_block or a tree block).
func (e *extFS) extents(node []byte, depth int) ([]extent, error) {
	le := binary.LittleEndian
	if len(node) < 12 || le.Uint16(node[0:]) != 0xF30A {
		return nil, errors.New("bad extent header")
	}
	if depth > 8 {
		return nil, errors.New("extent tree too deep")
	}
	n := int(le.Uint16(node[2:]))
	level := le.Uint16(node[6:])
	if 12+12*n > len(node) {
		return nil, errors.New("bad extent count")
	}
	var out []extent
	for i := 0; i < n; i++ {
		x := node[12+12*i:]
		if level == 0 {
			length := uint64(le.Uint16(x[4:]))
			uninit := length > 32768
			if uninit {
				length -= 32768
			}
			phys := uint64(le.Uint32(x[8:])) | uint64(le.Uint16(x[6:]))<<32
			out = append(out, extent{logical: uint64(le.Uint32(x[0:])), physical: phys, length: length, uninit: uninit})
			continue
		}
		leaf := uint64(le.Uint32(x[4:])) | uint64(le.Uint16(x[8:]))<<32
		b, err := e.readBlock(leaf)
		if err != nil {
			return nil, err
		}
		sub, err := e.extents(b, depth+1)
		if err != nil {
			return nil, err
		}
		out = append(out, sub...)
	}
	return out, nil
}

// physical maps a logical block of a file to a disk block (0 = hole).
func (e *extFS) physical(in *extInode, logical uint64, indirect map[uint64][]byte) (uint64, error) {
	if in.flags&extFlagExtents != 0 {
		for _, x := range in.extents {
			if logical >= x.logical && logical < x.logical+x.length {
				if x.uninit {
					return 0, nil
				}
				return x.physical + logical - x.logical, nil
			}
		}
		return 0, nil
	}
	le := binary.LittleEndian
	per := uint64(e.blockSize / 4)
	ptr := func(blk uint64, i uint64) (uint64, error) {
		if blk == 0 {
			return 0, nil
		}
		b, ok := indirect[blk]
		if !ok {
			var err error
			if b, err = e.readBlock(blk); err != nil {
				return 0, err
			}
			if len(indirect) > 64 {
				clear(indirect)
			}
			indirect[blk] = b
		}
		return uint64(le.Uint32(b[4*i:])), nil
	}
	direct := func(i int) uint64 { return uint64(le.Uint32(in.block[4*i:])) }
	switch {
	case logical < 12:
		return direct(int(logical)), nil
	case logical < 12+per:
		return ptr(direct(12), logical-12)
	case logical < 12+per+per*per:
		l := logical - 12 - per
		b, err := ptr(direct(13), l/per)
		if err != nil {
			return 0, err
		}
		return ptr(b, l%per)
	default:
		l := logical - 12 - per - per*per
		b, err := ptr(direct(14), l/(per*per))
		if err != nil {
			return 0, err
		}
		if b, err = ptr(b, (l/per)%per); err != nil {
			return 0, err
		}
		return ptr(b, l%per)
	}
}

// extFile reads a file's content block by block.
type extFile struct {
	e  *extFS
	in *extInode
	mu sync.Mutex
	ib map[uint64][]byte
}

func (f *extFile) ReadAt(p []byte, off int64) (int, error) {
	if off >= f.in.size {
		return 0, io.EOF
	}
	if f.in.flags&extFlagInline != 0 {
		n := copy(p, f.in.block[off:min(f.in.size, 60)])
		if off+int64(n) >= f.in.size {
			return n, io.EOF
		}
		return n, errors.New("inline data beyond 60 bytes is not supported")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	end := min(off+int64(len(p)), f.in.size)
	done := 0
	for pos := off; pos < end; {
		logical := uint64(pos / f.e.blockSize)
		within := pos % f.e.blockSize
		n := min(f.e.blockSize-within, end-pos)
		phys, err := f.e.physical(f.in, logical, f.ib)
		if err != nil {
			return done, err
		}
		dst := p[done : done+int(n)]
		if phys == 0 {
			clear(dst)
		} else if phys >= f.e.blocks {
			return done, fmt.Errorf("block %d beyond the file system", phys)
		} else if _, err := f.e.r.ReadAt(dst, int64(phys)*f.e.blockSize+within); err != nil && err != io.EOF {
			return done, err
		}
		done += int(n)
		pos += n
	}
	if off+int64(done) >= f.in.size {
		return done, io.EOF
	}
	return done, nil
}

func (e *extFS) file(in *extInode) *extFile { return &extFile{e: e, in: in, ib: map[uint64][]byte{}} }

func (e *extFS) readDir(in *extInode) ([]extDirent, error) {
	e.mu.Lock()
	d, ok := e.dirCache[in.ino]
	e.mu.Unlock()
	if ok {
		return d, nil
	}
	if in.flags&extFlagEncrypt != 0 {
		return nil, errors.New("the directory is encrypted")
	}
	if in.size > 256<<20 {
		return nil, errors.New("directory too large")
	}
	le := binary.LittleEndian
	var data []byte
	var out []extDirent // with "." and ".." (lookup resolves ".." with it)
	if in.flags&extFlagInline != 0 {
		// The parent inode, then entries (only the part in i_block).
		data = in.block[4:min(int64(60), max(in.size, 4))]
		out = append(out, extDirent{name: "..", ino: le.Uint32(in.block[0:])})
	} else {
		data = make([]byte, in.size)
		if n, err := e.file(in).ReadAt(data, 0); err != nil && err != io.EOF {
			return nil, err
		} else {
			data = data[:n]
		}
	}
	for off := 0; off+8 <= len(data); {
		ino := le.Uint32(data[off:])
		rec := int(le.Uint16(data[off+4:]))
		nameLen := int(data[off+6])
		typ := data[off+7]
		if e.incompat&extIncompatFiletype == 0 {
			nameLen |= int(typ) << 8
			typ = 0
		}
		if rec < 8 || off+rec > len(data) {
			break
		}
		if ino != 0 && nameLen > 0 && off+8+nameLen <= off+rec {
			out = append(out, extDirent{name: string(data[off+8 : off+8+nameLen]), ino: ino, typ: typ})
		}
		off += rec
	}
	e.mu.Lock()
	if len(e.dirCache) > 1024 {
		clear(e.dirCache)
	}
	e.dirCache[in.ino] = out
	e.mu.Unlock()
	return out, nil
}

// lookup finds the inode of a path. Symbolic links inside the path are
// followed within the file system (absolute targets start at its root);
// the last element too when follow is set.
func (e *extFS) lookup(p string, follow bool) (*extInode, error) {
	root, err := e.inode(extRootIno)
	if err != nil {
		return nil, err
	}
	hops := 0
	var walk func(dir *extInode, parts []string) (*extInode, error)
	walk = func(dir *extInode, parts []string) (*extInode, error) {
		in := dir
		for i, name := range parts {
			if in.mode&0xF000 != 0x4000 {
				return nil, fs.ErrNotExist
			}
			if name == "." {
				continue
			}
			ents, err := e.readDir(in)
			if err != nil {
				return nil, err
			}
			var next uint32
			for _, d := range ents {
				if d.name == name {
					next = d.ino
					break
				}
			}
			if next == 0 {
				return nil, fs.ErrNotExist
			}
			child, err := e.inode(next)
			if err != nil {
				return nil, err
			}
			last := i == len(parts)-1
			if child.mode&0xF000 == 0xA000 && (!last || follow) {
				if hops++; hops > 40 {
					return nil, errors.New("too many levels of symbolic links")
				}
				target := e.linkTarget(child)
				base := in
				if strings.HasPrefix(target, "/") {
					base = root
				}
				var tparts []string
				for _, x := range strings.Split(target, "/") {
					if x != "" && x != "." {
						tparts = append(tparts, x)
					}
				}
				if child, err = walk(base, tparts); err != nil {
					return nil, err
				}
			}
			in = child
		}
		return in, nil
	}
	return walk(root, split(p))
}

func (e *extFS) entry(name string, in *extInode) Entry {
	en := Entry{Name: name, Size: in.size, ModTime: in.mtime, Mode: fs.FileMode(in.mode & 0o777)}
	switch in.mode & 0xF000 {
	case 0x4000:
		en.Type, en.Size, en.Mode = "dir", 0, en.Mode|fs.ModeDir
	case 0x8000:
		en.Type = "file"
	case 0xA000:
		en.Type, en.Mode = "symlink", en.Mode|fs.ModeSymlink
		en.Link = e.linkTarget(in)
	default:
		en.Type, en.Size = "other", 0
	}
	return en
}

func (e *extFS) linkTarget(in *extInode) string {
	if in.size <= 0 || in.size > 4096 {
		return ""
	}
	// Fast symlinks keep the target in i_block.
	if in.size < 60 && (in.flags&extFlagExtents == 0 || in.flags&extFlagInline != 0) && in.nblocks == 0 {
		return string(in.block[:in.size])
	}
	b := make([]byte, in.size)
	n, _ := e.file(in).ReadAt(b, 0)
	return string(b[:n])
}

// ReadDir lists a directory.
func (e *extFS) ReadDir(p string) (out []Entry, err error) {
	err = safely(func() error {
		in, err := e.lookup(p, true)
		if err != nil {
			return err
		}
		if in.mode&0xF000 != 0x4000 {
			return fmt.Errorf("%s is not a directory", p)
		}
		ents, err := e.readDir(in)
		if err != nil {
			return err
		}
		out = make([]Entry, 0, len(ents))
		for _, d := range ents {
			if d.name == "." || d.name == ".." {
				continue
			}
			ci, err := e.inode(d.ino)
			if err != nil {
				continue
			}
			out = append(out, e.entry(d.name, ci))
		}
		return nil
	})
	return out, err
}

// Stat describes one path.
func (e *extFS) Stat(p string) (en Entry, err error) {
	err = safely(func() error {
		in, err := e.lookup(p, false)
		if err != nil {
			return err
		}
		parts := split(p)
		name := "/"
		if len(parts) > 0 {
			name = parts[len(parts)-1]
		}
		en = e.entry(name, in)
		return nil
	})
	return en, err
}

// Open returns a regular file's content.
func (e *extFS) Open(p string) (r io.ReaderAt, size int64, err error) {
	err = safely(func() error {
		in, err := e.lookup(p, true)
		if err != nil {
			return err
		}
		if in.mode&0xF000 != 0x8000 {
			return fmt.Errorf("%s is not a regular file", p)
		}
		if in.flags&extFlagEncrypt != 0 {
			return errors.New("the file is encrypted")
		}
		r, size = e.file(in), in.size
		return nil
	})
	return r, size, err
}
