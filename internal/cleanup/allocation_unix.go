//go:build unix

package cleanup

import (
	"os"
	"syscall"
)

// observedAllocation reads the block count already stored on the file info.
// It does not open the file. POSIX st_blocks is a count of 512-byte units
// (stat(2)). Go 1.27.2's syscall.Stat_t uses int64 Blocks; device and inode
// widths vary by target and are normalized below. A missing stat, a zero
// inode, a negative count, or a product that does not fit in int64 is
// unmeasured.
func observedAllocation(info os.FileInfo) (int64, uint64, uint64, bool) {
	if info == nil || info.Sys() == nil {
		return 0, 0, 0, false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat == nil || stat.Blocks < 0 {
		return 0, 0, 0, false
	}
	dev, devOK := unsignedID(stat.Dev)
	ino, inoOK := unsignedID(stat.Ino)
	if !devOK || !inoOK || ino == 0 {
		return 0, 0, 0, false
	}
	const blockBytes int64 = 512
	if stat.Blocks > (1<<63-1)/blockBytes {
		return 0, 0, 0, false
	}
	return stat.Blocks * blockBytes, dev, ino, true
}

// unsignedID accepts the device and inode widths Go 1.27.2 uses in
// syscall.Stat_t. Those fields are uint64, uint32, or int32 depending on
// the target, so a direct conversion is either a compile error or a no-op
// the linter rejects. A negative device number is unmeasured.
func unsignedID(value any) (uint64, bool) {
	switch typed := value.(type) {
	case uint64:
		return typed, true
	case uint32:
		return uint64(typed), true
	case int32:
		if typed < 0 {
			return 0, false
		}
		return uint64(typed), true
	default:
		return 0, false
	}
}
