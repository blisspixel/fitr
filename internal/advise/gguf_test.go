package advise

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadMetadataRoundTrip(t *testing.T) {
	kvs := map[string]any{
		"general.architecture":             "qwen3moe",
		"general.parameter_count":          uint64(30532132864),
		"qwen3moe.block_count":             uint64(48),
		"qwen3moe.attention.head_count_kv": uint64(4),
		"qwen3moe.attention.key_length":    uint64(128),
		"qwen3moe.expert_count":            uint64(128),
		"qwen3moe.expert_used_count":       uint64(8),
	}
	raw := encodeGGUF(t, kvs)
	got, err := ReadMetadata(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if asString(got["general.architecture"]) != "qwen3moe" {
		t.Fatalf("arch = %v", got["general.architecture"])
	}
	a := ArchFromKVs(got)
	if a.Blocks != 48 || a.KVHeads != 4 || a.KeyLength != 128 || a.Experts != 128 || a.ExpertUsed != 8 {
		t.Fatalf("parsed %+v", a)
	}
	if a.Params != 30532132864 {
		t.Fatalf("params = %d", a.Params)
	}
}

func TestOpenGGUFUsesFileSizeAsWeights(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tiny.gguf")
	raw := encodeGGUF(t, map[string]any{"general.architecture": "llama"})
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	kvs, size, err := OpenGGUF(path)
	if err != nil {
		t.Fatal(err)
	}
	if size != int64(len(raw)) {
		t.Fatalf("size = %d, want %d (on-disk, not invented)", size, len(raw))
	}
	if asString(kvs["general.architecture"]) != "llama" {
		t.Fatalf("kvs = %v", kvs)
	}
}

func TestOpenGGUFSumsCompleteShardSetAndRejectsMissingShard(t *testing.T) {
	dir := t.TempDir()
	raw := encodeGGUF(t, map[string]any{"general.architecture": "qwen35"})
	first := filepath.Join(dir, "model-00001-of-00002.gguf")
	second := filepath.Join(dir, "model-00002-of-00002.gguf")
	if err := os.WriteFile(first, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := OpenGGUF(first); err == nil {
		t.Fatal("incomplete split GGUF was accepted")
	}
	if err := os.WriteFile(second, append(raw, 1, 2, 3), 0o644); err != nil {
		t.Fatal(err)
	}
	_, size, err := OpenGGUF(first)
	if err != nil {
		t.Fatal(err)
	}
	want := int64(len(raw) + len(raw) + 3)
	if size != want {
		t.Fatalf("split size = %d, want %d", size, want)
	}
}

func TestOpenGGUFRejectsUnreasonableShardCountBeforeProbing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "model-00001-of-99999.gguf")
	raw := encodeGGUF(t, map[string]any{"general.architecture": "llama"})
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := OpenGGUF(path); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("unreasonable shard count error = %v", err)
	}
}

func TestReadMetadataRejectsNotGGUF(t *testing.T) {
	if _, err := ReadMetadata(bytes.NewReader([]byte("PK\x03\x04notgguf"))); err == nil {
		t.Fatal("non-GGUF must error")
	}
}

func TestReadMetadataRejectsUnsupportedVersion(t *testing.T) {
	buf := new(bytes.Buffer)
	buf.WriteString("GGUF")
	binary.Write(buf, binary.LittleEndian, uint32(1))
	if _, err := ReadMetadata(buf); err == nil {
		t.Fatal("GGUF v1 must error, not parse as v3")
	}
}

func TestReadMetadataRejectsNestedArrays(t *testing.T) {
	raw := encodeGGUFValue(t, "bad", func(buf *bytes.Buffer) {
		binary.Write(buf, binary.LittleEndian, ggufArray)
		binary.Write(buf, binary.LittleEndian, ggufArray)
		binary.Write(buf, binary.LittleEndian, uint64(1))
	})
	_, err := ReadMetadata(bytes.NewReader(raw))
	if err == nil || !strings.Contains(err.Error(), "nested GGUF arrays") {
		t.Fatalf("nested array error = %v", err)
	}
}

func TestReadMetadataRejectsDuplicateKeysAndInvalidBoolean(t *testing.T) {
	t.Run("duplicate key", func(t *testing.T) {
		buf := new(bytes.Buffer)
		buf.WriteString("GGUF")
		binary.Write(buf, binary.LittleEndian, uint32(3))
		binary.Write(buf, binary.LittleEndian, uint64(0))
		binary.Write(buf, binary.LittleEndian, uint64(2))
		for _, value := range []uint64{1, 2} {
			writeString(buf, "general.parameter_count")
			binary.Write(buf, binary.LittleEndian, ggufUint64)
			binary.Write(buf, binary.LittleEndian, value)
		}
		if _, err := ReadMetadata(bytes.NewReader(buf.Bytes())); err == nil || !strings.Contains(err.Error(), "duplicate") {
			t.Fatalf("duplicate metadata error = %v", err)
		}
	})

	t.Run("invalid boolean", func(t *testing.T) {
		raw := encodeGGUFValue(t, "general.test", func(buf *bytes.Buffer) {
			binary.Write(buf, binary.LittleEndian, ggufBool)
			binary.Write(buf, binary.LittleEndian, uint8(2))
		})
		if _, err := ReadMetadata(bytes.NewReader(raw)); err == nil || !strings.Contains(err.Error(), "boolean") {
			t.Fatalf("invalid boolean error = %v", err)
		}
	})
}

func TestReadMetadataRejectsHugeArrayBeforeAllocation(t *testing.T) {
	storageBytes, err := arrayElementStorageBytes(ggufUint8)
	if err != nil {
		t.Fatal(err)
	}
	limit := maxArrayStorageBytes / storageBytes
	raw := encodeGGUFValue(t, "bad", func(buf *bytes.Buffer) {
		binary.Write(buf, binary.LittleEndian, ggufArray)
		binary.Write(buf, binary.LittleEndian, ggufUint8)
		binary.Write(buf, binary.LittleEndian, limit+1)
	})
	_, err = ReadMetadata(bytes.NewReader(raw))
	if err == nil || !strings.Contains(err.Error(), "array length") {
		t.Fatalf("oversized array error = %v", err)
	}
}

func TestReadMetadataEnforcesAggregateStringBudget(t *testing.T) {
	raw := encodeGGUF(t, map[string]any{
		"tokenizer.ggml.tokens": []string{"alpha", "bravo", "charlie"},
	})
	if _, err := readMetadata(bytes.NewReader(raw), 128); err == nil ||
		!strings.Contains(err.Error(), "metadata budget exceeded") {
		t.Fatalf("aggregate budget error = %v", err)
	}
	if _, err := readMetadata(bytes.NewReader(raw), 1024); err != nil {
		t.Fatalf("reasonable aggregate metadata was rejected: %v", err)
	}
}

func TestReadMetadataAcceptsNormalVocabularyArray(t *testing.T) {
	const vocabSize = 128256
	tokens := make([]string, vocabSize)
	for i := range tokens {
		tokens[i] = fmt.Sprintf("token_%06d", i)
	}
	raw := encodeGGUF(t, map[string]any{
		"general.architecture":  "llama",
		"tokenizer.ggml.tokens": tokens,
	})
	kvs, err := ReadMetadata(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	got, ok := kvs["tokenizer.ggml.tokens"].([]any)
	if !ok || len(got) != vocabSize {
		t.Fatalf("vocabulary type/length = %T/%d", kvs["tokenizer.ggml.tokens"], len(got))
	}
	if got[0] != "token_000000" || got[len(got)-1] != "token_128255" {
		t.Fatalf("vocabulary endpoints = %q, %q", got[0], got[len(got)-1])
	}
}

func encodeGGUF(t *testing.T, kvs map[string]any) []byte {
	t.Helper()
	buf := new(bytes.Buffer)
	buf.WriteString("GGUF")
	binary.Write(buf, binary.LittleEndian, uint32(3))
	binary.Write(buf, binary.LittleEndian, uint64(0)) // no tensors
	binary.Write(buf, binary.LittleEndian, uint64(len(kvs)))
	for k, v := range kvs {
		writeString(buf, k)
		writeValue(t, buf, v)
	}
	return buf.Bytes()
}

func encodeGGUFValue(t *testing.T, key string, write func(*bytes.Buffer)) []byte {
	t.Helper()
	buf := new(bytes.Buffer)
	buf.WriteString("GGUF")
	binary.Write(buf, binary.LittleEndian, uint32(3))
	binary.Write(buf, binary.LittleEndian, uint64(0))
	binary.Write(buf, binary.LittleEndian, uint64(1))
	writeString(buf, key)
	write(buf)
	return buf.Bytes()
}

func writeString(buf *bytes.Buffer, s string) {
	binary.Write(buf, binary.LittleEndian, uint64(len(s)))
	buf.WriteString(s)
}

func writeValue(t *testing.T, buf *bytes.Buffer, v any) {
	t.Helper()
	switch x := v.(type) {
	case string:
		binary.Write(buf, binary.LittleEndian, ggufString)
		writeString(buf, x)
	case uint64:
		binary.Write(buf, binary.LittleEndian, ggufUint64)
		binary.Write(buf, binary.LittleEndian, x)
	case int64:
		binary.Write(buf, binary.LittleEndian, ggufInt64)
		binary.Write(buf, binary.LittleEndian, x)
	case []string:
		binary.Write(buf, binary.LittleEndian, ggufArray)
		binary.Write(buf, binary.LittleEndian, ggufString)
		binary.Write(buf, binary.LittleEndian, uint64(len(x)))
		for _, item := range x {
			writeString(buf, item)
		}
	default:
		t.Fatalf("unsupported fixture type %T", v)
	}
}
