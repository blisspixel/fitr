package record

import (
	"errors"
	"fmt"
)

const (
	// RecurrentKVCachePerLoop and RecurrentKVCacheLastLoop are the only modes
	// that can be sealed. The spelling was checked against llama.cpp pull
	// 30306 head ab0ffa188bbc3be15ce8e5d3f1278ba5224f4358 on 2026-11-16, while
	// that pull was still unmerged. llama.cpp b12359 (commit b1b770d) does
	// not report a loop count. vLLM pull 60149 merge
	// 75fc05a53a366068f534a61ab3ee6b50485419f2 is contained in tag v0.33.0 and
	// is not in v0.32.0. fitr does not read a vLLM override unless this
	// observation was sealed.
	RecurrentKVCachePerLoop  = "per_loop"
	RecurrentKVCacheLastLoop = "last_loop"
	// maxServingLoopCount matches the architecture dimension ceiling. A larger
	// value is unmeasured, not clamped.
	maxServingLoopCount = 1 << 20
)

// ServingLoopText describes the serving loop identity. It is not a file
// default. A nil count and an empty mode both stay unobserved.
func ServingLoopText(count *int, mode string) string {
	countText := "serving loop count unobserved"
	if count != nil {
		countText = fmt.Sprintf("serving loop count %d", *count)
	}
	modeText := "serving recurrent KV cache mode unobserved"
	if mode != "" {
		modeText = "serving recurrent KV cache mode " + mode
	}
	return countText + "; " + modeText
}

// ObserveServingLoop stages a loop count or cache mode for the next manifest
// seal. The observation has to come from the runtime or from the arguments
// the run was started with. An empty observation is rejected so a caller
// cannot record "unobserved" as if it were a measurement. A second call is
// rejected so the sealed plan cannot be replaced before it is attached.
func (r *Record) ObserveServingLoop(count *int, mode string) error {
	if r == nil {
		return errors.New("cannot record a serving loop on a nil record")
	}
	if r.Manifest != nil {
		return errors.New("serving loop observation must be sealed with the run manifest")
	}
	if r.servingLoopSet {
		return errors.New("serving loop observation is already recorded")
	}
	if err := validServingLoop(count, mode); err != nil {
		return err
	}
	if count != nil {
		n := *count
		r.servingLoopCount = &n
	}
	r.servingLoopMode = mode
	r.servingLoopSet = true
	return nil
}

func validServingLoop(count *int, mode string) error {
	if count == nil && mode == "" {
		return errors.New("serving loop observation is empty")
	}
	if count != nil && (*count < 1 || *count > maxServingLoopCount) {
		return errors.New("serving loop count is unmeasured")
	}
	switch mode {
	case "", RecurrentKVCachePerLoop, RecurrentKVCacheLastLoop:
		return nil
	default:
		return errors.New("serving recurrent KV cache mode is unmeasured")
	}
}

func (m *RunManifest) applyServingLoop(count *int, mode string) error {
	if count == nil && mode == "" {
		return nil
	}
	if m.Schema != RunManifestSchema {
		return errors.New("legacy run manifest cannot contain a serving loop observation")
	}
	if count != nil {
		n := *count
		m.ServingLoopCount = &n
	}
	m.RecurrentKVCacheMode = mode
	return nil
}

func (m RunManifest) validateServingLoop() error {
	if m.Schema == LegacyRunManifestSchema && (m.ServingLoopCount != nil || m.RecurrentKVCacheMode != "") {
		return errors.New("legacy run manifest cannot contain a serving loop observation")
	}
	return validServingLoopObservation(m.ServingLoopCount, m.RecurrentKVCacheMode)
}

// validServingLoopObservation allows both fields to be empty. That is an
// unobserved manifest, not a rejected one.
func validServingLoopObservation(count *int, mode string) error {
	if count == nil && mode == "" {
		return nil
	}
	return validServingLoop(count, mode)
}
