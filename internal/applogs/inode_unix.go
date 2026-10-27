//go:build unix

package applogs

import (
	"os"
	"syscall"
)

// inodeOf returns the inode number of a stat result, or 0 when unknown.
func inodeOf(fi os.FileInfo) uint64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Ino)
	}
	return 0
}
