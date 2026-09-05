package ollama

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ContextRequestPolicy controls the requested treatment of an overfull prompt.
// It does not prove that a particular server honors the controls. A consumer
// must bind and validate the runtime separately before using these observations.
type ContextRequestPolicy string

const PreserveContextV1 ContextRequestPolicy = "ollama.no_truncate_no_shift.v1"

var (
	ErrContextRequestPolicy = errors.New("invalid context request policy")
	ErrContextAccounting    = errors.New("context token accounting unavailable or invalid")
	ErrContextReserve       = errors.New("full output reserve does not fit the operating window")
)

// ContextTokenAccounting retains presence from one terminal native response.
// Counts include the runtime template; they are not payload-only token counts.
// Missing or null fields remain unknown. This transient receipt is separate
// from legacy Metrics.CacheKnown, which controls existing benchmark replay.
type ContextTokenAccounting struct {
	PromptTokens       *int `json:"prompt_tokens,omitempty"`
	CachedPromptTokens *int `json:"cached_prompt_tokens,omitempty"`
	OutputTokens       *int `json:"output_tokens,omitempty"`
}

func (p ContextRequestPolicy) validate(s Sampling) error {
	if p == "" {
		return nil
	}
	if p != PreserveContextV1 || s.NumCtx <= 0 || s.NumPredict <= 0 || s.NumPredict >= s.NumCtx {
		return ErrContextRequestPolicy
	}
	return nil
}

func (p ContextRequestPolicy) apply(payload map[string]any) {
	if p == PreserveContextV1 {
		payload["truncate"] = false
		payload["shift"] = false
	}
}

// encoding/json accepts case-insensitive struct-field aliases. An opt-in
// receipt must not let a differently capitalized field replace an earlier
// count or terminal marker. Exact duplicates are rejected by strictjson.
func (p ContextRequestPolicy) validateFrameKeys(frame []byte) error {
	if p == "" {
		return nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(frame, &fields); err != nil {
		return fmt.Errorf("%w: invalid response object", ErrContextAccounting)
	}
	for key := range fields {
		for _, name := range []string{
			"response", "message", "remote_model", "remote_host", "done", "done_reason",
			"eval_count", "eval_duration", "prompt_eval_count", "prompt_eval_cached_count",
			"prompt_eval_duration", "load_duration",
		} {
			if key != name && strings.EqualFold(key, name) {
				return fmt.Errorf("%w: noncanonical response field", ErrContextAccounting)
			}
		}
	}
	return nil
}

func nativeCount(n *int) int {
	if n == nil {
		return 0
	}
	return *n
}

func (p ContextRequestPolicy) accounting(prompt, output *int, cached json.RawMessage, outputCap int) (*ContextTokenAccounting, error) {
	if p == "" {
		return nil, nil
	}
	a := &ContextTokenAccounting{PromptTokens: prompt, OutputTokens: output}
	if len(cached) != 0 {
		if err := json.Unmarshal(cached, &a.CachedPromptTokens); err != nil {
			return nil, fmt.Errorf("%w: invalid cached count", ErrContextAccounting)
		}
	}
	if err := a.validate(outputCap, false); err != nil {
		return nil, err
	}
	return a, nil
}

func (a *ContextTokenAccounting) validate(outputCap int, requireAll bool) error {
	if a == nil || outputCap <= 0 {
		return ErrContextAccounting
	}
	if requireAll && (a.PromptTokens == nil || a.CachedPromptTokens == nil || a.OutputTokens == nil) {
		return ErrContextAccounting
	}
	for _, value := range []*int{a.PromptTokens, a.CachedPromptTokens, a.OutputTokens} {
		if value != nil && *value < 0 {
			return ErrContextAccounting
		}
	}
	if a.PromptTokens != nil && a.CachedPromptTokens != nil && *a.CachedPromptTokens > *a.PromptTokens {
		return ErrContextAccounting
	}
	if a.OutputTokens != nil && *a.OutputTokens > outputCap {
		return ErrContextAccounting
	}
	return nil
}

// CheckReserve checks a complete native receipt against the entire declared
// reserve, not just the observed short answer. Success proves arithmetic only;
// runtime identity, placement, window and nontruncation still need validation.
func (a *ContextTokenAccounting) CheckReserve(window, reserve int) error {
	if window <= 0 || reserve <= 0 || reserve >= window {
		return ErrContextRequestPolicy
	}
	if err := a.validate(reserve, true); err != nil {
		return err
	}
	// Subtraction avoids overflow when a server reports an enormous count.
	if *a.PromptTokens > window-reserve {
		return ErrContextReserve
	}
	return nil
}

// NewlyEvaluatedPromptTokens derives the uncached part only when the complete
// receipt is consistent. No tokenization or cache-miss estimate is performed.
func (a *ContextTokenAccounting) NewlyEvaluatedPromptTokens(outputCap int) (int, error) {
	if err := a.validate(outputCap, true); err != nil {
		return 0, err
	}
	return *a.PromptTokens - *a.CachedPromptTokens, nil
}
