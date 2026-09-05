package eval

import (
	"context"
	"testing"
	"time"

	"github.com/blisspixel/fitr/internal/ollama"
)

type memoryTimingBackend struct {
	fakeBackend
	metrics ollama.Metrics
}

func (b *memoryTimingBackend) Generate(context.Context, string, string, ollama.Sampling) (string, ollama.Metrics, error) {
	time.Sleep(30 * time.Millisecond)
	return "ok", b.metrics, nil
}

func TestMemoryLoadTimingUsesInferenceIntervalWhenKnown(t *testing.T) {
	for _, tc := range []struct {
		name    string
		metrics ollama.Metrics
		want    float64
	}{
		{"measured inference", ollama.Metrics{InferenceElapsedKnown: true, InferenceElapsed: 1250 * time.Millisecond}, 1.25},
		{"measured zero", ollama.Metrics{InferenceElapsedKnown: true}, 0},
		{"ordinary stopwatch", ollama.Metrics{InferenceElapsed: time.Hour}, -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			backend := &memoryTimingBackend{metrics: tc.metrics}
			result, err := RunMemory(t.Context(), backend, "candidate", 4096)
			if err != nil {
				t.Fatal(err)
			}
			if tc.want >= 0 && result.LoadS != tc.want {
				t.Fatalf("load=%v want=%v", result.LoadS, tc.want)
			}
			if tc.want < 0 && result.LoadS < 0.02 {
				t.Fatalf("ordinary client stopped measuring caller wall time: %v", result.LoadS)
			}
		})
	}
}
