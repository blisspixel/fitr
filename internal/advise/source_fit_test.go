package advise

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/source"
)

type sourceFitTransport func(*http.Request) (*http.Response, error)

func (transport sourceFitTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func sourceFitResolution(t *testing.T, names []string, sizes []int64) source.Resolution {
	t.Helper()
	siblings := []map[string]any{}
	for index, name := range names {
		siblings = append(siblings, map[string]any{"rfilename": name, "size": sizes[index],
			"blobId": strings.Repeat("b", 40), "lfs": map[string]any{
				"size": sizes[index], "sha256": strings.Repeat("c", 64), "pointerSize": 130}})
	}
	body, err := json.Marshal(map[string]any{"id": "owner/model", "sha": strings.Repeat("a", 40), "siblings": siblings})
	if err != nil {
		t.Fatal(err)
	}
	resolver := source.NewResolver(sourceFitTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header),
			Body: io.NopCloser(strings.NewReader(string(body)))}, nil
	}))
	resolution, err := resolver.ResolveHF(context.Background(), source.HFRequest{
		RepoID: "owner/model", Revision: "main", Files: names})
	if err != nil {
		t.Fatal(err)
	}
	if err := resolution.Validate(); err != nil || resolution.State != "resolved" {
		t.Fatalf("invalid fixture resolution: %v, %+v", err, resolution)
	}
	return resolution
}

func sourceFitMetadata() map[string]any {
	return map[string]any{"general.architecture": "llama", "llama.block_count": uint64(2),
		"llama.attention.head_count": uint64(8), "llama.attention.head_count_kv": uint64(2),
		"llama.attention.key_length": uint64(128), "llama.attention.value_length": uint64(128),
		"llama.context_length": uint64(8192)}
}

func sourceFitHeader(t *testing.T, name string, kvs map[string]any) SourceHeader {
	t.Helper()
	body := encodeGGUF(t, kvs)
	return SourceHeader{Path: name, Body: body, Observation: source.PrefixObservation{
		Outcome: "resolved", Bytes: len(body), RequestedHost: "huggingface.co", Host: "huggingface.co"}}
}

func sourceFitRequest() SourceFitRequest {
	return SourceFitRequest{Context: 4096, CapacityBytes: 8 * GiB, CapacitySource: "operator component ceiling"}
}

func TestSourceFitRefusesContradictoryResponseSize(t *testing.T) {
	resolution := sourceFitResolution(t, []string{"model.gguf"}, []int64{GiB})
	header := sourceFitHeader(t, "model.gguf", sourceFitMetadata())
	total := int64(2 * GiB)
	header.Observation.TotalBytes = &total
	report := ProjectSourceFit(resolution, []SourceHeader{header}, sourceFitRequest())
	if report.ProjectionStatus != "unresolved" || report.ComponentBytes != nil {
		t.Fatalf("response size contradicted weights but cleared: %+v", report)
	}
}

func TestSourceFitUsesOneCompleteShardWeightTotal(t *testing.T) {
	names := []string{"model-00001-of-00002.gguf", "model-00002-of-00002.gguf"}
	resolution := sourceFitResolution(t, names, []int64{2 * GiB, 3 * GiB})
	headers := []SourceHeader{}
	for index, name := range names {
		kvs := sourceFitMetadata()
		kvs["split.no"], kvs["split.count"] = uint64(index), uint64(len(names))
		headers = append(headers, sourceFitHeader(t, name, kvs))
	}
	request := sourceFitRequest()
	request.CapacityBytes = 4 * GiB
	report := ProjectSourceFit(resolution, headers, request)
	if report.ProjectionStatus != "exceeds_ceiling" || report.WeightsBytes == nil || *report.WeightsBytes != 5*GiB {
		t.Fatalf("a shard was projected as a whole model: %+v", report)
	}
	if report.CacheBytes == nil || *report.CacheBytes != 2*2*256*2*4096 {
		t.Fatalf("cache was duplicated or disagrees with conventional arithmetic: %+v", report)
	}
	if report.RuntimeStatus != "unmeasured" || report.ArchitectureStatus != "available" {
		t.Fatalf("component evidence was confused with runtime support: %+v", report)
	}
}

func TestSourceFitTruncatedMetadataCannotClearAGate(t *testing.T) {
	resolution := sourceFitResolution(t, []string{"model.gguf"}, []int64{GiB})
	header := sourceFitHeader(t, "model.gguf", sourceFitMetadata())
	// The selected prefix contains every conventional key, but its declared
	// metadata schedule commits to a further unread key that may change layout.
	count := binary.LittleEndian.Uint64(header.Body[16:24])
	binary.LittleEndian.PutUint64(header.Body[16:24], count+1)
	report := ProjectSourceFit(resolution, []SourceHeader{header}, sourceFitRequest())
	if report.ArchitectureStatus != "unresolved" || report.ComponentBytes != nil ||
		report.Shape == nil || report.Shape.Name != "llama" || report.Shape.KVSizable {
		t.Fatalf("truncation became projection evidence: %+v", report)
	}
}

func TestSourceFitRejectsIncompleteAndIndependentSelections(t *testing.T) {
	for _, names := range [][]string{{"model-00001-of-00002.gguf"}, {"first.gguf", "second.gguf"},
		{"model.gguf", "mmproj.gguf"}, {"weights.safetensors"}} {
		sizes := make([]int64, len(names))
		headers := []SourceHeader{}
		for index, name := range names {
			sizes[index] = GiB
			headers = append(headers, sourceFitHeader(t, name, sourceFitMetadata()))
		}
		report := ProjectSourceFit(sourceFitResolution(t, names, sizes), headers, sourceFitRequest())
		if report.ComponentBytes != nil || report.ArchitectureStatus != "unresolved" {
			t.Fatalf("selection %v was promoted: %+v", names, report)
		}
	}
}

func TestSourceFitRequiresContextAndCeilingEvidence(t *testing.T) {
	resolution := sourceFitResolution(t, []string{"model.gguf"}, []int64{GiB})
	header := sourceFitHeader(t, "model.gguf", sourceFitMetadata())
	for _, request := range []SourceFitRequest{{Context: -1}, {Context: 16384},
		{Context: 4096}, {Context: 4096, CapacityBytes: 8 * GiB}} {
		report := ProjectSourceFit(resolution, []SourceHeader{header}, request)
		if report.ProjectionStatus != "unresolved" || report.ProjectionReason == "" {
			t.Fatalf("missing or invalid evidence cleared a comparison: %+v", report)
		}
	}
	request := sourceFitRequest()
	request.Context = 0
	report := ProjectSourceFit(resolution, []SourceHeader{header}, request)
	if report.ProjectionStatus != "within_ceiling" || report.Context != 8192 {
		t.Fatalf("declared context maximum was not retained: %+v", report)
	}
}

func TestSourceFitRejectsMalformedOrMismatchedHeaderEvidence(t *testing.T) {
	resolution := sourceFitResolution(t, []string{"model.gguf"}, []int64{GiB})
	valid := sourceFitHeader(t, "model.gguf", sourceFitMetadata())
	for _, mutate := range []func(*SourceHeader){
		func(h *SourceHeader) { h.Path = "another.gguf" },
		func(h *SourceHeader) { h.Observation.Outcome = "cancelled" },
		func(h *SourceHeader) { h.Observation.Bytes-- },
		func(h *SourceHeader) { h.Body = []byte("NOPE"); h.Observation.Bytes = len(h.Body) },
	} {
		header := valid
		mutate(&header)
		report := ProjectSourceFit(resolution, []SourceHeader{header}, sourceFitRequest())
		if report.ComponentBytes != nil || report.ArchitectureStatus != "unresolved" {
			t.Fatalf("invalid header evidence cleared architecture: %+v", report)
		}
	}
	for _, headers := range [][]SourceHeader{nil, {valid, valid}} {
		if report := ProjectSourceFit(resolution, headers, sourceFitRequest()); report.ComponentBytes != nil {
			t.Fatalf("wrong header count produced a component total: %+v", report)
		}
	}
}

func TestSourceFitRejectsChangedReceiptsAndUnsizableLayouts(t *testing.T) {
	resolution := sourceFitResolution(t, []string{"model.gguf"}, []int64{GiB})
	changed := resolution
	changed.ResolvedCommit = strings.Repeat("d", 40)
	if report := ProjectSourceFit(changed, nil, sourceFitRequest()); !strings.Contains(report.ProjectionReason, "validate") {
		t.Fatalf("a modified receipt reached projection: %+v", report)
	}
	for _, kvs := range []map[string]any{{}, {"general.architecture": "llama"}, sourceFitMetadata()} {
		if len(kvs) > 1 {
			kvs["llama.attention.sliding_window"] = uint64(1024)
		}
		report := ProjectSourceFit(resolution, []SourceHeader{sourceFitHeader(t, "model.gguf", kvs)}, sourceFitRequest())
		if report.ArchitectureStatus != "unresolved" || report.ComponentBytes != nil {
			t.Fatalf("unsupported layout cleared a gate: %+v", report)
		}
	}
}

func TestSourceFitRejectsOverflow(t *testing.T) {
	names := []string{}
	sizes := []int64{}
	headers := []SourceHeader{}
	for index := range 9 {
		name := fmt.Sprintf("model-%05d-of-00009.gguf", index+1)
		names, sizes = append(names, name), append(sizes, 1<<60)
		kvs := sourceFitMetadata()
		kvs["split.no"], kvs["split.count"] = uint64(index), uint64(9)
		headers = append(headers, sourceFitHeader(t, name, kvs))
	}
	resolution := sourceFitResolution(t, names, sizes)
	if report := ProjectSourceFit(resolution, headers, sourceFitRequest()); !strings.Contains(report.ProjectionReason, "overflow") {
		t.Fatalf("weight total overflow was not refused: %+v", report)
	}
}

func TestSourceFitRejectsDisagreeingShards(t *testing.T) {
	names := []string{"model-00001-of-00002.gguf", "model-00002-of-00002.gguf"}
	resolution := sourceFitResolution(t, names, []int64{GiB, GiB})
	headers := []SourceHeader{}
	for index, name := range names {
		kvs := sourceFitMetadata()
		kvs["split.no"], kvs["split.count"] = uint64(index), uint64(2)
		headers = append(headers, sourceFitHeader(t, name, kvs))
	}
	kvs := sourceFitMetadata()
	kvs["llama.block_count"] = uint64(3)
	kvs["split.no"], kvs["split.count"] = uint64(1), uint64(2)
	headers[1] = sourceFitHeader(t, names[1], kvs)
	if report := ProjectSourceFit(resolution, headers, sourceFitRequest()); !strings.Contains(report.ProjectionReason, "disagree") {
		t.Fatalf("disagreeing shard architectures were combined: %+v", report)
	}
	headers[1] = sourceFitHeader(t, names[1], sourceFitMetadata())
	if report := ProjectSourceFit(resolution, headers, sourceFitRequest()); !strings.Contains(report.ProjectionReason, "split metadata") {
		t.Fatalf("filename alone established shard identity: %+v", report)
	}
}

func TestSourceFitAcceptsSplitOnlyMetadataOnLaterShards(t *testing.T) {
	// llama.cpp 4a89937354190cef5a97baf8eeb17336105eb72d
	// GGUFWriter.add_shard_kv_data places the architecture only in shard zero.
	names := []string{"model-00001-of-00002.gguf", "model-00002-of-00002.gguf"}
	resolution := sourceFitResolution(t, names, []int64{GiB, GiB})
	first := sourceFitMetadata()
	first["split.no"], first["split.count"] = uint64(0), uint64(2)
	later := map[string]any{"split.no": uint64(1), "split.count": uint64(2), "split.tensors.count": int64(100)}
	headers := []SourceHeader{sourceFitHeader(t, names[0], first), sourceFitHeader(t, names[1], later)}
	report := ProjectSourceFit(resolution, headers, sourceFitRequest())
	if report.ProjectionStatus != "within_ceiling" || report.WeightsBytes == nil || *report.WeightsBytes != 2*GiB {
		t.Fatalf("upstream split-only metadata was refused: %+v", report)
	}
}

func TestRecordedQwenPrefixCannotEstablishArchitectureReadiness(t *testing.T) {
	// Captured from official Qwen/Qwen3-0.6B-GGUF at pinned commit
	// 23749fefcc72300e3a2ad315e1317431b06b590a on 2026-09-13. Provenance
	// and acquisition bounds are recorded in docs/source-resolution.md.
	fixturePath, err := filepath.Abs("../source/testdata/qwen3-0.6b-resolution.json")
	if err != nil {
		t.Fatal(err)
	}
	resolution, err := source.LoadResolution(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile("../source/testdata/qwen3-0.6b-prefix.gguf")
	if err != nil {
		t.Fatal(err)
	}
	header := SourceHeader{Path: resolution.Files[0].Path, Body: body, Observation: source.PrefixObservation{
		Outcome: "resolved", Bytes: len(body), RequestedHost: "huggingface.co", Host: "us.aws.cdn.hf.co",
		HTTPStatus: http.StatusPartialContent, Redirects: 1}}
	report := ProjectSourceFit(resolution, []SourceHeader{header}, sourceFitRequest())
	if report.Shape == nil || report.Shape.Name != "qwen3" || report.Shape.KVSizable ||
		report.ArchitectureStatus != "unresolved" || report.ComponentBytes != nil || report.RuntimeStatus != "unmeasured" {
		t.Fatalf("recorded real prefix was promoted beyond its bytes: %+v", report)
	}
}

func TestSourceFitHonorsExplicitHeaderReadBounds(t *testing.T) {
	resolution := sourceFitResolution(t, []string{"model.gguf"}, []int64{GiB})
	header := sourceFitHeader(t, "model.gguf", sourceFitMetadata())
	header.Body = append(header.Body, make([]byte, source.MaxArtifactPrefixBytes)...)
	header.Observation.Bytes = len(header.Body)
	for _, bound := range []int{0, source.MaxArtifactPrefixBytes, -1, source.MaxScreeningPrefixBytes + 1} {
		header.Observation.RequestedBytes = bound
		if report := ProjectSourceFit(resolution, []SourceHeader{header}, sourceFitRequest()); report.ComponentBytes != nil {
			t.Fatalf("prefix exceeded authorization %d: %+v", bound, report)
		}
	}
	header.Observation.RequestedBytes = 1 << 20
	report := ProjectSourceFit(resolution, []SourceHeader{header}, sourceFitRequest())
	if report.ProjectionStatus != "within_ceiling" || report.SourceSHA256 != resolution.ResolutionSHA256 {
		t.Fatalf("explicit larger observation lost its source binding: %+v", report)
	}
}
