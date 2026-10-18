package discovery

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/analysis"
	"github.com/blisspixel/fitr/internal/render"
	"github.com/blisspixel/fitr/internal/source"
)

func TestClearScreenAndReadyPlanLeaveInstallUnmeasured(t *testing.T) {
	var buf bytes.Buffer
	render.WriteSourceScreen(&buf, clearInstallScreen(t))
	plan := readyInstallPlan(t)
	for _, step := range plan.Steps {
		fmt.Fprintln(&buf, step.Text)
	}
	got := buf.String()
	if !strings.Contains(got, "install command is unmeasured") || strings.Contains(got, "Download ") || inventedInstallCommand(got) {
		t.Fatalf("install line:\n%s", got)
	}
	if !strings.Contains(got, "does not establish runtime support") {
		t.Fatalf("screen limits dropped:\n%s", got)
	}
}

func inventedInstallCommand(text string) bool {
	for _, bad := range []string{"ollama pull", "llama-server", "hf.co", "huggingface-cli", "wget ", "curl ", "fitr artifact bind --"} {
		if strings.Contains(text, bad) {
			return true
		}
	}
	return false
}

func clearInstallScreen(t *testing.T) analysis.SourceScreen {
	t.Helper()
	metadata := fmt.Sprintf(`{"id":"owner/model","author":"owner","sha":%q,"cardData":{"license":"apache-2.0","base_model":"owner/base"},"siblings":[{"rfilename":"model.gguf","size":1024,"lfs":{"size":1024,"sha256":%q}}]}`,
		strings.Repeat("a", 40), strings.Repeat("b", 64))
	resolver := source.NewResolver(screenRoundTrip(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(metadata))}, nil
	}))
	resolution, err := resolver.ResolveHF(context.Background(), source.HFRequest{RepoID: "owner/model", Revision: "main", Files: []string{"model.gguf"}})
	if err != nil {
		t.Fatal(err)
	}
	policy := analysis.SourceScreenPolicy{AllowedLicenses: []string{"apache-2.0"}, AllowedArchitectures: []string{"llama"},
		Context: 4096, ComponentCeilingBytes: 8 << 30}
	facts := analysis.SourceScreenFacts{SourceSHA256: resolution.ResolutionSHA256, Context: 4096, CeilingBytes: 8 << 30,
		Architecture: "llama", ArchitectureStatus: "available", ProjectionStatus: "within_ceiling",
		ProjectionReason: "Declared weights plus f16 cache are within the component ceiling; runtime allocation is unmeasured."}
	report, err := analysis.AnalyzeSourceScreen(resolution, policy, facts)
	if err != nil || report.State != "clear" {
		t.Fatalf("screen %+v %v", report, err)
	}
	return report
}

type screenRoundTrip func(*http.Request) (*http.Response, error)

func (trip screenRoundTrip) RoundTrip(request *http.Request) (*http.Response, error) {
	return trip(request)
}

func readyInstallPlan(t *testing.T) SourceProposal {
	t.Helper()
	size := int64(2048)
	sha := "sha256:" + strings.Repeat("c", 64)
	receipt := source.Resolution{
		State:            "resolved",
		ResolutionSHA256: "sha256:" + strings.Repeat("d", 64),
		Files:            []source.FileMetadata{{Path: "model.gguf", State: "present", SizeBytes: &size, DeclaredSHA256: sha}},
	}
	plan, err := buildSourcePlan(Idea{ID: "idea", Role: "coding", State: "unmeasured"}, []SourceAttachment{{
		ResolutionSHA256: receipt.ResolutionSHA256,
		Resolution:       receipt,
	}}, "")
	if err != nil || plan.DownloadPlan == nil || plan.DownloadPlan.Status != "ready" {
		t.Fatalf("plan %+v %v", plan.DownloadPlan, err)
	}
	return plan
}
