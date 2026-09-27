//go:build windows

package main

import "os"

// checkConfigPerm is a no-op on Windows: Go synthesizes a POSIX-style mode
// for Windows files, so the Unix 0600 check doesn't reflect real ACLs and
// would reject perfectly fine credential files. Skip it rather than trying
// to emulate Unix permissions with Windows ACLs.
func checkConfigPerm(info os.FileInfo) error {
	return nil
}
