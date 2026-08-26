//go:build windows

package diskspace

import (
	"syscall"
	"unsafe"
)

var procGetDiskFreeSpaceExW = syscall.NewLazyDLL("kernel32.dll").
	NewProc("GetDiskFreeSpaceExW")

// diskFreeSpace returns (free-to-caller, total) for the volume holding path.
//
// GetDiskFreeSpaceExW's first output is the space available to the calling
// user, which is the one that matters: on a volume with a quota it is smaller
// than the volume's free space, and the larger figure would promise room the
// process cannot actually use.
func diskFreeSpace(path string) (free, total uint64, ok bool) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0, 0, false
	}
	var freeToCaller, totalBytes, totalFree uint64
	r, _, _ := procGetDiskFreeSpaceExW.Call(
		uintptr(unsafe.Pointer(p)),
		uintptr(unsafe.Pointer(&freeToCaller)),
		uintptr(unsafe.Pointer(&totalBytes)),
		uintptr(unsafe.Pointer(&totalFree)),
	)
	if r == 0 {
		return 0, 0, false
	}
	return freeToCaller, totalBytes, true
}

func freeBytes(path string) (uint64, bool) {
	free, _, ok := diskFreeSpace(path)
	return free, ok
}

func totalBytes(path string) (uint64, bool) {
	_, total, ok := diskFreeSpace(path)
	return total, ok
}
