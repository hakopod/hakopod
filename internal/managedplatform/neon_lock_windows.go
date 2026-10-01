package managedplatform

import (
	"os"

	"golang.org/x/sys/windows"
)

func lockNeonStateFile(file *os.File) (func(), error) {
	handle := windows.Handle(file.Fd())
	overlapped := new(windows.Overlapped)
	if err := windows.LockFileEx(handle, windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, overlapped); err != nil {
		return nil, err
	}
	return func() { _ = windows.UnlockFileEx(handle, 0, 1, 0, overlapped) }, nil
}
