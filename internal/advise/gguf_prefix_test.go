package advise

import (
	"bytes"
	"errors"
	"os"
	"testing"
)

// The first 4 KiB of a real 5 GB GGUF, captured with an HTTP range request:
//
//	curl -r 0-4095 .../unsloth/Qwen3-8B-GGUF/resolve/main/Qwen3-8B-Q4_K_M.gguf
//
// This is the whole discovery premise: a candidate can be sized exactly without
// downloading it, reusing the fit math rather than inventing a second one.
const prefixFixture = "testdata/qwen3-8b-head-4k.bin"

func TestPrefixReadSizesAModelWithoutDownloadingIt(t *testing.T) {
	b, err := os.ReadFile(prefixFixture)
	if err != nil {
		t.Fatalf("fixture missing: %v", err)
	}

	// ReadMetadata is all-or-nothing and must stay that way: a short GGUF on
	// disk is a damaged GGUF, and returning half of one would be worse.
	if kvs, err := ReadMetadata(bytes.NewReader(b)); err == nil || kvs != nil {
		t.Errorf("ReadMetadata returned %d keys and err=%v; it must discard a "+
			"truncated header entirely", len(kvs), err)
	}

	kvs, err := ReadMetadataPrefix(bytes.NewReader(b))
	if !errors.Is(err, ErrMetadataTruncated) {
		t.Fatalf("err = %v, want ErrMetadataTruncated", err)
	}
	if len(kvs) == 0 {
		t.Fatal("no keys recovered from a real header prefix")
	}

	arch := ArchFromKVs(kvs)
	if arch.Name == "" {
		t.Error("architecture name not recovered")
	}
	// These are the fields the KV-cache arithmetic needs. If a 4 KiB read
	// cannot supply them, the whole approach fails and this test says so.
	if !arch.KVReady() {
		t.Errorf("arch is not KV-ready from a 4 KiB prefix: %+v", arch)
	}
	t.Logf("arch=%s layers=%d kv_heads=%d ctx=%d",
		arch.Name, arch.Blocks, arch.KVHeads, arch.MaxCtx)
}

// Truncation and corruption must not share an error path. One is the expected
// outcome of a deliberately bounded read; the other is a file to refuse.
func TestPrefixReadDistinguishesTruncationFromCorruption(t *testing.T) {
	if _, err := ReadMetadataPrefix(bytes.NewReader([]byte("NOPE"))); err == nil ||
		errors.Is(err, ErrMetadataTruncated) {
		t.Errorf("a non-GGUF file read as truncated: %v", err)
	}
	// Too few bytes to reach a single key is truncation with nothing to show,
	// and must not be reported as a usable partial result.
	kvs, err := ReadMetadataPrefix(bytes.NewReader([]byte("GGUF")))
	if kvs != nil {
		t.Errorf("returned %d keys from four bytes", len(kvs))
	}
	if err == nil {
		t.Error("four bytes decoded without error")
	}
}
