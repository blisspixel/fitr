// Package record defines fitr's persisted measurement record and local store.
package record

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
	SchemaVersion int    `json:"schema_version"`
	RunID         string `json:"run_id,omitempty"`
	Model         string `json:"model"`
	StartedAt     string `json:"started_at"`
	Level         string `json:"level"`
	// SeedSet names the instance set the generated checks were drawn from.
	// Unique per run by default; pinned seed sets enable paired comparison.
	SeedSet     string             `json:"seedset,omitempty"`
	Repeats     int                `json:"repeats"`
	NumCtx      int                `json:"num_ctx,omitempty"`
	WallSeconds float64            `json:"wall_s"`
	Device      device.Fingerprint `json:"device"`
	DeviceKey   string             `json:"device_key"`
	Profile     string             `json:"profile"`
	ModelMeta   ollama.ModelInfo   `json:"model_meta"`

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
	Contamination []string `json:"contamination,omitempty"`

	Scorecard score.Scorecard `json:"scorecard"`
}

var validRunID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{7,127}$`)

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
	copy := *r
	copy.RunID = ""
	b, err := json.Marshal(copy)
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
