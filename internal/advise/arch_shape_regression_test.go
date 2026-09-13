package advise

import (
	"math"
	"strings"
	"testing"
)

func TestModernLayoutIsReadBeforeMissingKVFallback(t *testing.T) {
	kvs := map[string]any{"general.architecture": "interval_future", "interval_future.block_count": uint64(8),
		"interval_future.attention.head_count": uint64(16), "interval_future.attention.key_length": uint64(64),
		"interval_future.full_attention_interval": uint64(4), "interval_future.ssm.inner_size": uint64(128),
		"interval_future.ssm.state_size": uint64(16), "interval_future.ssm.conv_kernel": uint64(4),
		"interval_future.ssm.group_count": uint64(1)}
	arch := ArchFromKVs(kvs)
	if arch.KVHeads != 0 || arch.KVReady() {
		t.Fatalf("missing hybrid KV heads became all attention heads: %+v", arch)
	}
}

func TestInvalidKVScalarCannotBecomeAbsentOrFallback(t *testing.T) {
	for _, value := range []any{uint64(0), true, "2 heads", 2.9, math.NaN(), math.Inf(1), []any{}, []any{true}, map[string]any{}} {
		kvs := sourceFitMetadata()
		kvs["llama.attention.head_count_kv"] = value
		arch := ArchFromKVs(kvs)
		if arch.KVReady() {
			t.Errorf("invalid KV head value %T(%v) was projected: %+v", value, value, arch)
		}
	}
}

func TestInvalidPerLayerHeadEntryCannotBecomeZero(t *testing.T) {
	for _, value := range []any{true, false, "bad", 1.9, math.NaN(), math.Inf(1), map[string]any{}, []any{uint64(2)}} {
		kvs := sourceFitMetadata()
		kvs["llama.attention.head_count_kv"] = []any{uint64(2), value}
		arch := ArchFromKVs(kvs)
		if arch.KVReady() || len(arch.KVHeadsPerLayer) != 0 {
			t.Errorf("invalid per-layer value %T(%v) became a real layer dimension: %+v", value, value, arch)
		}
	}
}

func TestInvalidScalarDimensionCannotBeTruncatedOrParsedAsPrefix(t *testing.T) {
	for _, value := range []any{2.8, "2garbage", "2.0", math.NaN(), math.Inf(1)} {
		kvs := sourceFitMetadata()
		kvs["llama.block_count"] = value
		arch := ArchFromKVs(kvs)
		if arch.KVReady() || arch.Blocks != 0 {
			t.Errorf("invalid block count %T(%v) was projected: %+v", value, value, arch)
		}
	}
}

func TestNonDivisibleHybridIntervalNeedsPerLayerEvidence(t *testing.T) {
	arch := Arch{Name: "interval_future", Blocks: 10, Heads: 8, KVHeads: 2,
		KeyLength: 64, ValLength: 64, Hybrid: true, FullAttentionInterval: 4,
		SSMInnerSize: 128, SSMStateSize: 16, SSMConvKernel: 4, SSMGroupCount: 1, SSMGroupCountKnown: true}
	if arch.KVReady() {
		t.Fatal("10 layers at interval 4 have two or three attending layers depending on the runtime convention")
	}
	arch.KVHeadsPerLayer = []int{2, 0, 0, 0, 2, 0, 0, 0, 2, 0}
	if !arch.KVReady() {
		t.Fatal("exact per-layer evidence should resolve the incomplete pattern")
	}
}

func TestHybridConvolutionIncludesRecurrentKeyGroups(t *testing.T) {
	// llama.cpp 4a89937354190cef5a97baf8eeb17336105eb72d n_embd_r()
	// includes both recurrent key groups as well as the inner value width.
	// 48 linear layers * 4-byte fp32 * (6144*128 + 3*(6144+2*16*128)).
	arch := ArchFromKVs(qwen38KVs())
	fixed, ok := arch.recurrentStateBytes()
	if !ok || fixed != 156893184 {
		t.Fatalf("recurrent convolution omitted key groups: %v, available %v", fixed, ok)
	}
}

func TestRecurrentLayerWireShapeIsNotAScalarCount(t *testing.T) {
	for _, value := range []any{uint64(48), []any{true, true}} {
		kvs := qwen38KVs()
		kvs["qwen35.attention.recurrent_layers"] = value
		if arch := ArchFromKVs(kvs); arch.KVReady() {
			t.Fatalf("invalid recurrent-layer shape became a projection: %+v", arch)
		}
	}
	kvs := qwen38KVs()
	layers := make([]any, 65)
	for i := range layers {
		layers[i] = i < 64 && i%4 != 3
	}
	kvs["qwen35.attention.recurrent_layers"] = layers
	arch := ArchFromKVs(kvs)
	note, _ := arch.UnsizableReason()
	if arch.KVReady() || arch.InvalidMetadata || !strings.Contains(note, "recurrent pattern") {
		t.Fatalf("valid but unsupported recurrent pattern was mislabeled: %+v, %q", arch, note)
	}
}

func TestCacheByteBoundaryCannotConvertToNegative(t *testing.T) {
	arch := Arch{Blocks: 1024, KVHeads: 1024, KeyLength: 1024, ValLength: 1024}
	if bytes, ok := ProjectKVBytes(arch, 1<<31, 2); ok || bytes != 0 {
		t.Errorf("a cache at 2^63 bytes crossed the signed boundary: %d, %v", bytes, ok)
	}
	arch = Arch{Blocks: 1 << 20, KVHeads: 1, KeyLength: 1, ValLength: 1,
		Hybrid: true, FullAttentionInterval: 4, SSMInnerSize: 1 << 20, SSMStateSize: 1 << 20,
		SSMGroupCount: 1 << 20, SSMGroupCountKnown: true, SSMConvKernel: 1 << 20}
	shape := arch.Shape()
	if shape.KVSizable || shape.FixedCacheBytes < 0 {
		t.Fatalf("oversized fixed cache became a negative descriptive figure: %+v", shape)
	}
}

func TestExplicitZeroAttentionWidthsCannotUseAbsentFallback(t *testing.T) {
	for _, key := range []string{"llama.attention.key_length", "llama.attention.value_length"} {
		t.Run(key, func(t *testing.T) {
			kvs := sourceFitMetadata()
			kvs["llama.embedding_length"] = uint64(1024)
			kvs[key] = uint64(0)
			arch := ArchFromKVs(kvs)
			if arch.KVReady() {
				t.Fatalf("an explicit zero width became an inferred positive width: %+v", arch)
			}
			if bytes, ok := ProjectKVBytes(arch, 4096, 2); ok || bytes != 0 {
				t.Fatalf("zero width produced a cache: %d, %v", bytes, ok)
			}
			delete(kvs, key)
			if fallback := ArchFromKVs(kvs); !fallback.KVReady() || fallback.headDimK() != 128 || fallback.headDimV() != 128 {
				t.Fatalf("genuinely absent legacy metadata lost its fallback: %+v", fallback)
			}
		})
	}
}

func TestMissingValueWidthUsesIndependentEmbeddingHeadDefault(t *testing.T) {
	// llama.cpp 4a89937354190cef5a97baf8eeb17336105eb72d load_hparams
	// initializes K and V independently before their optional width overrides.
	kvs := sourceFitMetadata()
	kvs["llama.embedding_length"] = uint64(1024)
	kvs["llama.attention.key_length"] = uint64(256)
	delete(kvs, "llama.attention.value_length")
	arch := ArchFromKVs(kvs)
	if !arch.KVReady() || arch.headDimK() != 256 || arch.headDimV() != 128 {
		t.Errorf("explicit K width changed the independent default V width: %+v, V=%d", arch, arch.headDimV())
	}
	if cache, ok := ProjectKVBytes(arch, 4096, 2); !ok || cache != 12582912 {
		t.Errorf("cache did not use two distinct widths: %d, %v", cache, ok)
	}
	delete(kvs, "llama.embedding_length")
	if incomplete := ArchFromKVs(kvs); incomplete.KVReady() || incomplete.headDimV() != 0 {
		t.Fatalf("unknown V width borrowed the explicit K width: %+v", incomplete)
	}
}
