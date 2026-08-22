package lock

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// name keeps each test on its own lock file so a real fitr run on the same
// machine cannot make the suite flaky.
func name(t *testing.T) string {
	t.Helper()
	return "test-" + strings.ReplaceAll(t.Name(), "/", "-")
}

func TestAcquireAndRelease(t *testing.T) {
	l, err := Acquire(name(t), "unit test")
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	if err := l.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	l2, err := Acquire(name(t), "unit test")
	if err != nil {
		t.Fatalf("re-acquire after release: %v", err)
	}
	t.Cleanup(func() { _ = l2.Release() })
}

func TestSecondAcquireIsRefused(t *testing.T) {
	l, err := Acquire(name(t), "the first run")
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	t.Cleanup(func() { _ = l.Release() })

	_, err = Acquire(name(t), "the second run")
	if err == nil {
		t.Fatal("second acquire succeeded; concurrent evals would contaminate each other")
	}
	var busy *BusyError
	if !asBusy(err, &busy) {
		t.Fatalf("want *BusyError, got %T: %v", err, err)
	}
	// The message must name the holder, or the user cannot act on it.
	for _, want := range []string{"the first run", "already in progress"} {
		if !strings.Contains(busy.Error(), want) {
			t.Errorf("BusyError message missing %q:\n%s", want, busy.Error())
		}
	}
	if busy.Holder.PID != os.Getpid() {
		t.Errorf("holder pid = %d, want %d", busy.Holder.PID, os.Getpid())
	}
}

func TestStaleLockIsTakenOver(t *testing.T) {
	path := filepath.Join(os.TempDir(), "fitr-"+name(t)+".lock")
	enc, _ := json.Marshal(Holder{PID: 999999, Host: "ghost", What: "a crashed run"})
	if err := os.WriteFile(path, enc, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(path) })

	old := time.Now().Add(-2 * StaleAfter)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}

	l, err := Acquire(name(t), "the new run")
	if err != nil {
		t.Fatalf("a lock older than StaleAfter must be takeable, got: %v", err)
	}
	t.Cleanup(func() { _ = l.Release() })

	if h := readHolder(path); h.PID != os.Getpid() {
		t.Errorf("lock file still names pid %d, want takeover by %d", h.PID, os.Getpid())
	}
}

func TestFreshLockIsNotTakenOver(t *testing.T) {
	path := filepath.Join(os.TempDir(), "fitr-"+name(t)+".lock")
	enc, _ := json.Marshal(Holder{PID: 999999, Host: "other", What: "a live run"})
	if err := os.WriteFile(path, enc, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(path) })

	if _, err := Acquire(name(t), "an impatient run"); err == nil {
		t.Fatal("a freshly-touched lock must not be stolen")
	}
}

func TestOversizedHolderMetadataIsIgnored(t *testing.T) {
	path := filepath.Join(os.TempDir(), "fitr-"+name(t)+".lock")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", maxHolderBytes+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(path) })
	if got := readHolder(path); got != (Holder{}) {
		t.Fatalf("oversized holder metadata was accepted: %+v", got)
	}
}

func TestReleaseIsIdempotent(t *testing.T) {
	l, err := Acquire(name(t), "unit test")
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Release(); err != nil {
		t.Fatalf("first release: %v", err)
	}
	if err := l.Release(); err != nil {
		t.Fatalf("second release must be a no-op, got: %v", err)
	}
}

func TestStaleHolderCannotRefreshOrRemoveReplacement(t *testing.T) {
	l, err := Acquire(name(t), "old run")
	if err != nil {
		t.Fatal(err)
	}
	path := l.path
	replacement := Holder{PID: 42, Host: "new-host", What: "new run", Token: "replacement-token"}
	encoded, err := json.Marshal(replacement)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	if l.refreshOnce(time.Now()) {
		t.Fatal("stale holder refreshed a replacement lock")
	}
	if err := l.Release(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(path) })
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("stale holder removed replacement lock: %v", err)
	}
	if got := readHolder(path); got.Token != replacement.Token || got.What != replacement.What {
		t.Fatalf("replacement lock changed: %+v", got)
	}
}

func TestRefreshKeepsLockFresh(t *testing.T) {
	// A held lock must not drift into staleness while the run is still alive.
	if refreshEvery >= StaleAfter {
		t.Fatalf("refreshEvery (%v) must be well under StaleAfter (%v), or a live "+
			"run can be mistaken for a dead one", refreshEvery, StaleAfter)
	}
}

func asBusy(err error, target **BusyError) bool {
	b, ok := err.(*BusyError)
	if ok {
		*target = b
	}
	return ok
}
