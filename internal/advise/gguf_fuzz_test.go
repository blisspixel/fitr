package advise

import (
	"bytes"
	"encoding/binary"
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
	valid := func(kvs map[string]any) []byte {
		buf := new(bytes.Buffer)
		buf.WriteString("GGUF")
		_ = binary.Write(buf, binary.LittleEndian, uint32(3))
		_ = binary.Write(buf, binary.LittleEndian, uint64(0))
		_ = binary.Write(buf, binary.LittleEndian, uint64(len(kvs)))
		for k, v := range kvs {
			writeString(buf, k)
			switch n := v.(type) {
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
				b := byte(0)
				if n {
					b = 1
				}
				buf.WriteByte(b)
			}
		}
		return buf.Bytes()
	}

	f.Add(valid(map[string]any{
		"general.architecture":          "llama",
		"llama.block_count":             uint64(32),
		"llama.attention.head_count_kv": uint64(8),
		"llama.attention.key_length":    uint64(128),
		"llama.context_length":          uint64(131072),
		"llama.embedding_length":        uint64(4096),
	}))
	f.Add(valid(map[string]any{
		"general.architecture":         "qwen3moe",
		"qwen3moe.expert_count":        uint64(128),
		"qwen3moe.rope.scaling.factor": float32(4),
		"general.some_flag":            true,
	}))
	f.Add(valid(map[string]any{"general.architecture": "mamba"}))
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
		kvs, err := ReadMetadata(bytes.NewReader(data))
		if err != nil {
			if kvs != nil {
				t.Fatalf("failed decode returned %d keys; a rejected file must yield nothing", len(kvs))
			}
			return
		}
		if kvs == nil {
			t.Fatal("successful decode returned a nil map")
		}
		if len(kvs) > maxKVs {
			t.Fatalf("decoded %d keys, over the %d cap", len(kvs), maxKVs)
		}
		// The consumer must survive whatever parsed.
		a := ArchFromKVs(kvs)
		if a.Blocks < 0 || a.Embed < 0 || a.Heads < 0 || a.KVHeads < 0 ||
			a.KeyLength < 0 || a.MaxCtx < 0 || a.Experts < 0 || a.ExpertUsed < 0 {
			t.Fatalf("negative architecture field from parsed metadata: %+v", a)
		}
		if a.KVReady() {
			// A ready architecture is one advise will divide by; it must not
			// hand back a zero or negative per-token cost.
			if got := a.kvBytesPerToken(2); got <= 0 {
				t.Fatalf("KVReady architecture reports %v bytes per token: %+v", got, a)
			}
		}
	})
}
