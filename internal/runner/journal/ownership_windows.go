//go:build windows

package journal

import (
	"os"

	"golang.org/x/sys/windows"
)

func lockWriter(file *os.File) error {
	// Lock a byte beyond SQLite's data and reserved locking range. LockFileEx
	// permits locking beyond EOF; locking the header would block SQLite's own
	// reads through its independently opened handle, even in this process.
	overlap := windows.Overlapped{OffsetHigh: 1 << 30}
	return windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &overlap)
}

func kernelFileIdentity(file *os.File) (uint64, uint64, error) {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &info); err != nil {
		return 0, 0, err
	}
	return uint64(info.VolumeSerialNumber), uint64(info.FileIndexHigh)<<32 | uint64(info.FileIndexLow), nil
}
