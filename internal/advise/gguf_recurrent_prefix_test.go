package advise

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"os"
	"testing"
)

// publishedRecurrentPrefixFixture is the first 2048 bytes of a published GGUF,
// captured with an HTTP range request. The bytes are not built from fitr structs.
//
//	repo:   Khide/Qwen3.5-0.8B-Beam-ToolRouter-GGUF
//	commit: 22be680375651abdd30f4b771b7908a84cba5771
//	file:   Qwen3.5-0.8B.Q4_K_M.gguf
//	range:  bytes 0-2047 of 541903776
//	lfs:    sha256:c18f1758d916c9e666718f1b7a2777564a0ca0f5f12ae77b8e48a73273ebcb6b
//
// The header declares 40 metadata keys and 335 tensors. This prefix completes
// 29 keys and stops inside a later value. llama.cpp
// 4a89937354190cef5a97baf8eeb17336105eb72d loads attention.recurrent_layers
// with get_key_or_arr in qwen35.cpp, and sizes the fixed state with n_embd_s
// and n_embd_r in llama-hparams.cpp. ggml/include/gguf.h at that commit numbers
// bool as 7 and array as 9. Commit 9a7570587ce908b0073a0458877205b80627f393 is
// the converter that started writing the array.
const publishedRecurrentPrefixFixture = "testdata/qwen35-0.8b-recurrent-2k.bin"

const publishedRecurrentPrefixSHA = "c784ad08ecaa33228391ec851eedba93f077518b97ca591b7a40ef26fe2c8959"

func TestPublishedRecurrentHeaderKeepsTheBooleanWireShape(t *testing.T) {
	b := publishedRecurrentPrefix(t)
	if got := binary.LittleEndian.Uint32(b[4:8]); got != 3 {
		t.Fatalf("GGUF version = %d", got)
	}
	if got := binary.LittleEndian.Uint64(b[8:16]); got != 335 {
		t.Fatalf("tensor count = %d", got)
	}
	if got := binary.LittleEndian.Uint64(b[16:24]); got != 40 {
		t.Fatalf("metadata key count = %d", got)
	}
	if kvs, err := ReadMetadata(bytes.NewReader(b)); err == nil || kvs != nil {
		t.Fatalf("ReadMetadata kept %d keys, err=%v; a short local file stays discarded", len(kvs), err)
	}
	kvs, err := ReadMetadataPrefix(bytes.NewReader(b))
	if !errors.Is(err, ErrMetadataTruncated) || len(kvs) != 29 {
		t.Fatalf("prefix recovered %d keys, err=%v", len(kvs), err)
	}
	if _, present := kvs["tokenizer.ggml.tokens"]; present {
		t.Fatal("a value that ran out of bytes was stored")
	}
	if kvs["qwen35.nextn_predict_layers"] != uint64(1) {
		t.Fatalf("nextn = %T(%v)", kvs["qwen35.nextn_predict_layers"], kvs["qwen35.nextn_predict_layers"])
	}
	raw := publishedRecurrentBools(t, b)
	decoded, ok := kvs["qwen35.attention.recurrent_layers"].([]any)
	if !ok || len(decoded) != len(raw) {
		t.Fatalf("decoded recurrent layers = %T", kvs["qwen35.attention.recurrent_layers"])
	}
	// qwen35.cpp at the pinned commit falls back to
	// is_recr[i] = i < n_layer() && (i+1) % interval != 0, with
	// n_layer() = n_layer_all - n_layer_nextn. The published array matches
	// that fallback, including the final non-recurrent prediction slot.
	for i, entry := range decoded {
		bit, isBool := entry.(bool)
		want := i < 24 && (i+1)%4 != 0
		if !isBool || bit != (raw[i] == 1) || bit != want {
			t.Fatalf("layer %d = %T(%v), wire %d, fallback %v", i, entry, entry, raw[i], want)
		}
	}
}

func TestPublishedRecurrentHeaderMatchesPinnedAllocationCounts(t *testing.T) {
	kvs, err := ReadMetadataPrefix(bytes.NewReader(publishedRecurrentPrefix(t)))
	if err != nil && !errors.Is(err, ErrMetadataTruncated) {
		t.Fatal(err)
	}
	arch := ArchFromKVs(kvs)
	if !arch.KVReady() || arch.InvalidMetadata || arch.RecurrentPatternUnsupported {
		note, _ := arch.UnsizableReason()
		t.Fatalf("recovered header did not project: %s %+v", note, arch)
	}
	if arch.Blocks != 25 || arch.NextNPredictLayers != 1 || arch.RecurrentLayers != 18 || arch.fullAttentionLayers() != 6 {
		t.Fatalf("boundary blocks=%d nextn=%d recurrent=%d attending=%d",
			arch.Blocks, arch.NextNPredictLayers, arch.RecurrentLayers, arch.fullAttentionLayers())
	}
	// n_embd_s = ssm_d_state * ssm_d_inner.
	// n_embd_r = (ssm_d_conv-1) * (ssm_d_inner + 2*ssm_n_group*ssm_d_state).
	// llama_memory_recurrent stores both as fp32 and loops n_layer(), so the
	// prediction slot is not a recurrent row.
	const fp32 = 4
	nEmbdS := 128 * 2048
	nEmbdR := 3 * (2048 + 2*16*128)
	wantFixed := 18 * fp32 * (nEmbdS + nEmbdR)
	fixed, ok := arch.recurrentStateBytes()
	if !ok || fixed != float64(wantFixed) || wantFixed != 20201472 {
		t.Fatalf("fixed state = %v available %v, formula %d", fixed, ok, wantFixed)
	}
	// Six decoder attention layers, two KV heads, key and value width 256,
	// f16 elements. llama_kv_cache iterates n_layer_all, so the non-recurrent
	// prediction slot can receive a KV tensor. This projection keeps the
	// decoder boundary used for interval hybrids and does not include that
	// tensor, a sequence plane, or a measured resident total.
	const perToken = 6 * 2 * (256 + 256) * 2
	if got := arch.kvBytesPerToken(2); got != perToken {
		t.Fatalf("bytes per token = %v, want %d", got, perToken)
	}
	projected, ok := ProjectKVBytes(arch, 2048, 2)
	want := int64(perToken*2048) + int64(wantFixed)
	if !ok || projected != want {
		t.Fatalf("projected = %d available %v, want %d", projected, ok, want)
	}
	larger, ok := ProjectKVBytes(arch, 4096, 2)
	if !ok || larger-projected != int64(perToken*2048) {
		t.Fatalf("fixed state scaled with context: %d -> %d", projected, larger)
	}
}

func TestPublishedPrefixDoesNotClearASourceProjection(t *testing.T) {
	_, kvs, reason := sourceHeaderArchitecture(SourceHeader{Body: publishedRecurrentPrefix(t)})
	if reason == "" || kvs != nil {
		t.Fatalf("truncated published prefix cleared a source projection: %q", reason)
	}
}

func TestCuttingOffThePublishedBooleanKeyDoesNotInventThePattern(t *testing.T) {
	b := publishedRecurrentPrefix(t)
	keyAt := bytes.Index(b, []byte("qwen35.attention.recurrent_layers"))
	if keyAt < 8 {
		t.Fatalf("key record at %d", keyAt)
	}
	// Ending at the length prefix keeps earlier keys. It does not show that
	// the unread key is absent from the rest of the file.
	kvs, err := ReadMetadataPrefix(bytes.NewReader(b[:keyAt-8]))
	if !errors.Is(err, ErrMetadataTruncated) {
		t.Fatalf("err = %v", err)
	}
	if _, present := kvs["qwen35.attention.recurrent_layers"]; present {
		t.Fatal("a key whose record was outside the prefix was recovered")
	}
	arch := ArchFromKVs(kvs)
	if arch.RecurrentLayers != 0 || arch.RecurrentPatternUnsupported {
		t.Fatalf("an unread record became a recurrent pattern: %+v", arch)
	}
}

func TestFlippingOnePublishedRecurrentBitChangesTheCount(t *testing.T) {
	b := append([]byte(nil), publishedRecurrentPrefix(t)...)
	at := publishedRecurrentBoolOffset(t, b)
	if b[at] != 1 {
		t.Fatalf("first wire bit = %d", b[at])
	}
	b[at] = 0
	kvs, err := ReadMetadataPrefix(bytes.NewReader(b))
	if err != nil && !errors.Is(err, ErrMetadataTruncated) {
		t.Fatal(err)
	}
	arch := ArchFromKVs(kvs)
	if arch.FullAttentionInterval != 4 || arch.RecurrentLayers != 17 || arch.fullAttentionLayers() != 7 || !arch.KVReady() {
		t.Fatalf("the boolean array did not drive the count: %+v", arch)
	}
}

func publishedRecurrentPrefix(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(publishedRecurrentPrefixFixture)
	if err != nil {
		t.Fatalf("fixture missing: %v", err)
	}
	sum := sha256.Sum256(b)
	if hex.EncodeToString(sum[:]) != publishedRecurrentPrefixSHA || len(b) != 2048 {
		t.Fatalf("prefix length %d sha256 %s", len(b), hex.EncodeToString(sum[:]))
	}
	return b
}

func publishedRecurrentBools(t *testing.T, b []byte) []byte {
	t.Helper()
	at := publishedRecurrentBoolOffset(t, b)
	out := b[at : at+25]
	for _, bit := range out {
		if bit > 1 {
			t.Fatalf("wire bit %d is not a boolean", bit)
		}
	}
	return out
}

func publishedRecurrentBoolOffset(t *testing.T, b []byte) int {
	t.Helper()
	const key = "qwen35.attention.recurrent_layers"
	idx := bytes.Index(b, []byte(key))
	if idx < 8 || int(binary.LittleEndian.Uint64(b[idx-8:idx])) != len(key) {
		t.Fatalf("boolean key record at %d", idx)
	}
	typAt := idx + len(key)
	if typAt+16+25 > len(b) {
		t.Fatal("boolean array does not fit in the retained prefix")
	}
	if binary.LittleEndian.Uint32(b[typAt:]) != ggufArray || binary.LittleEndian.Uint32(b[typAt+4:]) != ggufBool {
		t.Fatalf("wire type %d element %d", binary.LittleEndian.Uint32(b[typAt:]), binary.LittleEndian.Uint32(b[typAt+4:]))
	}
	if binary.LittleEndian.Uint64(b[typAt+8:]) != 25 {
		t.Fatalf("array length %d", binary.LittleEndian.Uint64(b[typAt+8:]))
	}
	return typAt + 16
}
