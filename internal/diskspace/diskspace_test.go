package diskspace

import (
	"os"
	"testing"
)

func TestFreeBytesReadsARealVolume(t *testing.T) {
	free, ok := FreeBytes(t.TempDir())
	if !ok {
		t.Skip("free space unreadable on this platform/filesystem")
	}
	if free == 0 {
		t.Fatal("a writable temp dir reported zero bytes free")
	}
	t.Logf("free: %.1f GB", float64(free)/(1<<30))
}

// An unreadable figure must not read as "zero free", which would abort every
// download on a platform whose syscall we misread.
func TestUnreadablePathDoesNotBlock(t *testing.T) {
	ok, _, known := Fits(string([]byte{0}), 1<<30)
	if known {
		t.Skip("this platform resolved a deliberately invalid path")
	}
	if !ok {
		t.Fatal("an unmeasurable volume must not block the action; fitr does not " +
			"invent a number and must not act on one it failed to read")
	}
}

// The reserve is min(10%, 10 GB), not max. Taking the max reserves 200 GB on a
// 2 TB drive and refuses a one-byte write; a run against a real volume caught
// exactly that.
func TestHeadroomTakesTheSmallerOfPercentAndCap(t *testing.T) {
	// Small volume: the percentage relaxes the reserve so it stays usable.
	if got := Headroom(20 << 30); got != 2<<30 {
		t.Errorf("20 GB volume: headroom %d, want 2 GB (10%%)", got)
	}
	// Large volume: the cap holds it at a sane absolute figure.
	if got := Headroom(4000 << 30); got != headroomCap {
		t.Errorf("4 TB volume: headroom %d, want the %d cap", got, uint64(headroomCap))
	}
	// Never reserve more than the volume holds.
	if got := Headroom(1 << 30); got > 1<<30 {
		t.Errorf("1 GB volume: headroom %d exceeds the volume", got)
	}
}

func TestFitsRefusesWhenTheDownloadWouldEatTheHeadroom(t *testing.T) {
	dir := t.TempDir()
	free, ok := FreeBytes(dir)
	if !ok {
		t.Skip("free space unreadable")
	}
	// Something larger than the whole volume can never fit.
	if fits, _, known := Fits(dir, free+(1<<40)); known && fits {
		t.Error("a download larger than free space was reported as fitting")
	}
	// A single byte should fit on any volume with real headroom.
	if fits, _, known := Fits(dir, 1); known && !fits && free > 2*uint64(headroomCap) {
		t.Errorf("one byte did not fit with %.1f GB free", float64(free)/(1<<30))
	}
}

func TestHomeDirectoryIsMeasurable(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	if _, ok := FreeBytes(home); !ok {
		t.Errorf("free space unreadable for the home directory, which is where "+
			"models land: %s", home)
	}
}
