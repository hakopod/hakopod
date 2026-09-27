//go:build !windows

package backup

import "syscall"

// availableDiskSpace reports free bytes on the filesystem containing dir.
// Unix: syscall.Statfs, byte-for-byte the same arithmetic as before.
func availableDiskSpace(dir string) (uint64, bool, error) {
	var disk syscall.Statfs_t
	if err := syscall.Statfs(dir, &disk); err != nil {
		return 0, false, err
	}
	return uint64(disk.Bavail) * uint64(disk.Bsize), true, nil
}
