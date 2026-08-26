//go:build !windows

package diskspace

import "syscall"

func freeBytes(path string) (uint64, bool) {
	var fs syscall.Statfs_t
	if err := syscall.Statfs(path, &fs); err != nil {
		return 0, false
	}
	// Bavail, not Bfree: Bfree counts blocks reserved for root, which an
	// ordinary process cannot write into. Using it would promise room that
	// does not exist for the user running fitr.
	return uint64(fs.Bavail) * uint64(fs.Bsize), true //nolint:gosec,unconvert // kernel-reported sizes, widths vary by platform
}

func totalBytes(path string) (uint64, bool) {
	var fs syscall.Statfs_t
	if err := syscall.Statfs(path, &fs); err != nil {
		return 0, false
	}
	return uint64(fs.Blocks) * uint64(fs.Bsize), true //nolint:gosec,unconvert // kernel-reported sizes, widths vary by platform
}
