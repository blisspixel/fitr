package device

import "sort"

// KnobScope says when a serving setting takes effect.
// KnobStart is read when the serving process starts. A difference is a
// different configuration: the measurements on either side are not one
// run, and a fresh measurement has to come from a process that was
// started with the new value. fitr does not restart that process.
// KnobRequest is carried by the next request and is not that boundary.
//
// OLLAMA_FLASH_ATTENTION and OLLAMA_KV_CACHE_TYPE are the two the
// roadmap records as read only at server start. The other start names
// are serving keys this tree already copies from the server process
// environment or launch arguments. OLLAMA_MODELS is a directory path,
// not a measurement setting. The request names are the ones tune
// already treats as needing no restart.
type KnobScope string

const (
	KnobRequest KnobScope = "request"
	KnobStart   KnobScope = "start"
)

// ServingKnob is one setting whose scope is part of the comparison
// contract. Name is the recorded key, not a second spelling.
type ServingKnob struct {
	Name  string
	Scope KnobScope
	Note  string
}

// ServingKnobs returns the request and start-time settings in print
// order. Callers get a copy.
func ServingKnobs() []ServingKnob {
	return []ServingKnob{
		{Name: "num_ctx", Scope: KnobRequest, Note: "KV cache size; advise already names this. Also --ctx-size"},
		{Name: "num_batch", Scope: KnobRequest, Note: "prefill chunk; quality can move with it. Also -b"},
		{Name: "num_gpu", Scope: KnobRequest, Note: "offload; partial GPU is a RAM benchmark wearing a GPU badge. Also -ngl"},
		{Name: "OLLAMA_FLASH_ATTENTION", Scope: KnobStart, Note: "read at process start; some settings change quality"},
		{Name: "OLLAMA_KV_CACHE_TYPE", Scope: KnobStart, Note: "read at process start; f16, q8_0, and q4_0 are different configurations"},
		{Name: "OLLAMA_NUM_PARALLEL", Scope: KnobStart, Note: "read at process start; slots divide the context window"},
		{Name: "OLLAMA_MAX_LOADED_MODELS", Scope: KnobStart, Note: "read at process start; another resident model contaminates timings"},
		{Name: "OLLAMA_CONTEXT_LENGTH", Scope: KnobStart, Note: "server default context, read at process start; a request can still pass num_ctx"},
		{Name: "OLLAMA_IGPU_ENABLE", Scope: KnobStart, Note: "read at process start; selects the integrated GPU"},
		{Name: "LLAMA_ARG_FIT", Scope: KnobStart, Note: "llama-server launch argument; read when that process starts"},
	}
}

// StartBoundary returns the start-time keys whose observed values differ.
// Equal values, including both absent, are not a boundary. A request
// knob such as num_ctx is never returned.
func StartBoundary(left, right map[string]string) []string {
	var names []string
	for _, knob := range ServingKnobs() {
		if knob.Scope != KnobStart {
			continue
		}
		if left[knob.Name] != right[knob.Name] {
			names = append(names, knob.Name)
		}
	}
	sort.Strings(names)
	return names
}
