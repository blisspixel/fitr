// Package record defines fitr's persisted measurement record and local store.
package record

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"

	"github.com/blisspixel/fitr/internal/device"
	"github.com/blisspixel/fitr/internal/eval"
	"github.com/blisspixel/fitr/internal/ollama"
	"github.com/blisspixel/fitr/internal/score"
	"github.com/blisspixel/fitr/internal/stats"
)

// Record is the complete persisted result of one fitr measurement.
//
// RunID was added after the original result schema. It is optional on disk so
// legacy records remain valid. Store assigns a deterministic ID before saving,
// and loaders derive the same kind of ID in memory for legacy records.
type Record struct {
	SchemaVersion   int                `json:"schema_version"`
	RunID           string             `json:"run_id,omitempty"`
	Manifest        *RunManifest       `json:"manifest,omitempty"`
	Completion      *CompletionReceipt `json:"completion,omitempty"`
	Model           string             `json:"model"`
	StartedAt       string             `json:"started_at"`
	Level           string             `json:"level"`
	ExecutionPolicy string             `json:"execution_policy,omitempty"`
	TaskPlan        TaskPlan           `json:"task_plan,omitempty"`
	// SeedSet names the instance set the generated checks were drawn from.
	// Unique per run by default; pinned seed sets enable paired comparison.
	SeedSet     string                `json:"seedset,omitempty"`
	Repeats     int                   `json:"repeats"`
	NumCtx      int                   `json:"num_ctx,omitempty"`
	WallSeconds float64               `json:"wall_s"`
	Device      device.Fingerprint    `json:"device"`
	DeviceV2    *device.FingerprintV2 `json:"device_fingerprint_v2,omitempty"`
	DeviceKey   string                `json:"device_key"`
	Profile     string                `json:"profile"`
	ModelMeta   ollama.ModelInfo      `json:"model_meta"`

	Speed      []eval.SpeedResult             `json:"speed_repeats"`
	DecodeSum  stats.Summary                  `json:"decode_summary"`
	TTFTSum    stats.Summary                  `json:"ttft_summary"`
	PrefillSum stats.Summary                  `json:"prefill_summary"`
	Memory     eval.MemoryResult              `json:"memory"`
	CodeWrite  []eval.ExecResult              `json:"code_write"`
	CodeFix    []eval.ExecResult              `json:"code_fix"`
	Checks     []eval.CheckOutcome            `json:"checks,omitempty"`
	Tools      []eval.ToolLoopResult          `json:"tools"`
	Withdrawal *eval.ToolLoopResult           `json:"tool_withdrawal,omitempty"`
	Agentic    *eval.ToolLoopResult           `json:"agentic,omitempty"`
	Refusal    map[string]eval.RefusalVerdict `json:"refusal,omitempty"`
	Refused    int                            `json:"refused_count"`
	Plumbing   *eval.PlumbingResult           `json:"plumbing,omitempty"`
	Rep        score.Repetition               `json:"repetition"`
	Density    score.Density                  `json:"density"`

	// Contamination lists models that refused to unload. A non-empty value
	// means every timing in this result is suspect.
	Contamination     []string                      `json:"contamination,omitempty"`
	EvidenceCounts    map[string]eval.OutcomeCounts `json:"evidence_counts,omitempty"`
	AdaptiveDecisions []eval.AdaptiveDecision       `json:"adaptive_decisions,omitempty"`

	Scorecard score.Scorecard `json:"scorecard"`

	completionPrivateKey  ed25519.PrivateKey
	storageIntegrityIssue string
}

var validRunID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{7,127}$`)

const EvidenceSchemaVersion = 6

// ContextSize returns the measured request context. It understands the legacy
// device-key suffix used before num_ctx became a first-class result field.
func (r *Record) ContextSize() int {
	if r == nil {
		return eval.NumCtx
	}
	if r.NumCtx > 0 {
		return r.NumCtx
	}
	if n := eval.ParseKeyCtx(r.DeviceKey); n > 0 {
		return n
	}
	return eval.NumCtx
}

// ComparableDeviceKey returns the key a board or comparison may use. Current
// v2 runs require a verified effective context. Legacy records retain their
// historical key for display compatibility, subject to their separate
// evidence-integrity exclusion.
func (r *Record) ComparableDeviceKey() (string, error) {
	if r == nil {
		return "", errors.New("result is unavailable")
	}
	if r.DeviceV2 != nil {
		return r.DeviceV2.ComparabilityKey()
	}
	if r.SchemaVersion >= EvidenceSchemaVersion {
		return "", errors.New("current result is missing fingerprint v2")
	}
	if r.DeviceKey == "" {
		return "", errors.New("result has no device key")
	}
	return r.DeviceKey, nil
}

// StableRunID returns the stored ID when it is valid, otherwise it derives a
// deterministic ID from the record content. The content-derived fallback lets
// a canonical legacy file and an archived copy deduplicate without migration.
func (r *Record) StableRunID() string {
	if r == nil {
		return ""
	}
	if validRunID.MatchString(r.RunID) {
		return r.RunID
	}
	dup := *r
	dup.RunID = ""
	b, err := json.Marshal(dup)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:12])
}

// EnsureRunID assigns StableRunID to a record that does not already carry a
// valid ID. Store calls this before writing either representation.
func (r *Record) EnsureRunID() string {
	if r == nil {
		return ""
	}
	if !validRunID.MatchString(r.RunID) {
		r.RunID = r.StableRunID()
	}
	return r.RunID
}

// ValidateEvidenceContract enforces the current denominator and execution
// invariants before a completed result is stored or consumed.
func (r *Record) ValidateEvidenceContract() error {
	if r == nil {
		return errors.New("nil result")
	}
	if r.SchemaVersion < EvidenceSchemaVersion {
		return nil
	}
	if err := r.validateCurrentEvidenceHeader(); err != nil {
		return err
	}
	if err := r.validateCompletedEvidence(); err != nil {
		return err
	}
	if err := r.validateTerminationEvidence(); err != nil {
		return err
	}
	if err := r.validateEvidenceCountCompleteness(); err != nil {
		return err
	}
	return r.validateExecutableEvidenceIsolation()
}

func (r *Record) validateCurrentEvidenceHeader() error {
	if r.SchemaVersion != EvidenceSchemaVersion {
		return fmt.Errorf("unsupported result schema %d", r.SchemaVersion)
	}
	if r.Manifest == nil {
		return fmt.Errorf("schema %d result has no sealed run manifest", r.SchemaVersion)
	}
	if r.Manifest.Schema != RunManifestSchema {
		return fmt.Errorf("schema %d result has no reproducibility manifest", r.SchemaVersion)
	}
	if err := r.ValidateManifest(); err != nil {
		return err
	}
	if err := r.TaskPlan.Validate(); err != nil {
		return fmt.Errorf("task plan: %w", err)
	}
	if r.TaskPlan.AdaptiveChecks || len(r.AdaptiveDecisions) != 0 {
		return errors.New("current evidence requires a fixed generated-check plan")
	}
	if r.TaskPlan.Memory && r.Memory.RequestedCtx <= 0 {
		return errors.New("planned memory probe has no requested context")
	}
	if !r.TaskPlan.Memory && r.Memory != (eval.MemoryResult{}) {
		return errors.New("unplanned memory probe contains evidence")
	}
	if err := r.Memory.ValidateReceipt(); err != nil {
		return fmt.Errorf("memory receipt: %w", err)
	}
	return nil
}

func (r *Record) validateTerminationEvidence() error {
	for i, result := range r.Tools {
		if err := result.ValidateTerminationEvidence(); err != nil {
			return fmt.Errorf("tool result %d: %w", i, err)
		}
	}
	for _, result := range []struct {
		name  string
		value *eval.ToolLoopResult
	}{{"withdrawal", r.Withdrawal}, {"agentic", r.Agentic}} {
		if result.value != nil {
			if err := result.value.ValidateTerminationEvidence(); err != nil {
				return fmt.Errorf("%s result: %w", result.name, err)
			}
		}
	}
	return nil
}

func (r *Record) validateEvidenceCountCompleteness() error {
	expected := []struct {
		phase string
		count int
	}{
		{phase: "coding", count: r.TaskPlan.CodeTrials},
		{phase: "checks", count: r.TaskPlan.CheckTrialsLimit},
		{phase: "tools", count: r.TaskPlan.ToolTrials},
		{phase: "refusal", count: r.TaskPlan.RefusalTrials},
		{phase: "plumbing", count: boolCount(r.TaskPlan.Plumbing)},
		{phase: "withdrawal", count: boolCount(r.TaskPlan.Withdrawal)},
		{phase: "agentic", count: r.TaskPlan.AgenticTrials},
	}
	for _, requirement := range expected {
		phase, want := requirement.phase, requirement.count
		counts, ok := r.EvidenceCounts[phase]
		if !ok {
			return fmt.Errorf("missing %s evidence counts", phase)
		}
		if counts.Expected != want {
			return fmt.Errorf("%s evidence expected %d, manifest planned %d", phase, counts.Expected, want)
		}
		if !counts.Complete() {
			return fmt.Errorf("%s evidence counts are incomplete", phase)
		}
		if counts.Errors != 0 {
			return fmt.Errorf("%s contains %d infrastructure error outcome(s)", phase, counts.Errors)
		}
	}
	return nil
}

func (r *Record) validateExecutableEvidenceIsolation() error {
	for _, phase := range []string{"coding", "tools", "agentic"} {
		if counts := r.EvidenceCounts[phase]; counts.Scorable != 0 {
			return fmt.Errorf("%s executable observations cannot be scoreable before isolation", phase)
		}
	}
	return nil
}

// EvidenceIntegrityIssue returns a presentation-safe reason a result cannot
// participate in ranking claims. Legacy records remain readable but unverified.
func (r *Record) EvidenceIntegrityIssue() string {
	if r == nil {
		return "result is unavailable"
	}
	if r.SchemaVersion < EvidenceSchemaVersion {
		return fmt.Sprintf("legacy schema %d uses an earlier evidence contract and is display-only", r.SchemaVersion)
	}
	if err := r.ValidateEvidenceContract(); err != nil {
		return "invalid evidence contract: " + err.Error()
	}
	if r.storageIntegrityIssue != "" {
		return r.storageIntegrityIssue
	}
	if issue := r.Manifest.Model.RankingIssue(); issue != "" {
		return issue
	}
	return ""
}

func boolCount(value bool) int {
	if value {
		return 1
	}
	return 0
}
