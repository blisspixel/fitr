package advise

import "testing"

// An artifact can count a multi-token-prediction head inside block_count while
// its per-layer array covers only the layers that attend. Requiring the two to
// match exactly discarded a usable array and produced an unnecessary refusal.
func TestPerLayerArrayMayExcludeThePredictionHead(t *testing.T) {
	perLayer := make([]any, 64)
	for i := range perLayer {
		if i%4 == 3 {
			perLayer[i] = uint64(4)
			continue
		}
		perLayer[i] = uint64(0)
	}
	kvs := map[string]any{
		"general.architecture":           "qwen35",
		"qwen35.block_count":             uint64(65), // 64 attention layers plus one MTP head
		"qwen35.nextn_predict_layers":    uint64(1),
		"qwen35.attention.head_count":    uint64(24),
		"qwen35.attention.head_count_kv": perLayer,
		"qwen35.attention.key_length":    uint64(256),
		"qwen35.attention.value_length":  uint64(256),
		"qwen35.full_attention_interval": uint64(4),
		"qwen35.ssm.conv_kernel":         uint64(4),
		"qwen35.ssm.state_size":          uint64(128),
		"qwen35.ssm.inner_size":          uint64(6144),
		"qwen35.ssm.group_count":         uint64(16),
	}
	arch := ArchFromKVs(kvs)
	if len(arch.KVHeadsPerLayer) != 64 {
		t.Fatalf("per-layer array was discarded: %d entries kept", len(arch.KVHeadsPerLayer))
	}
	if got := arch.totalKVHeads(); got != 16*4 {
		t.Fatalf("summed KV heads = %d, want the 16 attending layers", got)
	}
	if !arch.KVReady() {
		t.Fatalf("a complete artifact was refused: %+v", arch)
	}
}

// Any other length means the two keys describe different models.
func TestPerLayerArrayOfAnUnrelatedLengthIsRefused(t *testing.T) {
	for _, entries := range []int{10, 63, 66} {
		perLayer := make([]any, entries)
		for i := range perLayer {
			perLayer[i] = uint64(4)
		}
		arch := ArchFromKVs(map[string]any{
			"general.architecture":           "qwen35",
			"qwen35.block_count":             uint64(65),
			"qwen35.nextn_predict_layers":    uint64(1),
			"qwen35.attention.head_count":    uint64(24),
			"qwen35.attention.head_count_kv": perLayer,
			"qwen35.attention.key_length":    uint64(256),
			"qwen35.attention.value_length":  uint64(256),
		})
		if len(arch.KVHeadsPerLayer) != 0 {
			t.Fatalf("a %d-entry array against 65 blocks was accepted", entries)
		}
	}
}
