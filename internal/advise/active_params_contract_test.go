package advise

import "testing"

func TestActiveParamsReconstructsMoEComputePathWithoutTotalMetadata(t *testing.T) {
	arch := Arch{
		Blocks: 4, Embed: 512, Heads: 8, KVHeads: 2,
		KeyLength: 64, ValLength: 64,
		Experts: 8, ExpertUsed: 2, FFN: 1024, Vocab: 1000,
	}
	got, ok := arch.ActiveParams()
	if !ok {
		t.Fatal("complete MoE routing metadata was not computable")
	}
	const want = int64(15_716_352)
	if got != want {
		t.Fatalf("active parameters = %d, want %d", got, want)
	}
}

func TestActiveParamsRequiresEnoughAttentionShapeToReconstruct(t *testing.T) {
	base := Arch{
		Blocks: 4, Embed: 512, Heads: 8, KVHeads: 2,
		Experts: 8, ExpertUsed: 2, FFN: 1024, Vocab: 1000,
	}
	if got, ok := base.ActiveParams(); !ok || got <= 0 {
		t.Fatalf("embed/head fallback should reconstruct active parameters: %d, %v", got, ok)
	}
	base.Heads = 0
	if got, ok := base.ActiveParams(); ok || got != 0 {
		t.Fatalf("missing attention heads produced active parameters: %d, %v", got, ok)
	}
	if got, ok := (Arch{}).ActiveParams(); ok || got != 0 {
		t.Fatalf("empty dense metadata produced active parameters: %d, %v", got, ok)
	}
}

func TestMetadataIntegerConversionAcceptsKnownGGUFScalarShapesOnly(t *testing.T) {
	for _, tc := range []struct {
		value any
		want  int64
	}{
		{value: int(1), want: 1},
		{value: int32(2), want: 2},
		{value: int64(3), want: 3},
		{value: uint32(4), want: 4},
		{value: uint64(5), want: 5},
		{value: uint64(1) << 63, want: 0},
		{value: float64(6), want: 6},
		{value: float32(7), want: 7},
		{value: []any{}, want: 0},
		{value: []any{"8"}, want: 8},
		{value: "9", want: 9},
		{value: "not-an-integer", want: 0},
		{value: true, want: 0},
	} {
		if got := asInt64(tc.value); got != tc.want {
			t.Fatalf("asInt64(%T(%v)) = %d, want %d", tc.value, tc.value, got, tc.want)
		}
	}
}

func TestKVElemBytesCoversSupportedRuntimeNames(t *testing.T) {
	for _, tc := range []struct {
		name string
		want float64
	}{
		{name: " BF16 ", want: 2},
		{name: "float32", want: 4},
		{name: "q8", want: 1},
		{name: "Q4_1", want: 0.5},
	} {
		if got, ok := KVElemBytes(tc.name); !ok || got != tc.want {
			t.Fatalf("KVElemBytes(%q) = %v, %v; want %v", tc.name, got, ok, tc.want)
		}
	}
}
