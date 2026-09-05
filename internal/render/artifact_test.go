package render

import (
	"bytes"
	"strconv"
	"strings"
	"testing"

	"github.com/clipperhouse/displaywidth"

	"github.com/blisspixel/fitr/internal/artifact"
	"github.com/blisspixel/fitr/internal/source"
)

func TestArtifactObservationKeepsIndependentStatesAndWidth(t *testing.T) {
	for _, width := range []int{40, 80, 120} {
		t.Run(strconv.Itoa(width), func(t *testing.T) {
			t.Setenv("FITR_WIDTH", strconv.Itoa(width))
			t.Setenv("NO_COLOR", "1")
			var report bytes.Buffer
			WriteArtifactBinding(&report, artifact.Binding{State: "matched", BytesRead: 16 << 30,
				Source: source.Resolution{Request: source.HFRequest{RepoID: "owner/model\x1b[2J"}, ResolvedCommit: strings.Repeat("a", 40)},
				Limits: artifact.Limits{MaxBytes: 64 << 30}, DependencyState: "unverified", RuntimeState: "unbound", CapacityState: "unmeasured", QualityState: "unmeasured",
				Files:         []artifact.FileObservation{{SourcePath: "model.gguf", LocalPath: "/models/" + strings.Repeat("模型", 40), ComponentRole: "weights", State: "matched", ObservedSHA256: "sha256:" + strings.Repeat("b", 64)}},
				UnmappedFiles: []string{"projector.gguf"}, Gaps: []string{"dependency_closure_unverified"},
			}, "plain")
			text := report.String()
			for _, fact := range []string{"LOCAL BYTES MATCHED", "projector.gguf", "unbound", "unmeasured", "unverified", "16.00 GiB", "17179869184 B", "68719476736 B"} {
				if !strings.Contains(text, fact) {
					t.Fatalf("missing observation fact %q", fact)
				}
			}
			if strings.Contains(text, "\x1b") {
				t.Fatal("artifact output leaked terminal controls")
			}
			for _, line := range strings.Split(text, "\n") {
				if displaywidth.String(line) > width {
					t.Fatalf("artifact line exceeds %d columns: %s", width, line)
				}
			}
		})
	}
}

func TestArtifactObservationExplainsSingleByteDifferences(t *testing.T) {
	for _, state := range []string{"size_mismatch", "budget_exceeded"} {
		t.Run(state, func(t *testing.T) {
			t.Setenv("FITR_WIDTH", "120")
			t.Setenv("NO_COLOR", "1")
			limit := int64(1 << 30)
			binding := artifact.Binding{
				State: state, Limits: artifact.Limits{MaxBytes: limit},
				Source: source.Resolution{Files: []source.FileMetadata{{Path: "model.gguf", SizeBytes: &limit}}},
				Files: []artifact.FileObservation{{SourcePath: "model.gguf", State: state,
					Before: &artifact.FileFacts{SizeBytes: limit + 1}}},
			}
			var report bytes.Buffer
			WriteArtifactBinding(&report, binding, "plain")
			for _, fact := range []string{"0 B observed", "1.00 GiB (1073741824 B) limit", "1.00 GiB (1073741825 B) observed"} {
				if !strings.Contains(report.String(), fact) {
					t.Fatalf("missing exact byte evidence %q:\n%s", fact, report.String())
				}
			}
			if state == "size_mismatch" && !strings.Contains(report.String(), "1.00 GiB (1073741824 B) declared by source") {
				t.Fatalf("expected source size is rounded ambiguously:\n%s", report.String())
			}
		})
	}
}

func TestArtifactObservationRetainsDependencyEvidence(t *testing.T) {
	for _, width := range []int{40, 80, 120} {
		t.Run(strconv.Itoa(width), func(t *testing.T) {
			t.Setenv("FITR_WIDTH", strconv.Itoa(width))
			t.Setenv("NO_COLOR", "1")
			binding := artifact.Binding{
				State: "matched", Limits: artifact.Limits{MaxBytes: 64 << 30},
				Source: source.Resolution{Dependencies: []source.DependencyFinding{
					{Kind: "shard", Status: "missing", SourceFile: "Q4_K_M-01.gguf", TargetFile: "Q4_K_M-02.gguf", Basis: "numbered_filename"},
					{Kind: "shard", Status: "unselected", SourceFile: "Q4_K_M-01.gguf", TargetFile: "Q4_K_M-03.gguf", Basis: "numbered_filename"},
					{Kind: "projector", Status: "candidate", TargetFile: "mmproj_Q4.gguf\x1b[2J\r\n\t", Basis: "filename_only"},
					{Kind: "encoder", Status: "unknown", Basis: "not_inspected"},
				}},
				Gaps: []string{"file:Q4_K_M.gguf:missing"}, DependencyState: "unverified",
				RuntimeState: "unbound", CapacityState: "unmeasured", QualityState: "unmeasured",
			}
			for _, mode := range []string{"plain", "rich"} {
				var report bytes.Buffer
				WriteArtifactBinding(&report, binding, mode)
				assertArtifactDependencyReport(t, report.String(), width)
			}
		})
	}
}

func assertArtifactDependencyReport(t *testing.T, report string, width int) {
	t.Helper()
	for _, fact := range []string{"shard: missing", "shard: unselected", "projector: candidate", "encoder: unknown",
		"source", "target", "Q4_K_M-01.gguf", "Q4_K_M-02.gguf", "Q4_K_M-03.gguf", "mmproj_Q4.gguf",
		"numbered_filename", "filename_only", "not_inspected", "file:Q4_K_M.gguf:missing", "unverified", "unbound", "unmeasured"} {
		if !strings.Contains(report, fact) {
			t.Fatalf("dependency evidence %q disappeared at width %d:\n%s", fact, width, report)
		}
	}
	if strings.ContainsAny(report, "\x1b\r\t") {
		t.Fatalf("dependency evidence leaked terminal controls:\n%s", report)
	}
	for _, line := range strings.Split(report, "\n") {
		if displaywidth.String(line) > width {
			t.Fatalf("dependency evidence exceeds %d columns: %s", width, line)
		}
	}
}
