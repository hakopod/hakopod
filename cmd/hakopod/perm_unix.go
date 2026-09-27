//go:build !windows

package main

import (
	"errors"
	"os"
)

// checkConfigPerm rejects a credential file that is readable/writable by
// group or other. Unix only: file modes are meaningful here.
func checkConfigPerm(info os.FileInfo) error {
	if info.Mode().Perm()&0077 != 0 {
		return errors.New("mode too permissive")
	}
	return nil
}
