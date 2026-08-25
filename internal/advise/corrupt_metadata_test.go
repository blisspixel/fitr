package advise

import (
	"bytes"
	"math"
	"testing"
)

// A GGUF header is an untrusted file. This one, found by FuzzReadMetadata,
// declares dimensions near 2^62. The products in the KV arithmetic wrapped
// negative in int64, so bytes-per-token came out NEGATIVE, need became less
// than weights, and advise answered "compatible" for a model that does not
// fit -- printing a negative GB figure beside the verdict. Wrong, and wrong
// in the direction that matters. Implausible metadata is unmeasured metadata.
func TestCorruptGGUFMetadataCannotProduceAFitVerdict(t *testing.T) {
	raw := []byte("GGUF\x03\x00\x00\x0000000000\x06\x00\x00\x00\x00\x00\x00\x00\x14\x00\x00\x00\x00\x00\x00\x00general.architecture\b\x00\x00\x00\x05\x00\x00\x00\x00\x00\x00\x00llama\x11\x00\x00\x00\x00\x00\x00\x00llama.block_count\n\x00\x00\x0000000000\x1d\x00\x00\x00\x00\x00\x00\x00llama.attention.head_count_kv\n\x00\x00\x00X0000000\x1a\x00\x00\x00\x00\x00\x00\x00llama.attention.key_length\n\x00\x00\x0000000201\x14\x00\x00\x00\x00\x00\x00\x0000000000000000000000\x00\x00\x00\x000\x16\x00\x00\x00\x00\x00\x00\x000000000000000000000000\x00\x00\x00\x000")
	kvs, err := ReadMetadata(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("corpus seed no longer parses: %v", err)
	}
	a := ArchFromKVs(kvs)
	if a.KVReady() {
		t.Fatalf("architecture from absurd dimensions is not sizable: %+v", a)
	}
	if got := a.kvBytesPerToken(2); got != 0 {
		t.Fatalf("bytes per token = %v, want 0 for an unsizable architecture", got)
	}
	r := Evaluate(Input{
		Model: "corrupt.gguf", WeightsB: 4 * GiB, HaveGB: 8, HaveSrc: "--vram-gb",
		Ctx: 4096, Arch: a, Backend: "ollama", Source: "GGUF metadata",
	})
	if r.Tier != Skip {
		t.Fatalf("tier = %s (%s); unbelievable metadata must SKIP, never rank", r.Tier, r.Why)
	}
	if r.NeedGB < 0 || r.KVGB < 0 || r.WeightsGB < 0 {
		t.Fatalf("negative memory reported: need=%v kv=%v weights=%v", r.NeedGB, r.KVGB, r.WeightsGB)
	}
}

// archDim keeps a real model intact while refusing one that cannot exist.
func TestArchDimAcceptsRealModelsAndRejectsImpossibleOnes(t *testing.T) {
	// Comfortably larger than any shipped model, and still accepted.
	big := map[string]any{
		"general.architecture":          "llama",
		"llama.block_count":             uint64(512),
		"llama.embedding_length":        uint64(32768),
		"llama.attention.head_count":    uint64(256),
		"llama.attention.head_count_kv": uint64(64),
		"llama.attention.key_length":    uint64(1024),
		"llama.context_length":          uint64(1 << 20),
	}
	a := ArchFromKVs(big)
	if !a.KVReady() || a.Blocks != 512 || a.KeyLength != 1024 || a.MaxCtx != 1<<20 {
		t.Fatalf("a large but real architecture was rejected: %+v", a)
	}
	if got := a.kvBytesPerToken(2); got <= 0 {
		t.Fatalf("bytes per token = %v for a valid architecture", got)
	}

	for _, bad := range []uint64{maxArchDim + 1, 1 << 40, math.MaxUint64 / 2} {
		a := ArchFromKVs(map[string]any{
			"general.architecture":          "llama",
			"llama.block_count":             bad,
			"llama.attention.head_count_kv": uint64(8),
			"llama.attention.key_length":    uint64(128),
		})
		if a.Blocks != 0 {
			t.Fatalf("block_count %d was accepted as %d", bad, a.Blocks)
		}
		if a.KVReady() {
			t.Fatalf("architecture with an impossible block count is not sizable: %+v", a)
		}
	}
}

// round1 is display arithmetic, and a float64 beyond int64 converts to the
// most negative int64 -- a huge number printed as a large negative one.
func TestRound1SurvivesValuesOutsideInt64(t *testing.T) {
	for _, v := range []float64{1e30, -1e30, math.MaxFloat64, math.Inf(1), math.Inf(-1)} {
		got := round1(v)
		if (v > 0 && got < 0) || (v < 0 && got > 0) {
			t.Fatalf("round1(%v) = %v, flipped sign", v, got)
		}
	}
	if math.IsNaN(round1(math.NaN())) != true {
		t.Fatal("round1(NaN) must stay NaN rather than become a number")
	}
	// In-range behaviour is unchanged.
	for _, tc := range []struct{ in, want float64 }{{1.24, 1.2}, {1.25, 1.3}, {17.34, 17.3}, {0, 0}} {
		if got := round1(tc.in); got != tc.want {
			t.Fatalf("round1(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// Parameter counts are products of five metadata fields. Unchecked, they wrap
// to a negative and the model's size prints as a negative number of
// parameters. "Cannot be computed" is already this function's answer for an
// unresolvable split, so an overflow returns that rather than a wrapped value.
func TestActiveParamsRefusesToOverflow(t *testing.T) {
	a := ArchFromKVs(map[string]any{
		"general.architecture":           "moe",
		"moe.block_count":                uint64(maxArchDim),
		"moe.embedding_length":           uint64(maxArchDim),
		"moe.expert_feed_forward_length": uint64(maxArchDim),
		"moe.expert_count":               uint64(maxArchDim),
		"moe.expert_used_count":          uint64(8),
		"moe.attention.head_count":       uint64(64),
		"moe.attention.head_count_kv":    uint64(8),
		"moe.attention.key_length":       uint64(128),
	})
	if p, ok := a.ActiveParams(); ok || p != 0 {
		t.Fatalf("ActiveParams = (%d, %v); an overflowing split is not computable", p, ok)
	}

	// A real MoE still resolves. Qwen3-30B-A3B: 48 blocks, 128 experts, 8 used.
	genuine := ArchFromKVs(map[string]any{
		"general.architecture":                "qwen3moe",
		"general.parameter_count":             uint64(30532122624),
		"qwen3moe.block_count":                uint64(48),
		"qwen3moe.embedding_length":           uint64(2048),
		"qwen3moe.expert_feed_forward_length": uint64(768),
		"qwen3moe.expert_count":               uint64(128),
		"qwen3moe.expert_used_count":          uint64(8),
		"qwen3moe.attention.head_count":       uint64(32),
		"qwen3moe.attention.head_count_kv":    uint64(4),
		"qwen3moe.attention.key_length":       uint64(128),
	})
	p, ok := genuine.ActiveParams()
	if !ok || p <= 0 {
		t.Fatalf("a real MoE failed to resolve: (%d, %v)", p, ok)
	}
	if p >= genuine.Params {
		t.Fatalf("active %d is not below total %d for an MoE", p, genuine.Params)
	}
}

func TestCheckedArithmeticBoundaries(t *testing.T) {
	if got, ok := mulInt64(math.MaxInt64, 2); ok || got != 0 {
		t.Fatalf("mulInt64 overflow = (%d,%v)", got, ok)
	}
	if got, ok := mulInt64(math.MaxInt64, 1); !ok || got != math.MaxInt64 {
		t.Fatalf("mulInt64 at the limit = (%d,%v)", got, ok)
	}
	if got, ok := mulInt64(math.MaxInt64, 0); !ok || got != 0 {
		t.Fatalf("a zero factor is zero, not overflow: (%d,%v)", got, ok)
	}
	if _, ok := mulInt64(-1, 2); ok {
		t.Fatal("a negative factor is not a size")
	}
	if got, ok := addInt64(math.MaxInt64, 1); ok || got != 0 {
		t.Fatalf("addInt64 overflow = (%d,%v)", got, ok)
	}
	if got, ok := addInt64(1, 2, 3); !ok || got != 6 {
		t.Fatalf("addInt64 = (%d,%v), want 6", got, ok)
	}
}
