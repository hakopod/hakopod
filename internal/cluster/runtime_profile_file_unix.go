//go:build unix

package cluster

import (
	"os"
	"syscall"
)

func runtimeProfileFileOwnerAllowed(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && (stat.Uid == 0 || stat.Uid == uint32(os.Geteuid()))
}
