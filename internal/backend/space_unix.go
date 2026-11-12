//go:build !windows

package backend

import "golang.org/x/sys/unix"

func diskSpace(p string) (Space, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(p, &st); err != nil {
		return Space{}, err
	}
	bs := uint64(st.Bsize) //nolint:unconvert // int64 on some systems
	return Space{Total: uint64(st.Blocks) * bs, Free: uint64(st.Bavail) * bs}, nil
}
