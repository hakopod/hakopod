//go:build linux || darwin

package managedplatform

import (
	"os"

	"golang.org/x/sys/unix"
)

func lockNeonStateFile(file *os.File) (func(), error) {
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return nil, err
	}
	return func() { _ = unix.Flock(int(file.Fd()), unix.LOCK_UN) }, nil
}
