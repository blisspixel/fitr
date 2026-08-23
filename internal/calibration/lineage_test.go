package calibration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/eval"
)

func testDigest(seed byte) string {
	return "sha256:" + strings.Repeat(hexByte(seed), 32)
}

func hexByte(seed byte) string {
	const digits = "0123456789abcdef"
	return string([]byte{digits[seed>>4], digits[seed&0x0f]})
}

func conversionFor(base, ref, cand string) ConversionManifest {
	return ConversionManifest{
		Schema: ConversionSchema, BaseRevision: base,
		Artifacts: []ConversionArtifact{
			{Digest: base, Role: "base", Quant: "F16"},
			{Digest: ref, Role: "derived", Quant: "Q8_0"},
			{Digest: cand, Role: "derived", Quant: "Q4_K_M"},
		},
	}
}

func TestLineageFromConversionBindsBothArtifacts(t *testing.T) {
	base, ref, cand := testDigest(0x0a), testDigest(0x0b), testDigest(0x0c)
	receipt, err := LineageFromConversion(conversionFor(base, ref, cand), ref, cand)
	if err != nil {
		t.Fatal(err)
	}
	if err := receipt.Bind(ref, cand); err != nil {
		t.Fatal(err)
	}
	if receipt.Method != LineageConversion || receipt.BaseRevision != base {
		t.Fatalf("receipt = %+v", receipt)
	}
}

func TestLineageFromConversionRejectsOperatorShapedGaps(t *testing.T) {
	base, ref, cand, other := testDigest(0x11), testDigest(0x12), testDigest(0x13), testDigest(0x14)
	if _, err := LineageFromConversion(conversionFor(base, ref, cand), ref, other); err == nil ||
		!strings.Contains(err.Error(), "both pair artifacts") {
		t.Fatalf("unrelated candidate digest = %v", err)
	}
	if _, err := LineageFromConversion(conversionFor(base, ref, cand), ref, ref); err == nil ||
		!strings.Contains(err.Error(), "distinct") {
		t.Fatalf("identical artifacts = %v", err)
	}
	badRole := conversionFor(base, ref, cand)
	badRole.Artifacts[1].Role = "same-family"
	if _, err := LineageFromConversion(badRole, ref, cand); err == nil || !strings.Contains(err.Error(), "role") {
		t.Fatalf("invented role = %v", err)
	}
	markedWrong := conversionFor(base, ref, cand)
	markedWrong.Artifacts[1].Role = "base"
	if _, err := LineageFromConversion(markedWrong, ref, cand); err == nil {
		t.Fatal("derived blob marked base was accepted")
	}
}

func TestReadConversionManifestRejectsUnknownFieldsAndTrailingJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "conv.json")
	if err := os.WriteFile(path, []byte(`{"schema":"fitr.lineage.conversion.v1","base_revision":"`+testDigest(1)+`","artifacts":[{"digest":"`+testDigest(2)+`"},{"digest":"`+testDigest(3)+`"}],"guess":true}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadConversionManifest(path); err == nil || !strings.Contains(err.Error(), "guess") {
		t.Fatalf("unknown field = %v", err)
	}
	valid := `{"schema":"fitr.lineage.conversion.v1","base_revision":"` + testDigest(1) + `","artifacts":[{"digest":"` + testDigest(2) + `"},{"digest":"` + testDigest(3) + `"}]}`
	if err := os.WriteFile(path, []byte(valid+"\n{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadConversionManifest(path); err == nil || !strings.Contains(strings.ToLower(err.Error()), "after") {
		t.Fatalf("trailing JSON = %v", err)
	}
	duplicate := strings.Replace(valid, `"schema":`, `"schema":"shadow","schema":`, 1)
	if err := os.WriteFile(path, []byte(duplicate), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadConversionManifest(path); err == nil || !strings.Contains(err.Error(), "duplicate JSON object name") {
		t.Fatalf("duplicate JSON name = %v", err)
	}
}

func TestGGUFNameIsNotALineageDigest(t *testing.T) {
	named := map[string]any{"general.base_model.0.name": "Llama-3.1-8B", "general.base_model.0.repo_url": "https://huggingface.co/meta"}
	if _, err := LineageFromGGUF(named, named, testDigest(2), testDigest(3)); err == nil ||
		!strings.Contains(err.Error(), "without a content digest") {
		t.Fatalf("name/url metadata = %v", err)
	}
}

func TestLineageFromGGUFRequiresMatchingBaseDigests(t *testing.T) {
	base := testDigest(0x21)
	refKVs := map[string]any{"general.base_model.0.sha256": base}
	candKVs := map[string]any{"general.base_model.0.sha256": base}
	receipt, err := LineageFromGGUF(refKVs, candKVs, testDigest(0x22), testDigest(0x23))
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Method != LineageGGUFDigest || receipt.BaseRevision != base || receipt.GGUF.Key != "general.base_model.0.sha256" {
		t.Fatalf("receipt = %+v", receipt)
	}
	candKVs["general.base_model.0.sha256"] = testDigest(0x24)
	if _, err := LineageFromGGUF(refKVs, candKVs, testDigest(0x22), testDigest(0x23)); err == nil ||
		!strings.Contains(err.Error(), "different base-revision") {
		t.Fatalf("conflicting GGUF bases = %v", err)
	}
}

func TestAttachLineageRejectsAReceiptFromAnotherPair(t *testing.T) {
	base, ref, cand, other := testDigest(0x31), testDigest(0x32), testDigest(0x33), testDigest(0x34)
	receipt, err := LineageFromConversion(conversionFor(base, ref, cand), ref, cand)
	if err != nil {
		t.Fatal(err)
	}
	report := NewPair("0.5.0", 2, "seed", Device{ID: "1111111111111111"},
		Run{Model: "m-q8", Quant: "Q8_0", Family: "fam", ParameterSize: "8B", ResultSchemaVersion: 4, ArtifactDigest: other},
		Run{Model: "m-q4", Quant: "Q4_K_M", Family: "fam", ParameterSize: "8B", ResultSchemaVersion: 4, ArtifactDigest: cand},
		[]eval.ItemStat{{TaskID: "json", Family: "json", Need: "structured_output", Shared: 10, APass: 10, BPass: 9}})
	if err := report.AttachLineage(receipt); err == nil {
		t.Fatal("receipt from a different reference digest was attached")
	}
}

func TestPairJSONRoundTripPreservesLineage(t *testing.T) {
	base, ref, cand := testDigest(0x41), testDigest(0x42), testDigest(0x43)
	receipt, err := LineageFromConversion(conversionFor(base, ref, cand), ref, cand)
	if err != nil {
		t.Fatal(err)
	}
	report := NewPair("0.5.0", 2, "seed", Device{ID: "1111111111111111"},
		Run{Model: "m-q8", Quant: "Q8_0", Family: "fam", ParameterSize: "8B", ResultSchemaVersion: 4, ArtifactDigest: ref},
		Run{Model: "m-q4", Quant: "Q4_K_M", Family: "fam", ParameterSize: "8B", ResultSchemaVersion: 4, ArtifactDigest: cand},
		[]eval.ItemStat{{TaskID: "json", Family: "json", Need: "structured_output", Shared: 10, Flips: 1, APass: 10, BPass: 9}})
	if err := report.AttachLineage(receipt); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "pair.json")
	if err := WriteJSON(path, report); err != nil {
		t.Fatal(err)
	}
	got, err := ReadPair(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Lineage == nil || got.Lineage.Validate() != nil ||
		got.Lineage.Bind(got.Reference.ArtifactDigest, got.Candidate.ArtifactDigest) != nil {
		t.Fatalf("round-tripped lineage = %+v", got.Lineage)
	}
}

func TestTamperedConversionEvidenceIsRejected(t *testing.T) {
	base, ref, cand := testDigest(0x51), testDigest(0x52), testDigest(0x53)
	receipt, err := LineageFromConversion(conversionFor(base, ref, cand), ref, cand)
	if err != nil {
		t.Fatal(err)
	}
	receipt.Conversion.Artifacts[2].Quant = "Q3_K_S"
	if err := receipt.Validate(); err == nil || !strings.Contains(err.Error(), "evidence digest") {
		t.Fatalf("mutated conversion = %v", err)
	}
}
