//go:build !unix

package cluster

import "os"

func runtimeProfileFileOwnerAllowed(os.FileInfo) bool {
	return false
}
