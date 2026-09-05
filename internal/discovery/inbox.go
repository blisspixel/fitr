// Package discovery captures ideas as unmeasured candidates. It never fetches
// a source, executes its instructions, or promotes a claim into local evidence.
package discovery

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/blisspixel/fitr/internal/boundedio"
	"github.com/blisspixel/fitr/internal/lock"
	"github.com/blisspixel/fitr/internal/strictjson"
)

const Schema = "fitr.discovery.idea.v1"
const maximumIdeaBytes = 32 << 10
const maximumIdeas = 1000

type Idea struct {
	Schema     string `json:"schema"`
	ID         string `json:"id"`
	CapturedAt string `json:"captured_at"`
	Source     string `json:"source"`
	Model      string `json:"model,omitempty"`
	Role       string `json:"role"`
	Harness    string `json:"harness,omitempty"`
	Claim      string `json:"claim,omitempty"`
	State      string `json:"state"`
}

func New(source, model, role, harness, claim string, now time.Time) (Idea, error) {
	idea := Idea{Schema: Schema, CapturedAt: now.UTC().Format(time.RFC3339),
		Source: strings.TrimSpace(source), Model: strings.TrimSpace(model),
		Role: strings.TrimSpace(role), Harness: strings.TrimSpace(harness),
		Claim: strings.TrimSpace(claim), State: "unmeasured"}
	idea.ID = idea.digest()
	return idea, idea.Validate()
}

func (idea Idea) Validate() error {
	if idea.Schema != Schema || idea.State != "unmeasured" || idea.ID != idea.digest() {
		return errors.New("invalid discovery identity or evidence state")
	}
	if _, err := time.Parse(time.RFC3339, idea.CapturedAt); err != nil {
		return errors.New("invalid discovery capture time")
	}
	if !validText(idea.Source, 2048) || !validText(idea.Role, 64) ||
		!optionalText(idea.Model, 512) || !optionalText(idea.Harness, 256) || !optionalText(idea.Claim, 8192) {
		return errors.New("discovery fields are missing, oversized, or contain control characters")
	}
	if strings.HasPrefix(idea.Model, "-") {
		return errors.New("model reference cannot begin with a flag prefix")
	}
	if strings.Contains(idea.Source, "://") {
		parsed, err := url.Parse(idea.Source)
		if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") ||
			parsed.Hostname() == "" || parsed.User != nil {
			return errors.New("source URL must be HTTP(S) without embedded credentials; use a permalink or source label")
		}
	}
	return nil
}

func validText(value string, maximum int) bool {
	return strings.TrimSpace(value) != "" && len(value) <= maximum &&
		strings.IndexFunc(value, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }) < 0
}

func optionalText(value string, maximum int) bool { return value == "" || validText(value, maximum) }

func (idea Idea) digest() string {
	idea.ID, idea.CapturedAt = "", ""
	data, _ := json.Marshal(idea)
	digest := sha256.Sum256(append([]byte(Schema+"\x00"), data...))
	return hex.EncodeToString(digest[:])
}

func Save(directory string, idea Idea) (saved Idea, err error) {
	if err := idea.Validate(); err != nil {
		return Idea{}, err
	}
	directory, err = canonicalInboxDirectory(directory, true)
	if err != nil {
		return Idea{}, err
	}
	guard, err := acquireInbox(directory)
	if err != nil {
		return Idea{}, err
	}
	defer func() { err = errors.Join(err, guard.Release()) }()
	path := filepath.Join(directory, idea.ID+".json")
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return Idea{}, errors.New("discovery entry cannot be a symbolic link")
		}
		existing, err := Load(path)
		if err != nil || existing.ID != idea.ID {
			return Idea{}, errors.New("existing discovery entry does not match its identity")
		}
		return existing, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return Idea{}, err
	}
	entries, err := inboxEntries(directory)
	if err != nil {
		return Idea{}, err
	}
	if len(entries) >= maximumIdeas {
		return Idea{}, errors.New("discovery inbox reached its 1000-entry limit")
	}
	data, err := json.MarshalIndent(idea, "", "  ")
	if err != nil {
		return Idea{}, err
	}
	if len(data) > maximumIdeaBytes {
		return Idea{}, errors.New("discovery idea is too large")
	}
	return idea, publishIdea(path, append(data, '\n'))
}

func Load(path string) (Idea, error) {
	absolute, err := inboxAbsolutePath(path)
	if err != nil {
		return Idea{}, err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(absolute))
	if err != nil {
		return Idea{}, err
	}
	path = filepath.Join(parent, filepath.Base(absolute))
	info, err := os.Lstat(path)
	if err != nil {
		return Idea{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return Idea{}, errors.New("discovery entry cannot be a symbolic link")
	}
	data, err := boundedio.ReadFile(path, maximumIdeaBytes)
	if err != nil {
		return Idea{}, err
	}
	return decode(data)
}

func decode(data []byte) (Idea, error) {
	if len(data) > maximumIdeaBytes || strictjson.Validate(data) != nil {
		return Idea{}, errors.New("invalid discovery JSON")
	}
	var idea Idea
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&idea); err != nil {
		return Idea{}, errors.New("invalid discovery fields")
	}
	return idea, idea.Validate()
}

func List(directory, role string) (result []Idea, err error) {
	directory, err = canonicalInboxDirectory(directory, false)
	if errors.Is(err, os.ErrNotExist) {
		return []Idea{}, nil
	}
	if err != nil {
		return nil, err
	}
	guard, err := acquireInbox(directory)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, guard.Release()) }()
	entries, err := inboxEntries(directory)
	if err != nil {
		return nil, err
	}
	ideas := []Idea{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil, errors.New("discovery inbox cannot contain symbolic links")
		}
		idea, err := Load(filepath.Join(directory, entry.Name()))
		if err != nil || entry.Name() != idea.ID+".json" {
			return nil, fmt.Errorf("invalid discovery entry %s", entry.Name())
		}
		if role == "" || role == idea.Role {
			ideas = append(ideas, idea)
		}
	}
	sort.Slice(ideas, func(i, j int) bool { return ideas[i].ID < ideas[j].ID })
	return ideas, nil
}

// Resolve a configured directory alias once, then use the physical spelling for
// both locking and I/O. Raw parent components must be rejected before cleaning:
// a symlink followed by .. need not refer to its lexically cleaned parent.
func canonicalInboxDirectory(directory string, create bool) (string, error) {
	absolute, err := inboxAbsolutePath(directory)
	if err != nil {
		return "", err
	}
	if create {
		if err := os.MkdirAll(absolute, 0o700); err != nil {
			return "", err
		}
	}
	physical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(physical)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("discovery inbox must be a directory")
	}
	return physical, nil
}

func inboxAbsolutePath(path string) (string, error) {
	if strings.TrimSpace(path) == "" || strings.ContainsAny(path, "\x00\r\n") {
		return "", errors.New("invalid discovery path")
	}
	relative := strings.TrimPrefix(path, filepath.VolumeName(path))
	for _, part := range strings.FieldsFunc(relative, func(char rune) bool { return char == '/' || char == '\\' }) {
		if part == ".." {
			return "", errors.New("discovery paths cannot contain parent traversal")
		}
	}
	return filepath.Abs(path)
}

func acquireInbox(physicalDirectory string) (*lock.Lock, error) {
	if runtime.GOOS == "windows" {
		physicalDirectory = strings.ToLower(physicalDirectory)
	}
	digest := sha256.Sum256([]byte(physicalDirectory))
	guard, err := lock.Acquire("discovery-"+hex.EncodeToString(digest[:]), "update discovery inbox")
	return guard, discoveryLockError(err)
}

type discoveryBusyError struct{ cause error }

func (err *discoveryBusyError) Error() string {
	return "discovery storage is busy; retry after the current operation finishes"
}

func (err *discoveryBusyError) Unwrap() error { return err.cause }

func discoveryLockError(err error) error {
	var busy *lock.BusyError
	if errors.As(err, &busy) {
		return &discoveryBusyError{cause: err}
	}
	return err
}

func inboxEntries(directory string) ([]os.DirEntry, error) {
	file, err := os.Open(directory)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	entries, err := file.ReadDir(maximumIdeas + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if len(entries) > maximumIdeas {
		return nil, errors.New("discovery inbox exceeds its 1000-entry limit")
	}
	return entries, nil
}

// Publish complete private bytes without replacing an existing idea. The inbox
// lock covers the temporary entry as well, so List cannot observe it or count
// both links during publication. Filesystems without hard links fail closed.
func publishIdea(path string, data []byte) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".fitr-inbox-*")
	if err != nil {
		return err
	}
	defer func() {
		_ = temporary.Close()
		_ = os.Remove(temporary.Name())
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Link(temporary.Name(), path)
}
