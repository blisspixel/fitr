package lock

import (
	"errors"
	"os"
	"sync"
	"testing"
)

// Contention must only ever produce a retryable busy answer. Windows keeps the
// name of a deleted file reserved while any handle remains open, so a
// contender's exclusive create can fail with access-denied while an owner is
// releasing -- a moment when nothing actually holds the lock. Reporting that
// as a fault abandoned concurrent work that only needed to try again.
func TestContentionOnlyEverReportsABusyLock(t *testing.T) {
	const workers, rounds = 8, 40
	var wait sync.WaitGroup
	faults := make(chan error, workers*rounds)
	for range workers {
		wait.Go(func() {
			for range rounds {
				held, err := Acquire(name(t), "contention regression")
				var busy *BusyError
				switch {
				case err == nil:
					if err := held.Release(); err != nil {
						faults <- err
					}
				case errors.As(err, &busy):
				default:
					faults <- err
				}
			}
		})
	}
	wait.Wait()
	close(faults)
	for err := range faults {
		t.Fatalf("contention surfaced as a fault rather than a busy lock: %v", err)
	}
}

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
