package eval

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

// BuiltinDefinitionHashes identifies the exact embedded evaluation corpus.
// TaskSetSHA256 covers executable and generated task definitions. SpecSHA256
// additionally covers version.json, which defines the result schema contract.
type BuiltinDefinitionHashes struct {
	TaskSetSHA256 string `json:"task_set_sha256"`
	SpecSHA256    string `json:"spec_sha256"`
}

// BuiltinHashes returns deterministic hashes of the embedded definitions.
// Paths and byte lengths are framed before content so file boundaries cannot
// be rearranged into the same digest.
func BuiltinHashes() (BuiltinDefinitionHashes, error) {
	return hashBuiltinCorpus(tasksFS)
}

// EffectiveHashes identifies the exact in-memory battery selected for a run.
// Unlike BuiltinHashes, it includes user checks after validation and merging.
// This is the receipt real runs persist so a local task cannot alter a verdict
// without altering both the task-set and complete-spec hashes.
func EffectiveHashes(spec *Spec) (BuiltinDefinitionHashes, error) {
	if spec == nil {
		return BuiltinDefinitionHashes{}, fmt.Errorf("effective evaluation spec is nil")
	}
	tasks := struct {
		Speed      SpeedSpec    `json:"speed"`
		CodeWrite  ExecSpec     `json:"code_write"`
		CodeFix    ExecSpec     `json:"code_fix"`
		Tools      ToolLoopSpec `json:"tools"`
		Agentic    ToolLoopSpec `json:"agentic"`
		Withdrawal ToolLoopSpec `json:"tool_withdrawal"`
		Refusal    RefusalSpec  `json:"refusal"`
		Plumbing   PlumbingSpec `json:"tool_plumbing"`
		Checks     []CheckSpec  `json:"checks"`
	}{
		Speed: spec.Speed, CodeWrite: spec.CodeWrite, CodeFix: spec.CodeFix,
		Tools: spec.Tools, Agentic: spec.Agentic, Withdrawal: spec.Withdrawal,
		Refusal: spec.Refusal, Plumbing: spec.Plumbing, Checks: spec.Checks,
	}
	taskBytes, err := json.Marshal(tasks)
	if err != nil {
		return BuiltinDefinitionHashes{}, fmt.Errorf("encode effective task set: %w", err)
	}
	versionBytes, err := json.Marshal(spec.Version)
	if err != nil {
		return BuiltinDefinitionHashes{}, fmt.Errorf("encode effective spec version: %w", err)
	}
	taskHash := framedDigest("fitr.effective-task-set.v1", taskBytes)
	specHash := framedDigest("fitr.effective-spec.v1", taskBytes, versionBytes)
	return BuiltinDefinitionHashes{TaskSetSHA256: taskHash, SpecSHA256: specHash}, nil
}

func framedDigest(domain string, parts ...[]byte) string {
	h := sha256.New()
	writeFramed(h, []byte(domain))
	for _, part := range parts {
		writeFramed(h, part)
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

func hashBuiltinCorpus(fsys fs.FS) (BuiltinDefinitionHashes, error) {
	var taskFiles, specFiles []string
	err := fs.WalkDir(fsys, "tasks", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(name, ".json") {
			return nil
		}
		specFiles = append(specFiles, name)
		if name != "tasks/version.json" {
			taskFiles = append(taskFiles, name)
		}
		return nil
	})
	if err != nil {
		return BuiltinDefinitionHashes{}, fmt.Errorf("enumerate embedded task definitions: %w", err)
	}
	if len(taskFiles) == 0 || len(specFiles) == 0 {
		return BuiltinDefinitionHashes{}, fmt.Errorf("embedded task definition corpus is empty")
	}
	sort.Strings(taskFiles)
	sort.Strings(specFiles)
	taskHash, err := hashCorpus(fsys, "fitr.task-set.v1", taskFiles)
	if err != nil {
		return BuiltinDefinitionHashes{}, err
	}
	specHash, err := hashCorpus(fsys, "fitr.eval-spec.v1", specFiles)
	if err != nil {
		return BuiltinDefinitionHashes{}, err
	}
	return BuiltinDefinitionHashes{TaskSetSHA256: taskHash, SpecSHA256: specHash}, nil
}

func hashCorpus(fsys fs.FS, domain string, names []string) (string, error) {
	h := sha256.New()
	writeFramed(h, []byte(domain))
	for _, name := range names {
		b, err := fs.ReadFile(fsys, name)
		if err != nil {
			return "", fmt.Errorf("hash embedded definition %s: %w", name, err)
		}
		writeFramed(h, []byte(name))
		writeFramed(h, b)
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

type binaryWriter interface {
	Write([]byte) (int, error)
}

func writeFramed(w binaryWriter, b []byte) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(b)))
	_, _ = w.Write(length[:])
	_, _ = w.Write(b)
}
