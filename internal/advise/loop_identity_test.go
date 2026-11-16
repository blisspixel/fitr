package advise

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/record"
)

// Published Dory header facts from issue 110. Key names were checked against
// llama.cpp pull 30306 head ab0ffa188bbc3be15ce8e5d3f1278ba5224f4358, which
// was open and unmerged on 2026-11-16. This map is not a downloaded artifact.
func doryLoopKVs(sliding bool) map[string]any {
	kvs := map[string]any{
		"general.architecture":         "dory",
		"dory.block_count":             uint64(72),
		"dory.embedding_length":        uint64(2560),
		"dory.attention.head_count":    uint64(20),
		"dory.attention.head_count_kv": uint64(4),
		"dory.attention.key_length":    uint64(128),
		"dory.attention.value_length":  uint64(128),
		"dory.num_loops":               uint64(4),
		"dory.recurrent_kv_cache_mode": "per_loop",
		"dory.input_layer_count":       uint64(16),
		"dory.recurrent_layer_count":   uint64(40),
		"dory.output_layer_count":      uint64(16),
	}
	if sliding {
		kvs["dory.attention.sliding_window"] = uint64(1024)
	}
	return kvs
}

func TestLoopedArchitectureWithholdsKVProjection(t *testing.T) {
	arch := ArchFromKVs(doryLoopKVs(false))
	if arch.KVReady() || arch.RecurrentLayers != 0 || arch.Hybrid || arch.ShapeClass() != "looped" {
		t.Fatalf("loop declaration was treated as an ordinary or boolean-hybrid layout: %+v", arch)
	}
	if bytes, ok := ProjectKVBytes(arch, 4096, 2); ok || bytes != 0 {
		t.Fatalf("projection = %d, %v", bytes, ok)
	}
	note, hint := arch.UnsizableReason()
	if note != loopRefusalNote {
		t.Fatalf("note = %q", note)
	}
	if arch.UnsizableReasonCode() != LoopedArchitectureReason {
		t.Fatalf("reason code = %q", arch.UnsizableReasonCode())
	}
	if arch.NumLoops != 4 || arch.NumLoopsState != LoopStateDeclared {
		t.Fatalf("file loop count = %d %q", arch.NumLoops, arch.NumLoopsState)
	}
	if RecurrentKVCachePerLoop != record.RecurrentKVCachePerLoop || RecurrentKVCacheLastLoop != record.RecurrentKVCacheLastLoop {
		t.Fatal("file cache-mode spelling drifted from the sealed observation")
	}
	if arch.RecurrentKVCacheMode != RecurrentKVCachePerLoop || arch.RecurrentKVCacheModeState != LoopStateDeclared {
		t.Fatalf("file cache mode = %q %q", arch.RecurrentKVCacheMode, arch.RecurrentKVCacheModeState)
	}
	if arch.InputLayerCount != 16 || arch.RecurrentLayerCount != 40 || arch.OutputLayerCount != 16 {
		t.Fatalf("layer counts = %d %d %d", arch.InputLayerCount, arch.RecurrentLayerCount, arch.OutputLayerCount)
	}
	if strings.Contains(hint, "recommended 2") || strings.Contains(hint, "n_recurrent_loops") {
		t.Fatalf("hint invented a publisher recommendation: %q", hint)
	}
	if !strings.Contains(hint, "unobserved") || !strings.Contains(hint, "not the serving configuration") {
		t.Fatalf("hint = %q", hint)
	}
	shape := arch.Shape()
	if shape.KVSizable || shape.UnsizableReasonCode != LoopedArchitectureReason || shape.NumLoops != 4 || shape.KVBytesPerToken != 0 {
		t.Fatalf("shape = %+v", shape)
	}
	text := arch.FileLoopText()
	if !strings.Contains(text, "file num_loops 4") || !strings.Contains(text, "publisher recommendation unobserved") {
		t.Fatalf("file loop text = %q", text)
	}
	last := doryLoopKVs(false)
	last["dory.recurrent_kv_cache_mode"] = RecurrentKVCacheLastLoop
	if got := ArchFromKVs(last); got.RecurrentKVCacheMode != RecurrentKVCacheLastLoop || got.KVReady() {
		t.Fatalf("last_loop = %+v", got)
	}
}

func TestSlidingWindowDoesNotHideALoopedArchitecture(t *testing.T) {
	arch := ArchFromKVs(doryLoopKVs(true))
	note, _ := arch.UnsizableReason()
	if arch.KVReady() || arch.SlidingWindow != 1024 || strings.Contains(note, "sliding-window") {
		t.Fatalf("sliding window hid the loop declaration: note %q arch %+v", note, arch)
	}
	if arch.UnsizableReasonCode() != LoopedArchitectureReason {
		t.Fatalf("reason code = %q", arch.UnsizableReasonCode())
	}
	without := doryLoopKVs(true)
	delete(without, "dory.num_loops")
	delete(without, "dory.recurrent_kv_cache_mode")
	delete(without, "dory.input_layer_count")
	delete(without, "dory.recurrent_layer_count")
	delete(without, "dory.output_layer_count")
	hidden := ArchFromKVs(without)
	hiddenNote, _ := hidden.UnsizableReason()
	if hidden.loopDeclared() || !strings.Contains(hiddenNote, "sliding-window") {
		t.Fatalf("removing the loop keys should leave the sliding-window refusal: %q %+v", hiddenNote, hidden)
	}
}

func TestMissingLoopKeyIsNotDefaulted(t *testing.T) {
	kvs := map[string]any{
		"general.architecture":          "llama",
		"llama.block_count":             uint64(32),
		"llama.embedding_length":        uint64(4096),
		"llama.attention.head_count":    uint64(32),
		"llama.attention.head_count_kv": uint64(8),
		"llama.attention.key_length":    uint64(128),
		"llama.attention.value_length":  uint64(128),
	}
	arch := ArchFromKVs(kvs)
	if !arch.KVReady() || arch.NumLoops != 0 || arch.NumLoopsState != "" || arch.loopDeclared() {
		t.Fatalf("absent loop key was filled in: %+v", arch)
	}
	if arch.FileLoopText() != "" || arch.UnsizableReasonCode() != "" || arch.ShapeClass() != "dense" {
		t.Fatal("an ordinary architecture grew a loop explanation")
	}
	encoded, err := json.Marshal(arch.Shape())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "num_loops") || strings.Contains(string(encoded), "recurrent_kv") {
		t.Fatalf("ordinary shape grew loop fields: %s", encoded)
	}
	kvs["llama.num_loops"] = uint64(4)
	declared := ArchFromKVs(kvs)
	if declared.NumLoops != 4 || declared.NumLoops == 2 || declared.NumLoops == 1 {
		t.Fatalf("loop count = %d", declared.NumLoops)
	}
}

func TestWrongShapeLoopMetadataIsUnmeasured(t *testing.T) {
	for _, value := range []any{"4", []any{uint64(4)}, 4.5, uint64(0), int64(-1), uint64(maxArchDim + 1)} {
		kvs := doryLoopKVs(false)
		kvs["dory.num_loops"] = value
		arch := ArchFromKVs(kvs)
		if arch.NumLoops != 0 || arch.NumLoopsState != LoopStateUnmeasured || arch.KVReady() {
			t.Fatalf("value %#v became loop count %d state %q ready %v", value, arch.NumLoops, arch.NumLoopsState, arch.KVReady())
		}
		if bytes, ok := ProjectKVBytes(arch, 1024, 2); ok || bytes != 0 {
			t.Fatalf("wrong-shaped loop count projected %d", bytes)
		}
		if arch.UnsizableReasonCode() != LoopedArchitectureReason {
			t.Fatalf("value %#v reason code = %q", value, arch.UnsizableReasonCode())
		}
	}
	base := doryLoopKVs(false)
	base["dory.recurrent_kv_cache_mode"] = "both"
	arch := ArchFromKVs(base)
	if arch.RecurrentKVCacheMode != "" || arch.RecurrentKVCacheModeState != LoopStateUnmeasured || arch.KVReady() {
		t.Fatalf("unknown cache mode was stored: %+v", arch)
	}
	integral := doryLoopKVs(false)
	integral["dory.num_loops"] = 4.0
	if got := ArchFromKVs(integral); got.NumLoops != 4 || got.NumLoopsState != LoopStateDeclared {
		t.Fatalf("integral float loop count = %d %q", got.NumLoops, got.NumLoopsState)
	}
}

func TestScalarRecurrentLayerCountIsNotTheBooleanPattern(t *testing.T) {
	kvs := doryLoopKVs(false)
	delete(kvs, "dory.num_loops")
	delete(kvs, "dory.recurrent_kv_cache_mode")
	arch := ArchFromKVs(kvs)
	if arch.RecurrentLayers != 0 || arch.RecurrentLayerCount != 40 || !arch.loopDeclared() || arch.KVReady() {
		t.Fatalf("scalar recurrent_layer_count changed the boolean pattern: %+v", arch)
	}
	boolean := map[string]any{
		"general.architecture":                   "custom_arch",
		"custom_arch.block_count":                uint64(32),
		"custom_arch.attention.head_count":       uint64(32),
		"custom_arch.attention.head_count_kv":    uint64(8),
		"custom_arch.attention.key_length":       uint64(128),
		"custom_arch.attention.value_length":     uint64(128),
		"custom_arch.attention.recurrent_layers": false,
	}
	kept := ArchFromKVs(boolean)
	if !kept.KVReady() || kept.RecurrentLayers != 0 || kept.RecurrentLayerCountState != "" || kept.loopDeclared() {
		t.Fatalf("boolean recurrent pattern changed: %+v", kept)
	}
}

func TestLayerPatternWithholdsKVProjection(t *testing.T) {
	kvs := map[string]any{
		"general.architecture":         "dory",
		"dory.block_count":             uint64(72),
		"dory.embedding_length":        uint64(2560),
		"dory.attention.head_count":    uint64(20),
		"dory.attention.head_count_kv": uint64(4),
		"dory.attention.key_length":    uint64(128),
		"dory.attention.value_length":  uint64(128),
		"dory.layer_pattern":           []any{"input", "recurrent"},
	}
	arch := ArchFromKVs(kvs)
	if arch.NumLoops != 0 || arch.LayerPatternState != LoopStateUnmeasured || arch.KVReady() {
		t.Fatalf("layer pattern was projected or counted: %+v", arch)
	}
}
