//go:build !windows

package builder

import "syscall"

// DiskSpace returns the total and available bytes of the filesystem holding
// path (available = what an unprivileged writer can still use).
func DiskSpace(path string) (total, avail uint64, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	bs := uint64(st.Bsize)
	return uint64(st.Blocks) * bs, uint64(st.Bavail) * bs, nil
}
