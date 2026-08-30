package eval

import (
	"context"
	"testing"
	"time"
)

type llamaServerMemoryBackend struct {
	fakeBackend
	stopCalls int
}

func (b *llamaServerMemoryBackend) Name() string { return "llama-server" }

func (b *llamaServerMemoryBackend) StopAll(context.Context) ([]string, error) {
	b.stopCalls++
	return nil, nil
}

func TestRunMemorySkipsLlamaServerWithoutInference(t *testing.T) {
	b := &llamaServerMemoryBackend{}
	start := time.Now()
	result, err := RunMemory(context.Background(), b, "served.gguf", 32768)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != OutcomeSkipped || result.RequestedCtx != 32768 {
		t.Fatalf("result = %+v, want requested-32K SKIP", result)
	}
	if result.UnavailableReason != "llama-server does not report resident allocation bytes" {
		t.Fatalf("unavailable reason = %q", result.UnavailableReason)
	}
	if b.generateCalls != 0 || b.stopCalls != 0 {
		t.Fatalf("llama-server memory SKIP called Generate %d time(s) and StopAll %d time(s)", b.generateCalls, b.stopCalls)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("llama-server memory SKIP took %v; expected no cooldown", elapsed)
	}
}
