package automation

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blisspixel/fitr/internal/atomicfile"
	"github.com/blisspixel/fitr/internal/ollama"
)

func createdSession(t *testing.T) *Session {
	t.Helper()
	session, err := (Store{Results: t.TempDir()}).Create(sessionPlan(t))
	if err != nil {
		t.Fatal(err)
	}
	session.now = func() time.Time { return sessionTime().Add(2 * time.Second) }
	t.Cleanup(func() {
		if err := session.Close(); err != nil {
			t.Error(err)
		}
	})
	return session
}

func journalPath(t *testing.T, session *Session) string {
	t.Helper()
	path, err := session.store.path(session.id, false)
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func publishJournal(t *testing.T, path string, journal Journal) {
	t.Helper()
	data, err := json.Marshal(journal)
	if err != nil {
		t.Fatal(err)
	}
	if err := atomicfile.Write(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func startSessionPoint(t *testing.T, session *Session) ollama.InferenceRequest {
	t.Helper()
	if err := session.Append(Event{Action: "point_started", Phase: "exploration", Point: 1, RunID: "first-run"}, sessionTime().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	return ollama.InferenceRequest{Kind: "generate", Model: session.journal.Plan.Candidates[0].Model, MaxOutputTokens: 10}
}

func TestSessionInterruptedReservationsStayChargedAfterReopen(t *testing.T) {
	session := createdSession(t)
	request := startSessionPoint(t, session)
	permit, err := session.Reserve(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !permit.Deadline.Equal(sessionTime().Add(2400 * time.Second)) {
		t.Fatalf("exploration deadline = %v", permit.Deadline)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	resumed, err := session.store.Open(session.id)
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.Close()
	resumed.now = func() time.Time { return sessionTime().Add(3 * time.Second) }
	_, state, err := resumed.Snapshot()
	if err != nil || state.Requests != 1 || state.RequestedOutputTokens != 10 || state.ActivePoint != 1 {
		t.Fatalf("resumed state=%+v error=%v", state, err)
	}
	if _, err := resumed.Reserve(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	_, state, _ = resumed.Snapshot()
	if state.Requests != 2 || state.RequestedOutputTokens != 20 {
		t.Fatalf("reservation was refunded: %+v", state)
	}
}

func TestSessionPostRenameUncertaintyPoisonsWriterAndPreservesCharge(t *testing.T) {
	session := createdSession(t)
	request := startSessionPoint(t, session)
	writes := 0
	session.write = func(path string, data []byte, mode os.FileMode) error {
		writes++
		if err := atomicfile.Write(path, data, mode); err != nil {
			return err
		}
		return errors.New("injected post-rename metadata failure")
	}
	if _, err := session.Reserve(t.Context(), request); err == nil {
		t.Fatal("uncertain persistence admitted inference")
	}
	if _, err := session.Reserve(t.Context(), request); err == nil || writes != 1 {
		t.Fatalf("poisoned writer dispatched again: writes=%d error=%v", writes, err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	journal, err := session.store.Load(session.id)
	if err != nil {
		t.Fatal(err)
	}
	state, err := journal.Replay()
	if err != nil || state.Requests != 1 || state.RequestedOutputTokens != 10 {
		t.Fatalf("uncertain reservation disappeared: %+v, %v", state, err)
	}
}

func TestSessionDiskRewriteCASPoisonsWriter(t *testing.T) {
	session := createdSession(t)
	path := journalPath(t, session)
	original, _, _ := session.Snapshot()
	changed := original
	changed.Events = []Event{{Action: "finished", Outcome: "cancelled"}}
	resealJournal(t, &changed)
	publishJournal(t, path, changed)
	event := Event{Action: "point_started", Phase: "exploration", Point: 1, RunID: "test-run"}
	if err := session.Append(event, sessionTime().Add(time.Second)); !errors.Is(err, ErrStale) {
		t.Fatalf("disk CAS error = %v", err)
	}
	publishJournal(t, path, original)
	if err := session.Append(event, sessionTime().Add(time.Second)); !errors.Is(err, ErrStale) {
		t.Fatalf("restoring disk revived a stale writer: %v", err)
	}
}

func TestSessionSnapshotsAndAppendsDoNotRetainCallerPointers(t *testing.T) {
	session := createdSession(t)
	for point := 1; point <= len(session.journal.Plan.Candidates); point++ {
		for _, event := range sessionPointEvents("exploration", point) {
			if err := session.Append(event, sessionTime().Add(time.Second)); err != nil {
				t.Fatal(err)
			}
		}
	}
	ref := sessionStoreRef("exploration")
	if err := session.Append(Event{Action: "exploration_closed", StoreRef: ref}, sessionTime().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	ref.ID = "caller-mutated"
	confirmation := syntheticConfirmation(t, session.journal.Plan)
	if err := session.Append(Event{Action: "confirmation_started", Confirmation: &confirmation}, sessionTime().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	confirmation.Candidates[0].Model.Value = "changed"
	journal, state, err := session.Snapshot()
	if err != nil || state.ExplorationStore.ID != "exploration" {
		t.Fatalf("caller pointer changed persisted journal: %+v, %v", state, err)
	}
	journal.Plan.Candidates[0].Model = "external-change"
	state.Confirmation.Candidates[0].Model.Value = "external-change"
	if _, _, err := session.Snapshot(); err != nil {
		t.Fatalf("snapshot changed writer state: %v", err)
	}
}

func TestSessionClockRollbackAndClosedWriterCannotSpend(t *testing.T) {
	session := createdSession(t)
	request := startSessionPoint(t, session)
	session.now = func() time.Time { return sessionTime() }
	if _, err := session.Reserve(t.Context(), request); !errors.Is(err, ErrStale) {
		t.Fatalf("clock rollback error = %v", err)
	}
	if err := session.Append(Event{Action: "finished", Outcome: "cancelled"}, time.Time{}); err == nil {
		t.Fatal("zero time accepted")
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Reserve(t.Context(), request); err == nil {
		t.Fatal("closed session spent budget")
	}
}

func TestSessionStrictJSONAndConfinement(t *testing.T) {
	for _, scenario := range []string{"unknown", "duplicate", "trailing", "oversized", "wrong-id", "directory"} {
		t.Run(scenario, func(t *testing.T) { checkInvalidJournalFile(t, scenario) })
	}
	session := createdSession(t)
	for _, id := range []string{"../escape", "a/b", "", "UPPERCASE"} {
		if _, err := session.store.Load(id); err == nil {
			t.Fatalf("invalid ID accepted: %q", id)
		}
		if _, err := session.store.Open(id); err == nil {
			t.Fatalf("invalid ID opened: %q", id)
		}
	}
	if _, err := (Store{}).Create(sessionPlan(t)); err == nil {
		t.Fatal("empty results root accepted")
	}
}

func checkInvalidJournalFile(t *testing.T, scenario string) {
	t.Helper()
	session := createdSession(t)
	path := journalPath(t, session)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	switch scenario {
	case "unknown":
		data = append([]byte(`{"unknown":true,`), data[1:]...)
	case "duplicate":
		data = append([]byte(`{"schema":"duplicate",`), data[1:]...)
	case "trailing":
		data = append(data, []byte(`{}`)...)
	case "oversized":
		data = bytes.Repeat([]byte(" "), MaximumJournalBytes+1)
	case "wrong-id":
		data = bytes.ReplaceAll(data, []byte(session.id), []byte("different-session"))
	case "directory":
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if scenario != "directory" {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := session.store.Load(session.id); err == nil {
		t.Fatal("invalid managed journal accepted")
	}
}

func TestSessionStoreRejectsManagedSymlinks(t *testing.T) {
	for _, part := range []string{"journal", "session", "namespace"} {
		t.Run(part, func(t *testing.T) {
			session := createdSession(t)
			path := journalPath(t, session)
			if err := session.Close(); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(t.TempDir(), "target")
			switch part {
			case "session":
				path = filepath.Dir(path)
			case "namespace":
				path = filepath.Dir(filepath.Dir(path))
			}
			if err := os.Rename(path, target); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, path); err != nil {
				t.Skipf("symlink unavailable: %v", err)
			}
			if _, err := session.store.Load(session.id); err == nil {
				t.Fatal("managed symlink accepted")
			}
		})
	}
}

func TestSessionResultsAliasesResolveAndWritersPinPhysicalRoot(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()
	alias := filepath.Join(t.TempDir(), "results-alias")
	if err := os.Symlink(first, alias); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	store := Store{Results: filepath.Join(alias, "new-results")}
	session, err := store.Create(sessionPlan(t))
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	physical, err := filepath.EvalSymlinks(filepath.Join(first, "new-results"))
	if err != nil || !strings.EqualFold(session.store.Results, physical) {
		t.Fatalf("writer root=%q want=%q error=%v", session.store.Results, physical, err)
	}
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(second, alias); err != nil {
		t.Fatal(err)
	}
	if err := session.Append(Event{Action: "finished", Outcome: "cancelled"}, sessionTime().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(second, "new-results")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("writer followed a replaced root alias: %v", err)
	}
}

func TestSessionLockPreventsConcurrentWriterAndDuplicateCreation(t *testing.T) {
	session := createdSession(t)
	if _, err := session.store.Open(session.id); err == nil {
		t.Fatal("second live writer opened")
	}
	if _, err := session.store.Create(session.journal.Plan); err == nil {
		t.Fatal("duplicate live session created")
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := session.store.Create(session.journal.Plan); err == nil {
		t.Fatal("existing session overwritten")
	}
	opened, err := session.store.Open(session.id)
	if err != nil {
		t.Fatalf("failed duplicate creation leaked a lock: %v", err)
	}
	if err := opened.Close(); err != nil {
		t.Fatal(err)
	}
}
