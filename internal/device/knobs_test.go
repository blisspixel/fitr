package device

import (
	"reflect"
	"testing"
)

func TestStartBoundaryIsAProcessChange(t *testing.T) {
	left := map[string]string{"OLLAMA_KV_CACHE_TYPE": "f16", "OLLAMA_FLASH_ATTENTION": "1"}
	right := map[string]string{"OLLAMA_KV_CACHE_TYPE": "q8_0", "OLLAMA_FLASH_ATTENTION": "1"}
	if got := StartBoundary(left, right); !reflect.DeepEqual(got, []string{"OLLAMA_KV_CACHE_TYPE"}) {
		t.Fatalf("boundary = %v", got)
	}
	if StartBoundary(left, left) != nil {
		t.Fatal("equal start knobs were a boundary")
	}
	if got := StartBoundary(
		map[string]string{"OLLAMA_MODELS": "a"},
		map[string]string{"OLLAMA_MODELS": "b"},
	); len(got) != 0 {
		t.Fatalf("model directory was a measurement boundary: %v", got)
	}
	classified := map[string]bool{}
	for _, knob := range ServingKnobs() {
		if knob.Name == "num_ctx" && knob.Scope != KnobRequest {
			t.Fatal("num_ctx requires a process restart")
		}
		if knob.Scope == KnobStart {
			classified[knob.Name] = true
		}
	}
	for _, key := range configKeys {
		if key == "OLLAMA_MODELS" {
			if classified[key] {
				t.Fatal("OLLAMA_MODELS was classified as a measurement restart")
			}
			continue
		}
		if !classified[key] {
			t.Fatalf("serving key %s has no start-time contract", key)
		}
	}
}
