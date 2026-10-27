//go:build !unix

package applogs

import "os"

// inodeOf has no inode to report off Unix. The collector only runs beside
// kubelet on Linux; this keeps packages that import applogs (the CLI)
// building on Windows.
func inodeOf(os.FileInfo) uint64 { return 0 }
