package lock

import (
	"os"
	"testing"
)

func TestHolderReaderDoesNotStrandReleasedLock(t *testing.T) {
	held, err := Acquire(name(t), "reader release regression")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = held.Release() })
	reader, err := openHolder(held.path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	// A contender can still hold its diagnostic read handle when the owner
	// releases. Windows applies these sharing rules across processes too.
	if err := held.Release(); err != nil {
		t.Fatalf("diagnostic reader prevented release: %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(held.path); !os.IsNotExist(err) {
		t.Fatalf("released lock remained after reader closed: %v", err)
	}
	next, err := Acquire(name(t), "next owner")
	if err != nil {
		t.Fatalf("reader stranded a live-looking lock: %v", err)
	}
	if err := next.Release(); err != nil {
		t.Fatal(err)
	}
}
