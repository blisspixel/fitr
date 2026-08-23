package eval

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRunMemoryHonorsCancellationDuringCooldown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	_, err := RunMemory(ctx, &fakeBackend{}, "m", 8192)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context cancellation", err)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("cancelled memory check took %v", elapsed)
	}
}
