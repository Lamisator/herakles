package backend

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"

	"github.com/hirochachacha/go-smb2"
)

// Space is the capacity of the storage a repository lives on. With Quota
// set, Total is the repository's quota (Herakles server) and Free what is
// left of it.
type Space struct {
	Total uint64 `json:"total"`
	Free  uint64 `json:"free"`
	Quota bool   `json:"quota,omitempty"`
}

// SpaceReporter is implemented by backends that can tell their capacity.
type SpaceReporter interface {
	Space(ctx context.Context) (Space, error)
}

// ErrNoSpaceInfo is returned when the storage does not report its capacity.
var ErrNoSpaceInfo = errors.New("the storage does not report its capacity")

// SpaceOf asks a backend for its capacity.
func SpaceOf(ctx context.Context, b Backend) (Space, error) {
	if l, ok := b.(*Limited); ok {
		b = l.Backend
	}
	if s, ok := b.(SpaceReporter); ok {
		return s.Space(ctx)
	}
	return Space{}, ErrNoSpaceInfo
}

// DiskSpace reports the file system holding path (or its nearest existing
// parent, for a repository that does not exist yet).
func DiskSpace(path string) (Space, error) {
	p := filepath.Clean(path)
	for {
		if _, err := os.Stat(p); err == nil {
			return diskSpace(p)
		}
		parent := filepath.Dir(p)
		if parent == p {
			return Space{}, ErrNoSpaceInfo
		}
		p = parent
	}
}

// Space reports the disk of a local repository.
func (l *Local) Space(ctx context.Context) (Space, error) { return DiskSpace(l.root) }

// Space reports the share or SFTP server's file system, when it tells.
func (b *fsBackend) Space(ctx context.Context) (Space, error) {
	s, ok := b.fs.(interface{ space(p string) (Space, error) })
	if !ok {
		return Space{}, ErrNoSpaceInfo
	}
	defer b.acquire()()
	return s.space(b.root)
}

func (f sftpFS) space(p string) (Space, error) {
	for {
		st, err := f.c.StatVFS(p)
		if err == nil {
			return Space{Total: st.TotalSpace(), Free: st.Frsize * st.Bavail}, nil
		}
		if _, serr := f.c.Stat(p); serr == nil || p == "/" || p == "." || p == "" {
			return Space{}, ErrNoSpaceInfo // statvfs@openssh.com not offered
		}
		p = path.Dir(p)
	}
}

func (f smbFS) space(p string) (Space, error) {
	if p == "" {
		p = "."
	}
	st, err := f.s.Statfs(p)
	if err != nil {
		if st, err = f.s.Statfs("."); err != nil {
			return Space{}, err
		}
	}
	return smbSpace(st), nil
}

// smbSpace converts SMB's FileFsFullSizeInformation. Sizes come in
// allocation units of BytesPerSector × SectorsPerAllocationUnit; go-smb2
// calls these BlockSize and FragmentSize.
func smbSpace(st smb2.FileFsInfo) Space {
	unit := st.BlockSize() * max(st.FragmentSize(), 1)
	return Space{Total: unit * st.TotalBlockCount(), Free: unit * st.AvailableBlockCount()}
}

// Space asks the Herakles server for the repository's quota, or the free
// space of its data directory.
func (r *REST) Space(ctx context.Context) (Space, error) {
	resp, err := r.do(ctx, "GET", r.base.String()+"?space", nil, nil)
	if err != nil {
		return Space{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return Space{}, ErrNoSpaceInfo // older server
	}
	var s Space
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&s); err != nil {
		return Space{}, err
	}
	return s, nil
}
