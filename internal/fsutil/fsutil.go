// Package fsutil contains platform specific filesystem helpers: metadata
// capture and restore, device size detection and aligned device reading.
package fsutil

import (
	"io"
	"os"
	"os/user"
	"strconv"
	"sync"

	"github.com/lamisator/herakles/internal/repository"
)

// NodeFromInfo builds a node (without content) from file info.
func NodeFromInfo(path string, fi os.FileInfo) *repository.Node {
	n := &repository.Node{
		Name:    fi.Name(),
		Mode:    fi.Mode(),
		ModTime: fi.ModTime().UTC(),
	}
	m := fi.Mode()
	switch {
	case m.IsRegular():
		n.Type = repository.NodeFile
		n.Size = uint64(fi.Size())
	case m.IsDir():
		n.Type = repository.NodeDir
	case m&os.ModeSymlink != 0:
		n.Type = repository.NodeSymlink
		n.LinkTarget, _ = os.Readlink(path)
	case m&os.ModeDevice != 0 && m&os.ModeCharDevice != 0:
		n.Type = repository.NodeCharDev
	case m&os.ModeDevice != 0:
		n.Type = repository.NodeBlkDev
	case m&os.ModeNamedPipe != 0:
		n.Type = repository.NodeFIFO
	case m&os.ModeSocket != 0:
		n.Type = repository.NodeSocket
	default:
		n.Type = repository.NodeFile
	}
	fillPlatform(path, fi, n)
	return n
}

var (
	nameMu sync.Mutex
	users  = map[uint32]string{}
	groups = map[uint32]string{}
)

func lookupUser(uid uint32) string {
	nameMu.Lock()
	defer nameMu.Unlock()
	if n, ok := users[uid]; ok {
		return n
	}
	name := ""
	if u, err := user.LookupId(strconv.Itoa(int(uid))); err == nil {
		name = u.Username
	}
	users[uid] = name
	return name
}

func lookupGroup(gid uint32) string {
	nameMu.Lock()
	defer nameMu.Unlock()
	if n, ok := groups[gid]; ok {
		return n
	}
	name := ""
	if g, err := user.LookupGroupId(strconv.Itoa(int(gid))); err == nil {
		name = g.Name
	}
	groups[gid] = name
	return name
}

// AlignedReader reads from devices in fixed, sector aligned blocks, which
// raw volume handles on Windows require.
type AlignedReader struct {
	r   io.Reader
	buf []byte
	pos int
	end int
	err error
}

// NewAlignedReader wraps r using blocks of size bytes.
func NewAlignedReader(r io.Reader, size int) *AlignedReader {
	return &AlignedReader{r: r, buf: make([]byte, size)}
}

// Read implements io.Reader.
func (a *AlignedReader) Read(p []byte) (int, error) {
	if a.pos == a.end {
		if a.err != nil {
			return 0, a.err
		}
		n, err := io.ReadFull(a.r, a.buf)
		a.pos, a.end = 0, n
		if err == io.ErrUnexpectedEOF {
			err = io.EOF
		}
		a.err = err
		if n == 0 {
			return 0, a.err
		}
	}
	n := copy(p, a.buf[a.pos:a.end])
	a.pos += n
	return n, nil
}

// OpenDevice opens a block device or partition for reading and returns its size.
func OpenDevice(path string) (*os.File, int64, error) {
	f, err := openDevice(path)
	if err != nil {
		return nil, 0, err
	}
	size, err := deviceSize(f)
	if err != nil || size <= 0 {
		if end, serr := f.Seek(0, io.SeekEnd); serr == nil && end > 0 {
			size, err = end, nil
			_, _ = f.Seek(0, io.SeekStart)
		}
	}
	if err != nil {
		f.Close()
		return nil, 0, err
	}
	return f, size, nil
}

// OpenDeviceForWrite opens a device (or image file) for restoring.
func OpenDeviceForWrite(path string) (*os.File, error) { return openDeviceWrite(path) }
