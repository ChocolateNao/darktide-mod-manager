//go:build windows

package platform

import "golang.org/x/sys/windows"

// FreeSpace reports the bytes available to the current user at path.
func FreeSpace(path string) (int64, error) {
	ptr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}

	var free, total, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(ptr, &free, &total, &totalFree); err != nil {
		return 0, err
	}

	return int64(free), nil
}
