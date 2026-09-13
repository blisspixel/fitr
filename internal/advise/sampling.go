package advise

import (
	"fmt"
	"strings"
)

// AuthorSampling is the sampler configuration the artifact's author stored in
// the file, under the general.sampling.* keys llama.cpp defines.
//
// It matters because nothing else carries it. The same file is served with
// different defaults by different runtimes, and none of those defaults is the
// author's, so a user asking "what settings should I use" is otherwise reading
// a model card by hand or accepting whichever runtime they happened to install.
//
// Every field is optional because an absent key is an absent declaration, not
// a zero. Most artifacts today declare nothing at all, and saying so is the
// honest answer rather than printing a runtime's default as though the author
// had chosen it.
type AuthorSampling struct {
	Sequence       string   `json:"sequence,omitempty"`
	Temp           *float64 `json:"temp,omitempty"`
	TopK           *int     `json:"top_k,omitempty"`
	TopP           *float64 `json:"top_p,omitempty"`
	MinP           *float64 `json:"min_p,omitempty"`
	PenaltyRepeat  *float64 `json:"penalty_repeat,omitempty"`
	PenaltyLastN   *int     `json:"penalty_last_n,omitempty"`
	XTCProbability *float64 `json:"xtc_probability,omitempty"`
	XTCThreshold   *float64 `json:"xtc_threshold,omitempty"`
	Mirostat       *int     `json:"mirostat,omitempty"`
	MirostatTau    *float64 `json:"mirostat_tau,omitempty"`
	MirostatEta    *float64 `json:"mirostat_eta,omitempty"`
}

// Declared reports whether the artifact carries any author sampling at all.
func (s *AuthorSampling) Declared() bool {
	if s == nil {
		return false
	}
	return s.Sequence != "" || s.Temp != nil || s.TopK != nil || s.TopP != nil ||
		s.MinP != nil || s.PenaltyRepeat != nil || s.PenaltyLastN != nil ||
		s.XTCProbability != nil || s.XTCThreshold != nil || s.Mirostat != nil ||
		s.MirostatTau != nil || s.MirostatEta != nil
}

// AuthorSamplingFromKVs reads the author's declared sampler settings.
//
// The key names are Keys.General.SAMPLING_* in llama.cpp's
// gguf-py/gguf/constants.py, checked against master on 2026-09-12. They are
// llama.cpp's own extension and are not in the GGUF specification, so a
// spec-conformant reader will not find them and a runtime that does not
// implement them will silently use its own defaults instead.
func AuthorSamplingFromKVs(kvs map[string]any) *AuthorSampling {
	s := &AuthorSampling{}
	if seq, ok := kvs["general.sampling.sequence"].(string); ok {
		s.Sequence = seq
	}
	s.Temp = optionalFloat(kvs, "general.sampling.temp")
	s.TopP = optionalFloat(kvs, "general.sampling.top_p")
	s.MinP = optionalFloat(kvs, "general.sampling.min_p")
	s.PenaltyRepeat = optionalFloat(kvs, "general.sampling.penalty_repeat")
	s.XTCProbability = optionalFloat(kvs, "general.sampling.xtc_probability")
	s.XTCThreshold = optionalFloat(kvs, "general.sampling.xtc_threshold")
	s.MirostatTau = optionalFloat(kvs, "general.sampling.mirostat_tau")
	s.MirostatEta = optionalFloat(kvs, "general.sampling.mirostat_eta")
	s.TopK = optionalInt(kvs, "general.sampling.top_k")
	s.PenaltyLastN = optionalInt(kvs, "general.sampling.penalty_last_n")
	s.Mirostat = optionalInt(kvs, "general.sampling.mirostat")
	if !s.Declared() {
		return nil
	}
	return s
}

// optionalFloat keeps an absent key absent. A missing sampler setting is a
// declaration the author did not make, which is different from zero: a
// temperature of zero is greedy decoding and a real choice.
func optionalFloat(kvs map[string]any, key string) *float64 {
	raw, ok := kvs[key]
	if !ok || raw == nil {
		return nil
	}
	switch n := raw.(type) {
	case float64:
		return &n
	case float32:
		v := float64(n)
		return &v
	}
	return nil
}

func optionalInt(kvs map[string]any, key string) *int {
	raw, ok := kvs[key]
	if !ok || raw == nil {
		return nil
	}
	if n := asInt64(raw); n != 0 || isExplicitZero(raw) {
		v := int(n)
		return &v
	}
	return nil
}

// isExplicitZero separates a declared zero from a value asInt64 could not read.
func isExplicitZero(raw any) bool {
	switch n := raw.(type) {
	case int:
		return n == 0
	case int32:
		return n == 0
	case int64:
		return n == 0
	case uint32:
		return n == 0
	case uint64:
		return n == 0
	}
	return false
}

// Summary is a one-line rendering of what the author declared, or empty when
// the artifact declares nothing. The runtime's own default is deliberately not
// substituted: the point of this line is that these values came from the file.
func (s *AuthorSampling) Summary() string {
	if !s.Declared() {
		return ""
	}
	var parts []string
	addFloat := func(name string, v *float64) {
		if v != nil {
			parts = append(parts, fmt.Sprintf("%s=%g", name, *v))
		}
	}
	addInt := func(name string, v *int) {
		if v != nil {
			parts = append(parts, fmt.Sprintf("%s=%d", name, *v))
		}
	}
	addFloat("temp", s.Temp)
	addInt("top_k", s.TopK)
	addFloat("top_p", s.TopP)
	addFloat("min_p", s.MinP)
	addFloat("repeat_penalty", s.PenaltyRepeat)
	addInt("penalty_last_n", s.PenaltyLastN)
	addInt("mirostat", s.Mirostat)
	if s.Sequence != "" {
		parts = append(parts, "sequence="+s.Sequence)
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " ") + " (declared by the artifact; a runtime " +
		"that does not read these keys will use its own defaults instead)"
}
