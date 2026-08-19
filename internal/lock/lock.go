// Package lock provides a single-instance guard for operations that must not
// run concurrently on one machine.
//
// This exists because of a real incident: an orphaned eval script survived a
// cancel and kept running while a new eval started. Both talked to the same
// Ollama server, both had models resident, and every timing in the second run
// was contaminated. Nothing detected it -- the numbers looked plausible and
// were wrong. Plausible-and-wrong is the worst failure a measurement tool has.
//
// The guard is deliberately portable: no cgo, no build tags, no flock. A lock
// file records its holder and is refreshed while held; a lock that stops being
// refreshed goes stale and may be taken over, so a crashed run cannot block the
// machine forever.
package lock

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// StaleAfter is how long a lock may go un-refreshed before another process may
// take it. Generous enough to survive a long single phase without a refresh,
// short enough that a crash does not strand the machine.
const StaleAfter = 2 * time.Minute

// refreshEvery must be comfortably shorter than StaleAfter so that an ordinary
// pause -- a slow model load, a swapping machine -- is never mistaken for death.
const refreshEvery = 20 * time.Second

// Holder describes the process that owns a lock, so the error can name it.
type Holder struct {
	PID   int    `json:"pid"`
	Host  string `json:"host"`
	What  string `json:"what"`
	Since string `json:"since"`
}

// BusyError reports that another process holds the lock.
type BusyError struct {
	Holder Holder
	Path   string
	Age    time.Duration
}

func (e *BusyError) Error() string {
	return fmt.Sprintf(
		"another fitr run is already in progress: %s (pid %d on %s, started %s, last seen %s ago).\n"+
			"Concurrent runs share one inference server, so both sets of timings would be wrong.\n"+
			"Wait for it to finish, or remove %s if you are certain it is dead.",
		e.Holder.What, e.Holder.PID, e.Holder.Host, e.Holder.Since,
		e.Age.Round(time.Second), e.Path)
}

// Lock is a held single-instance lock. Release it exactly once, ideally by defer.
type Lock struct {
	path string
	stop chan struct{}
	done chan struct{}
	once sync.Once
}

// Acquire takes the named lock, or returns *BusyError if another live process
// holds it. `what` is a human description used in that error.
func Acquire(name, what string) (*Lock, error) {
	path := filepath.Join(os.TempDir(), "fitr-"+name+".lock")

	// Two attempts: the second only happens after clearing a stale lock, so
	// this cannot spin.
	for attempt := 0; attempt < 2; attempt++ {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			host, _ := os.Hostname()
			h := Holder{PID: os.Getpid(), Host: host, What: what,
				Since: time.Now().Format(time.RFC3339)}
			enc, _ := json.Marshal(h)
			_, werr := f.Write(enc)
			cerr := f.Close()
			if werr != nil || cerr != nil {
				os.Remove(path) //nolint:errcheck // best effort on a failed acquire
				return nil, errors.Join(werr, cerr)
			}
			l := &Lock{path: path, stop: make(chan struct{}), done: make(chan struct{})}
			go l.refresh()
			return l, nil
		}
		if !os.IsExist(err) {
			return nil, err
		}

		st, serr := os.Stat(path)
		if serr != nil {
			// Vanished between the failed create and the stat; try again.
			continue
		}
		age := time.Since(st.ModTime())
		if age <= StaleAfter {
			return nil, &BusyError{Holder: readHolder(path), Path: path, Age: age}
		}
		// Stale: the holder stopped refreshing. Take it over.
		if rerr := os.Remove(path); rerr != nil && !os.IsNotExist(rerr) {
			return nil, rerr
		}
	}
	return nil, &BusyError{Holder: readHolder(path), Path: path}
}

func readHolder(path string) Holder {
	var h Holder
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &h) // a corrupt lock still reports as busy, just unnamed
	}
	return h
}

// refresh touches the lock so it does not appear stale while genuinely held.
func (l *Lock) refresh() {
	defer close(l.done)
	t := time.NewTicker(refreshEvery)
	defer t.Stop()
	for {
		select {
		case <-l.stop:
			return
		case now := <-t.C:
			// Failure here is not fatal: the worst case is that a live lock
			// looks stale and a second run takes over, which is the behaviour
			// we would have had without a lock at all.
			_ = os.Chtimes(l.path, now, now)
		}
	}
}

// Release drops the lock. Safe to call more than once.
func (l *Lock) Release() error {
	var err error
	l.once.Do(func() {
		close(l.stop)
		<-l.done
		if rerr := os.Remove(l.path); rerr != nil && !os.IsNotExist(rerr) {
			err = rerr
		}
	})
	return err
}
