//go:build windows

package backup

import "golang.org/x/sys/windows"

// availableDiskSpace reports free bytes on the volume containing dir, via
// GetDiskFreeSpaceEx (golang.org/x/sys/windows is already a module
// dependency, so no new dependency is added).
func availableDiskSpace(dir string) (uint64, error) {
	path, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return 0, err
	}
	var freeAvail, total, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(path, &freeAvail, &total, &totalFree); err != nil {
		return 0, err
	}
	return freeAvail, nil
}
