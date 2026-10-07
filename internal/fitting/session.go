package fitting

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/blisspixel/fitr/internal/atomicfile"
	"github.com/blisspixel/fitr/internal/boundedio"
	"github.com/blisspixel/fitr/internal/lock"
	"github.com/blisspixel/fitr/internal/strictjson"
)

// Store keeps one JSON session per id under results/.fitting.
// The id is the filename, so it cannot name a path.
type Store struct{ Results string }

func (store Store) Create(session Session) error {
	if session.Schema == "" {
		session.Schema = SessionSchema
	}
	if err := session.Validate(); err != nil {
		return err
	}
	guard, err := lock.Acquire("fitting-"+session.Plan.ID, "create fitting "+session.Plan.ID)
	if err != nil {
		return err
	}
	defer func() { _ = guard.Release() }()
	path, err := store.path(session.Plan.ID, true)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(path); err == nil {
		return errors.New("fitting session already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return writeSession(path, session)
}

func (store Store) Save(session Session) error {
	if err := session.Validate(); err != nil {
		return err
	}
	path, err := store.path(session.Plan.ID, true)
	if err != nil {
		return err
	}
	return writeSession(path, session)
}

func (store Store) Load(id string) (Session, error) {
	path, err := store.path(id, false)
	if err != nil {
		return Session{}, err
	}
	data, err := boundedio.ReadFile(path, 1<<20)
	if err != nil {
		return Session{}, err
	}
	session, err := decodeSession(data)
	if err != nil {
		return Session{}, err
	}
	if session.Plan.ID != id {
		return Session{}, errors.New("fitting session id does not match its file")
	}
	return session, nil
}

func decodeSession(data []byte) (Session, error) {
	if err := strictjson.Validate(data); err != nil {
		return Session{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var session Session
	if err := decoder.Decode(&session); err != nil {
		return Session{}, err
	}
	if err := session.Validate(); err != nil {
		return Session{}, err
	}
	return session, nil
}

func (session Session) Validate() error {
	if session.Schema != SessionSchema {
		return errors.New("invalid fitting session schema")
	}
	if err := session.Plan.Validate(); err != nil {
		return err
	}
	switch session.Phase {
	case PhaseBlocked, PhasePreviewed, PhaseApproved, PhaseMeasuring, PhaseMeasured, PhaseDelegated, PhaseAdoptionClosed:
	default:
		return errors.New("invalid fitting phase")
	}
	if session.Phase == PhaseBlocked && !session.Plan.Blocked {
		return errors.New("blocked phase does not match the plan")
	}
	if session.Phase != PhaseBlocked && session.Plan.Blocked {
		return errors.New("a blocked plan cannot enter a measuring phase")
	}
	if session.ConfirmationSeed != "" {
		return errors.New("this fitting does not store a replacement confirmation seed")
	}
	if session.Phase == PhaseDelegated && (session.AutoSessionID == "" || strings.ContainsAny(session.AutoSessionID, `/\`)) {
		return errors.New("delegated fitting has no auto session id")
	}
	if session.ExplorationID != "" && strings.ContainsAny(session.ExplorationID, `/\`) {
		return errors.New("invalid fitting exploration id")
	}
	seen := map[string]bool{}
	for _, point := range session.Points {
		if seen[point.Model] || point.RunID == "" || !strings.HasPrefix(point.EvidenceSHA256, "sha256:") {
			return errors.New("fitting points need unique models and evidence digests")
		}
		known := false
		for _, candidate := range session.Plan.Candidates {
			if candidate == point.Model {
				known = true
			}
		}
		if !known {
			return errors.New("fitting point names a candidate outside the plan")
		}
		seen[point.Model] = true
	}
	return nil
}

func (store Store) path(id string, create bool) (string, error) {
	if !idPattern.MatchString(id) {
		return "", errors.New("invalid fitting session id")
	}
	if store.Results == "" {
		return "", errors.New("fitting requires a results directory")
	}
	root, err := filepath.Abs(store.Results)
	if err != nil || strings.HasPrefix(root, `\\`) {
		return "", errors.New("fitting requires a local results directory")
	}
	if create {
		if err := os.MkdirAll(root, 0o700); err != nil {
			return "", err
		}
	}
	// The operator-selected root may have an alias, including /var on macOS.
	// Managed descendants must stay physical and cannot redirect a private write.
	root, err = filepath.EvalSymlinks(root)
	if err != nil || strings.HasPrefix(root, `\\`) {
		return "", errors.New("fitting results root cannot be resolved locally")
	}
	directory := filepath.Join(root, ".fitting")
	if create {
		if err := os.Mkdir(directory, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return "", err
		}
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("fitting storage requires a physical managed directory")
	}
	path := filepath.Join(directory, id+".json")
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return "", errors.New("fitting storage requires a regular session file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return path, nil
}

func writeSession(path string, session Session) error {
	data, err := json.MarshalIndent(session, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(path, append(data, '\n'), 0o600)
}
