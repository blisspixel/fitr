// Package diskspace answers one question: how many bytes are free on the
// volume holding a path.
//
// It exists because fitr could fill a disk. `fitr run <model> --pull` streams
// a multi-gigabyte download with no idea how much room is left, and a pasted
// Hugging Face reference pulls without even asking for --pull. Neither had a
// floor. A measurement tool that bricks the machine it was measuring has not
// made an honest trade.
//
// No cgo, no dependencies: syscall on unix, kernel32 on Windows.
package diskspace

// FreeBytes reports free space on the volume containing path, and whether the
// figure could be obtained at all.
//
// The second return is not decoration. fitr's rule is that it does not invent
// numbers, and a probe that failed must not be reported as "0 bytes free" --
// that would abort every download on a platform whose syscall we misread. An
// unreadable figure means the caller has no floor to enforce, and says so.
func FreeBytes(path string) (uint64, bool) { return freeBytes(path) }

// Headroom is the space a download must leave behind on the target volume.
//
// min(10% of the volume, 10 GB), which is BuildKit's reserve policy and the
// closest well-tested analogue: same problem shape, same artifact sizes.
//
// The MIN is the whole point and it is easy to get backwards. A reserve of
// 10 GB is right on a large disk and absurd on a 20 GB volume where it would
// forbid everything; the percentage relaxes it there. Taking the max instead
// reserves 200 GB on a 2 TB drive and refuses a one-byte write, which a test
// against a real volume caught immediately.
const (
	headroomFraction = 0.10
	headroomCap      = 10 << 30
)

// Headroom returns the bytes that must remain free on a volume of totalBytes.
func Headroom(totalBytes uint64) uint64 {
	if h := uint64(float64(totalBytes) * headroomFraction); h < headroomCap {
		return h
	}
	return headroomCap
}

// Fits reports whether want bytes can be written to path while leaving the
// volume's headroom intact, plus the free figure used to decide.
//
// When free space cannot be read it returns true: fitr declines to block an
// action on a measurement it could not take. The caller is told the figure was
// unavailable so it can say so rather than implying the check passed.
func Fits(path string, want uint64) (ok bool, free uint64, known bool) {
	free, known = FreeBytes(path)
	if !known {
		return true, 0, false
	}
	total, totalKnown := totalBytes(path)
	room := uint64(headroomCap)
	if totalKnown {
		room = Headroom(total)
	}
	if free < room {
		return false, free, true
	}
	return free-room >= want, free, true
}
