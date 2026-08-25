package main

import (
	"context"
	"testing"

	"github.com/blisspixel/fitr/internal/device"
	"github.com/blisspixel/fitr/internal/ollama"
)

// archBackend serves architecture metadata the way Ollama's /api/show does.
type archBackend struct {
	*runIntegrationBackend
	kvs  map[string]any
	size int64
}

func (b *archBackend) Name() string { return "ollama" }
func (b *archBackend) Show(context.Context, string) (ollama.ModelInfo, error) {
	return ollama.ModelInfo{Name: "model", Info: b.kvs, Size: b.size}, nil
}

func llama8BKVs() map[string]any {
	return map[string]any{
		"general.architecture":          "llama",
		"llama.block_count":             uint64(32),
		"llama.attention.head_count":    uint64(32),
		"llama.attention.head_count_kv": uint64(8),
		"llama.attention.key_length":    uint64(128),
		"llama.embedding_length":        uint64(4096),
		"llama.context_length":          uint64(131072),
	}
}

// `fitr run` defaults to 8192 so that models measured with defaults stay
// comparable, which is correct. It is also often far below what the card can
// hold, and advise already solves for the real number. Throwing that away left
// anyone who skipped advise measuring a narrow window with no idea a wider one
// was available.
func TestRunPointsAtAWiderWindowWhenOneFits(t *testing.T) {
	ctx := context.Background()
	fp := device.Fingerprint{VRAMGb: 24, VRAMSource: "nvidia-smi", Config: map[string]string{}}

	// 5 GiB of weights on 24 GB: the whole 131072 window fits.
	roomy := &archBackend{runIntegrationBackend: &runIntegrationBackend{}, kvs: llama8BKVs(), size: 5 << 30}
	if got := largerFittingContext(ctx, roomy, "model", fp, 8192); got != 131072 {
		t.Fatalf("suggested %d, want the full 131072 window", got)
	}

	// Measured at the full window already: nothing better to say.
	if got := largerFittingContext(ctx, roomy, "model", fp, 131072); got != 0 {
		t.Fatalf("suggested %d when already at the maximum", got)
	}

	// 20 GiB of weights: the full window does not fit, so the suggestion must
	// be the largest that does, and it must be smaller than the architecture
	// maximum or it would not fit at all.
	tight := &archBackend{runIntegrationBackend: &runIntegrationBackend{}, kvs: llama8BKVs(), size: 20 << 30}
	got := largerFittingContext(ctx, tight, "model", fp, 8192)
	if got <= 8192 || got >= 131072 {
		t.Fatalf("suggested %d; want a window between the default and the architecture max", got)
	}
}

// A suggestion that does not fit is worse than no suggestion, so every
// uncertain input returns nothing.
func TestContextHintStaysQuietWhenItCannotBeSure(t *testing.T) {
	ctx := context.Background()
	full := device.Fingerprint{VRAMGb: 24, VRAMSource: "nvidia-smi", Config: map[string]string{}}
	roomy := &archBackend{runIntegrationBackend: &runIntegrationBackend{}, kvs: llama8BKVs(), size: 5 << 30}

	for _, tc := range []struct {
		name string
		b    *archBackend
		fp   device.Fingerprint
		used int
	}{
		{"no backend", nil, full, 8192},
		{"unmeasured vram", roomy, device.Fingerprint{Config: map[string]string{}}, 8192},
		{"no architecture metadata", &archBackend{runIntegrationBackend: &runIntegrationBackend{}, kvs: nil, size: 5 << 30}, full, 8192},
		{"no context used", roomy, full, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got int
			if tc.b == nil {
				got = largerFittingContext(ctx, nil, "model", tc.fp, tc.used)
			} else {
				got = largerFittingContext(ctx, tc.b, "model", tc.fp, tc.used)
			}
			if got != 0 {
				t.Fatalf("suggested %d from an uncertain input", got)
			}
		})
	}
}
