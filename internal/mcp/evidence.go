package mcp

import (
	"context"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/blisspixel/fitr/internal/record"
	"github.com/blisspixel/fitr/internal/role"
)

// These narrower limits bound repeated immutable-history reconciliation in
// role.Review. The ordinary CLI remains available for larger evidence stores.
const (
	maxDirectoryEntries   = 512
	maxEvidenceFileBytes  = 4 << 20
	maxEvidenceTotalBytes = 16 << 20
)

type localEvidence struct {
	root string
	mu   sync.Mutex
}

func newLocalEvidence(dir string) (*localEvidence, error) {
	if strings.TrimSpace(dir) == "" || strings.HasPrefix(dir, `\\`) || strings.HasPrefix(dir, "//") {
		return nil, errors.New("result root is required")
	}
	absolute, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err == nil {
		absolute = resolved
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return &localEvidence{root: absolute}, nil
}

type roleSummary struct {
	Name           string `json:"name"`
	Revision       string `json:"revision"`
	CandidateCount int    `json:"candidate_count"`
}

type roleList struct {
	Schema string        `json:"schema"`
	Roles  []roleSummary `json:"roles"`
}

func (source *localEvidence) list(ctx context.Context) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := source.checkRoles(); err != nil {
		return nil, err
	}
	libraries, err := (role.Store{Dir: filepath.Join(source.root, ".roles")}).List()
	if err != nil {
		return nil, err
	}
	result := roleList{Schema: "fitr.mcp.roles.v1", Roles: []roleSummary{}}
	for _, library := range libraries {
		result.Roles = append(result.Roles, roleSummary{Name: library.Name, Revision: library.CurrentRevision, CandidateCount: len(library.Candidates)})
	}
	return result, ctx.Err()
}

type preferenceSummary struct {
	Estimate float64 `json:"estimate"`
	Low      float64 `json:"low"`
	High     float64 `json:"high"`
}

type candidateSummary struct {
	EvidenceSHA256 string             `json:"evidence_sha256"`
	State          string             `json:"state"`
	ReasonCount    int                `json:"reason_count"`
	Preference     *preferenceSummary `json:"preference,omitempty"`
}

type reviewSummary struct {
	Schema             string             `json:"schema"`
	Role               string             `json:"role"`
	Revision           string             `json:"revision"`
	Scope              string             `json:"scope"`
	State              string             `json:"state"`
	EvaluatedAt        string             `json:"evaluated_at"`
	Candidates         []candidateSummary `json:"candidates"`
	ExplorationLead    string             `json:"exploration_lead,omitempty"`
	GapCount           int                `json:"gap_count"`
	ComparisonReady    bool               `json:"comparison_ready"`
	AdoptionAuthorized bool               `json:"adoption_authorized"`
}

func (source *localEvidence) review(ctx context.Context, name string) (any, error) {
	if !source.mu.TryLock() {
		return nil, errors.New("evidence review already running")
	}
	defer source.mu.Unlock()
	if !roleNamePattern.MatchString(name) {
		return nil, errors.New("invalid role name")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := source.checkRoles(); err != nil {
		return nil, err
	}
	library, err := (role.Store{Dir: filepath.Join(source.root, ".roles")}).Load(name)
	if err != nil {
		return nil, err
	}
	if err := source.checkEvidence(library); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	report, err := role.Review(library, record.Store{Dir: source.root}, time.Now())
	if err != nil {
		return nil, err
	}
	return summarizeReview(report), ctx.Err()
}

func summarizeReview(report role.ReviewReport) reviewSummary {
	result := reviewSummary{Schema: "fitr.mcp.review.v1", Role: report.Role, Revision: report.Revision,
		Scope: "battery_screening", State: report.State, EvaluatedAt: report.EvaluatedAt,
		Candidates: []candidateSummary{}, ExplorationLead: report.Lead, GapCount: len(report.Gaps),
		ComparisonReady: report.Comparison != nil && report.Comparison.Ready}
	for _, candidate := range report.Candidates {
		entry := candidateSummary{EvidenceSHA256: candidate.ID, State: candidate.State, ReasonCount: len(candidate.Reasons)}
		if candidate.Preference != nil {
			entry.Preference = &preferenceSummary{Estimate: clampUtility(candidate.Preference.Estimate), Low: clampUtility(candidate.Preference.Low), High: clampUtility(candidate.Preference.High)}
		}
		result.Candidates = append(result.Candidates, entry)
	}
	return result
}

// Normalized weighted sums may cross an endpoint by floating-point roundoff.
// Clamp only the displayed utility, after role selection has already finished.
func clampUtility(value float64) float64 { return math.Max(0, math.Min(1, value)) }

func (source *localEvidence) checkRoles() error {
	if err := checkedDirectory(source.root); err != nil {
		return err
	}
	_, err := boundedDirectory(filepath.Join(source.root, ".roles"), 1<<20)
	return err
}

func (source *localEvidence) checkEvidence(library role.Library) error {
	total, err := boundedDirectory(filepath.Join(source.root, ".history"), maxEvidenceFileBytes)
	if err != nil {
		return err
	}
	for _, attachment := range library.Candidates {
		if !samePath(filepath.Dir(attachment.Path), source.root) || !strings.HasSuffix(attachment.Path, ".json") {
			return errors.New("attachment is outside the configured canonical result directory")
		}
		size, err := checkedFile(attachment.Path, maxEvidenceFileBytes)
		if err != nil {
			return err
		}
		total += size
		if total > maxEvidenceTotalBytes {
			return errors.New("evidence exceeds the read-only profile limit")
		}
	}
	return nil
}

func samePath(first, second string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(first, second)
	}
	return first == second
}

func checkedDirectory(path string) error {
	resolved, err := filepath.EvalSymlinks(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !samePath(resolved, path) {
		return errors.New("evidence directories cannot redirect through symbolic links")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("evidence root must be a regular directory")
	}
	return nil
}

func checkedFile(path string, maximum int64) (int64, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if !info.Mode().IsRegular() || info.Size() > maximum {
		return 0, errors.New("evidence file is not regular or exceeds its limit")
	}
	return info.Size(), nil
}

func boundedDirectory(path string, maximum int64) (int64, error) {
	if err := checkedDirectory(path); err != nil {
		return 0, err
	}
	directory, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer directory.Close()
	entries, err := directory.ReadDir(maxDirectoryEntries + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return 0, err
	}
	if len(entries) > maxDirectoryEntries {
		return 0, errors.New("evidence directory exceeds its entry limit")
	}
	var total int64
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		size, err := checkedFile(filepath.Join(path, entry.Name()), maximum)
		if err != nil {
			return 0, err
		}
		total += size
		if total > maxEvidenceTotalBytes {
			return 0, errors.New("evidence directory exceeds its size limit")
		}
	}
	return total, nil
}
