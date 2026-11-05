//go:build !windows

package platform

import "syscall"

// FreeSpace reports the bytes available to an unprivileged user at path.
func FreeSpace(path string) (int64, error) {
	var stats syscall.Statfs_t
	if err := syscall.Statfs(path, &stats); err != nil {
		return 0, err
	}

	//nolint:gosec // real filesystems keep the block count well below the int64 limit
	return int64(stats.Bavail) * int64(stats.Bsize), nil
}
