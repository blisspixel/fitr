package automation

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/blisspixel/fitr/internal/atomicfile"
	"github.com/blisspixel/fitr/internal/boundedio"
	"github.com/blisspixel/fitr/internal/lock"
	"github.com/blisspixel/fitr/internal/strictjson"
)

// Store is confined to one fixed results/.auto directory. Its public commands
// take session IDs, never arbitrary history paths. The journal is an integrity
// record; permissions still depend on the account's parent directory ACLs.
type Store struct{ Results string }

type Session struct {
	mu       sync.Mutex
	store    Store
	id       string
	journal  Journal
	guard    *lock.Lock
	poisoned error
	now      func() time.Time
	write    func(string, []byte, os.FileMode) error
}

func (store Store) path(id string, create bool) (string, error) {
	if !idPattern.MatchString(id) {
		return "", errors.New("invalid auto session ID")
	}
	root, err := store.physicalRoot(create)
	if err != nil {
		return "", err
	}
	directory := filepath.Join(root, ".auto")
	if err := physicalDirectory(directory, create); err != nil {
		return "", err
	}
	return filepath.Join(directory, id, "journal.json"), nil
}

// Results is an operator-selected boundary, so its existing aliases are
// permitted. Managed descendants are physical directories only. Resolve the
// nearest existing ancestor to support a new results directory under /var on
// macOS without granting aliases inside .auto the same permission.
func (store Store) physicalRoot(create bool) (string, error) {
	absolute, err := filepath.Abs(store.Results)
	if err != nil || store.Results == "" || strings.HasPrefix(absolute, `\\`) {
		return "", errors.New("auto requires a local results directory")
	}
	probe := absolute
	var missing []string
	for {
		_, err := os.Lstat(probe)
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrNotExist) || !create {
			return "", err
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			return "", err
		}
		missing = append(missing, filepath.Base(probe))
		probe = parent
	}
	root, err := filepath.EvalSymlinks(probe)
	if err != nil || strings.HasPrefix(root, `\\`) {
		return "", errors.New("auto results root cannot be resolved to a local directory")
	}
	for index := len(missing) - 1; index >= 0; index-- {
		root = filepath.Join(root, missing[index])
	}
	if err := physicalDirectory(root, create); err != nil {
		return "", err
	}
	return root, nil
}

func physicalDirectory(path string, create bool) error {
	parent := filepath.Dir(path)
	if parent != path {
		if err := physicalDirectory(parent, create); err != nil {
			return err
		}
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) && create {
		if err := os.Mkdir(path, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("auto storage requires physical directories without symbolic links")
	}
	return nil
}

func (store Store) Create(plan Plan) (*Session, error) {
	if err := plan.Validate(); err != nil {
		return nil, err
	}
	guard, err := lock.Acquire("auto-"+plan.ID, "auto session "+plan.ID)
	if err != nil {
		return nil, err
	}
	failed := true
	defer func() {
		if failed {
			_ = guard.Release()
		}
	}()
	path, err := store.path(plan.ID, true)
	if err != nil {
		return nil, err
	}
	store.Results = filepath.Dir(filepath.Dir(filepath.Dir(path)))
	if err := os.Mkdir(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	journal := Journal{Schema: JournalSchema, Plan: plan, Events: []Event{}}
	data, err := json.Marshal(journal)
	if err != nil {
		return nil, err
	}
	if len(data) > MaximumJournalBytes {
		return nil, errors.New("auto journal exceeds its storage limit")
	}
	if err := atomicfile.Write(path, append(data, '\n'), 0o600); err != nil {
		return nil, err
	}
	// Load a private snapshot rather than retaining mutable caller-owned slices.
	snapshot, err := store.Load(plan.ID)
	if err != nil {
		return nil, err
	}
	failed = false
	return &Session{store: store, id: plan.ID, journal: snapshot, guard: guard, now: time.Now, write: atomicfile.Write}, nil
}

func (store Store) Load(id string) (Journal, error) {
	path, err := store.path(id, false)
	if err != nil {
		return Journal{}, err
	}
	if err := physicalDirectory(filepath.Dir(path), false); err != nil {
		return Journal{}, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return Journal{}, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return Journal{}, errors.New("auto journal must be a regular file without symbolic links")
	}
	data, err := boundedio.ReadFile(path, MaximumJournalBytes)
	if err != nil {
		return Journal{}, err
	}
	if err := strictjson.Validate(data); err != nil {
		return Journal{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var journal Journal
	if err := decoder.Decode(&journal); err != nil {
		return Journal{}, err
	}
	if journal.Plan.ID != id {
		return Journal{}, errors.New("auto journal ID does not match its directory")
	}
	if _, err := journal.Replay(); err != nil {
		return Journal{}, err
	}
	return journal, nil
}

func (store Store) Open(id string) (*Session, error) {
	if !idPattern.MatchString(id) {
		return nil, errors.New("invalid auto session ID")
	}
	guard, err := lock.Acquire("auto-"+id, "auto session "+id)
	if err != nil {
		return nil, err
	}
	path, err := store.path(id, false)
	if err != nil {
		_ = guard.Release()
		return nil, err
	}
	store.Results = filepath.Dir(filepath.Dir(filepath.Dir(path)))
	journal, err := store.Load(id)
	if err != nil {
		_ = guard.Release()
		return nil, err
	}
	return &Session{store: store, id: id, journal: journal, guard: guard, now: time.Now, write: atomicfile.Write}, nil
}

func (session *Session) Close() error {
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.guard == nil {
		return nil
	}
	err := session.guard.Release()
	session.guard = nil
	session.poisoned = errors.New("auto session writer is closed")
	return err
}

func (session *Session) Snapshot() (Journal, State, error) {
	session.mu.Lock()
	defer session.mu.Unlock()
	data, err := json.Marshal(session.journal)
	if err != nil {
		return Journal{}, State{}, err
	}
	var snapshot Journal
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return Journal{}, State{}, err
	}
	state, err := snapshot.Replay()
	return snapshot, state, err
}

func (session *Session) Append(event Event, now time.Time) error {
	session.mu.Lock()
	defer session.mu.Unlock()
	return session.appendLocked(event, now)
}

func (session *Session) appendLocked(event Event, now time.Time) error {
	if session.poisoned != nil {
		return session.poisoned
	}
	if now.IsZero() {
		return errors.New("auto event needs a current time")
	}
	state, err := session.journal.Replay()
	if err != nil {
		return err
	}
	if len(session.journal.Events) >= MaximumEvents {
		return ErrBudget
	}
	if now.Before(state.LastObservedAt) {
		return fmt.Errorf("%w: clock moved backwards", ErrStale)
	}
	if err := session.checkCurrent(state.Digest); err != nil {
		return err
	}
	event.Sequence = len(session.journal.Events) + 1
	event.Previous = state.Digest
	event.At = now.UTC().Format(time.RFC3339Nano)
	event.SHA256 = ""
	event.SHA256, err = digest(event)
	if err != nil {
		return err
	}
	snapshot := session.journal
	snapshot.Events = append(append([]Event{}, session.journal.Events...), event)
	if _, err := snapshot.Replay(); err != nil {
		return err
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	if len(data)+1 > MaximumJournalBytes {
		return ErrBudget
	}
	// Retain exactly the published bytes, without caller-owned nested pointers.
	var private Journal
	if err := json.Unmarshal(data, &private); err != nil {
		return err
	}
	path, err := session.store.path(session.id, false)
	if err != nil {
		return err
	}
	if err := session.write(path, append(data, '\n'), 0o600); err != nil {
		// A filesystem error after rename may mean the write reached disk. Do
		// not refund or continue through an uncertain persistence boundary.
		session.poisoned = fmt.Errorf("auto persistence failed; reopen before any further request: %w", err)
		return session.poisoned
	}
	session.journal = private
	return nil
}

func (session *Session) checkCurrent(expected string) error {
	current, err := session.store.Load(session.id)
	if err != nil {
		return err
	}
	currentState, err := current.Replay()
	if err != nil {
		return err
	}
	if currentState.Digest != expected {
		session.poisoned = ErrStale
		return ErrStale
	}
	return nil
}
