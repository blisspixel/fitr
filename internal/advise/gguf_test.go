package advise

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
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
	default:
		t.Fatalf("unsupported fixture type %T", v)
	}
}
