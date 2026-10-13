package device

import "fmt"

const (
	// BoundaryProbeCeiling is the largest declared window that gets a live
	// overflow probe. Above it, the prompt itself is large enough to dominate
	// the run, so the runtime report stays and the record says it was not probed.
	BoundaryProbeCeiling = 8192
	// BoundaryProbeMargin is how many extra copies of the probe word are sent
	// past the declared window. The runtime's own token count, not this count,
	// decides whether the probe reached the window.
	BoundaryProbeMargin = 64
)

// BoundaryObservation is what one probe request produced. Unrecognized errors
// are not a window. Accepted is a generation that returned. Overflow is
// llama.cpp's typed exceed_context_size_error, already parsed by the caller.
type BoundaryObservation struct {
	Sent         bool
	Accepted     bool
	PromptTokens int
	OverflowNCtx int
	Overflow     bool
	Unrecognized bool
}

// BoundaryPrompt is the text sent to provoke a window refusal. ok is false
// when the declared window is outside the range this probe is allowed to send.
func BoundaryPrompt(declared int) (string, bool) {
	if declared < 1 || declared > BoundaryProbeCeiling {
		return "", false
	}
	n := declared + BoundaryProbeMargin
	// One short word per copy. A denser or sparser tokenizer changes the
	// runtime's prompt-token count, which the resolver compares. It does not
	// change this string into a token measurement.
	word := "tok "
	out := make([]byte, 0, n*len(word))
	for range n {
		out = append(out, word...)
	}
	if len(out) > 0 {
		out = out[:len(out)-1]
	}
	return string(out), true
}

// ResolveBoundary turns a probe observation into context evidence.
//
// A refusal whose per-slot n_ctx matches the declaration is a boundary probe.
// A different n_ctx is a contradiction: neither number enters the key.
// Acceptance above the declaration means the declaration understated the
// window, so the effective value is not set to the declaration. Acceptance
// below it is a short probe. Anything else leaves the runtime report alone.
func ResolveBoundary(receipt ContextVerification, obs BoundaryObservation) (ContextVerification, string) {
	declared := receipt.RequestedTokens
	if declared > BoundaryProbeCeiling {
		return receipt, "boundary probe not sent; requested context is above the probe ceiling"
	}
	if !obs.Sent || obs.Unrecognized || (!obs.Accepted && !obs.Overflow) {
		return receipt, ""
	}
	if obs.Overflow {
		if obs.OverflowNCtx == declared && obs.OverflowNCtx >= 1 {
			n := obs.OverflowNCtx
			receipt.EffectiveTokens = &n
			receipt.EffectiveSource = ContextSourceBoundaryProbe
			return receipt, ""
		}
		receipt.EffectiveTokens = nil
		receipt.EffectiveSource = ""
		return receipt, fmt.Sprintf(
			"boundary probe reported a %d-token window against the declared %d; neither enters the comparability key",
			obs.OverflowNCtx, declared)
	}
	if obs.PromptTokens > declared {
		if receipt.EffectiveTokens != nil && *receipt.EffectiveTokens == declared {
			receipt.EffectiveTokens = nil
			receipt.EffectiveSource = ""
		}
		return receipt, fmt.Sprintf(
			"boundary probe accepted %d prompt tokens, above the declared %d-token window; the declaration understated the window",
			obs.PromptTokens, declared)
	}
	if obs.PromptTokens > 0 && obs.PromptTokens < declared {
		return receipt, "boundary probe was shorter than the declared window; tokenizer density is not a smaller window"
	}
	return receipt, ""
}
