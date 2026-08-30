package advise

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
)

// A GGUF header is the largest untrusted binary surface fitr has: `fitr advise
// ./model.gguf` parses a file the user did not write, with attacker-influenced
// lengths, counts and type tags driving every read and allocation. The decoder
// carries budgets for exactly that reason, so the budgets need a fuzzer.
//
// The target covers the consumer too. ArchFromKVs reads the decoded map and
// does the arithmetic advise depends on, so a value that parses but cannot be
// reasoned about must still not panic or produce a nonsense architecture.
func FuzzReadMetadata(f *testing.F) {
	f.Add(validFuzzGGUF(map[string]any{
		"general.architecture":          "llama",
		"llama.block_count":             uint64(32),
		"llama.attention.head_count_kv": uint64(8),
		"llama.attention.key_length":    uint64(128),
		"llama.context_length":          uint64(131072),
		"llama.embedding_length":        uint64(4096),
	}))
	f.Add(validFuzzGGUF(map[string]any{
		"general.architecture":         "qwen3moe",
		"qwen3moe.expert_count":        uint64(128),
		"qwen3moe.rope.scaling.factor": float32(4),
		"general.some_flag":            true,
	}))
	f.Add(validFuzzGGUF(map[string]any{"general.architecture": "mamba"}))
	f.Add([]byte("GGUF"))
	f.Add([]byte{})
	// Header claiming counts it does not deliver: the budget path, not the
	// happy path, is what a hostile file exercises.
	hdr := new(bytes.Buffer)
	hdr.WriteString("GGUF")
	_ = binary.Write(hdr, binary.LittleEndian, uint32(3))
	_ = binary.Write(hdr, binary.LittleEndian, ^uint64(0))
	_ = binary.Write(hdr, binary.LittleEndian, ^uint64(0))
	f.Add(hdr.Bytes())

	f.Fuzz(func(t *testing.T, data []byte) {
		if !validateFullFuzzDecode(t, data) {
			return
		}

		// The prefix reader is a second entry point for the same hostile bytes,
		// and it is the one a remote candidate arrives through: fitr will point
		// it at 4 KiB fetched over HTTP from a repo it does not control. It
		// must hold every invariant the all-or-nothing reader holds, and it
		// must additionally never report corruption as truncation.
		validatePrefixFuzzDecode(t, data)
	})
}

func validFuzzGGUF(kvs map[string]any) []byte {
	buf := new(bytes.Buffer)
	buf.WriteString("GGUF")
	_ = binary.Write(buf, binary.LittleEndian, uint32(3))
	_ = binary.Write(buf, binary.LittleEndian, uint64(0))
	_ = binary.Write(buf, binary.LittleEndian, uint64(len(kvs)))
	for key, value := range kvs {
		writeString(buf, key)
		writeFuzzKV(buf, value)
	}
	return buf.Bytes()
}

func writeFuzzKV(buf *bytes.Buffer, value any) {
	switch n := value.(type) {
	case string:
		_ = binary.Write(buf, binary.LittleEndian, ggufString)
		writeString(buf, n)
	case uint64:
		_ = binary.Write(buf, binary.LittleEndian, ggufUint64)
		_ = binary.Write(buf, binary.LittleEndian, n)
	case uint32:
		_ = binary.Write(buf, binary.LittleEndian, ggufUint32)
		_ = binary.Write(buf, binary.LittleEndian, n)
	case float32:
		_ = binary.Write(buf, binary.LittleEndian, ggufFloat32)
		_ = binary.Write(buf, binary.LittleEndian, n)
	case bool:
		_ = binary.Write(buf, binary.LittleEndian, ggufBool)
		if n {
			buf.WriteByte(1)
		} else {
			buf.WriteByte(0)
		}
	}
}

func validateFullFuzzDecode(t *testing.T, data []byte) bool {
	t.Helper()
	kvs, err := ReadMetadata(bytes.NewReader(data))
	if err != nil {
		if kvs != nil {
			t.Fatalf("failed decode returned %d keys; a rejected file must yield nothing", len(kvs))
		}
		return false
	}
	if kvs == nil {
		t.Fatal("successful decode returned a nil map")
	}
	validateFuzzArchitecture(t, kvs, "parsed metadata")
	return true
}

func validatePrefixFuzzDecode(t *testing.T, data []byte) {
	t.Helper()
	kvs, err := ReadMetadataPrefix(bytes.NewReader(data))
	switch {
	case err == nil:
		if kvs == nil {
			t.Fatal("prefix decode succeeded with a nil map")
		}
	case errors.Is(err, ErrMetadataTruncated):
		if kvs != nil && len(kvs) == 0 {
			t.Fatal("truncated decode returned an empty non-nil map")
		}
	default:
		if kvs != nil {
			t.Fatalf("rejected file returned %d keys from the prefix reader", len(kvs))
		}
	}
	validateFuzzArchitecture(t, kvs, "a prefix decode")
}

func validateFuzzArchitecture(t *testing.T, kvs map[string]any, source string) {
	t.Helper()
	if len(kvs) > maxKVs {
		t.Fatalf("%s decoded %d keys, over the %d cap", source, len(kvs), maxKVs)
	}
	arch := ArchFromKVs(kvs)
	if arch.Blocks < 0 || arch.Embed < 0 || arch.Heads < 0 || arch.KVHeads < 0 ||
		arch.KeyLength < 0 || arch.MaxCtx < 0 || arch.Experts < 0 || arch.ExpertUsed < 0 {
		t.Fatalf("negative architecture field from %s: %+v", source, arch)
	}
	if arch.KVReady() {
		if got := arch.kvBytesPerToken(2); got <= 0 {
			t.Fatalf("KVReady architecture from %s reports %v bytes per token: %+v", source, got, arch)
		}
	}
}
