package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/discovery"
	"github.com/blisspixel/fitr/internal/source"
)

func discoveryCLIIdea(t *testing.T) discovery.Idea {
	t.Helper()
	t.Setenv("FITR_RESULTS", sourceTestDirectory(t))
	t.Setenv("FITR_BACKEND", "invalid-must-not-be-used")
	output, code := captureTopStdout(t, func() int {
		return cmdDiscover(t.Context(), []string{"add", "https://example.com/model", "--role", "coding", "--model", "hf.co/owner/model:Q4_K_M", "--display", "json"})
	})
	var inbox discoveryInboxOutput
	if err := json.Unmarshal([]byte(output), &inbox); err != nil || code != exitOK || len(inbox.Ideas) != 1 {
		t.Fatalf("capture failed: code=%d err=%v output=%s", code, err, output)
	}
	return inbox.Ideas[0]
}

func discoveryCLIReceipt(t *testing.T, hash string) (string, source.Resolution) {
	t.Helper()
	metadata := fmt.Sprintf(`{"id":"owner/model","sha":%q,"siblings":[{"rfilename":"model.gguf","size":1024,"blobId":%q,"lfs":{"size":1024,"sha256":%q,"pointerSize":128}}]}`,
		strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat(hash, 64))
	return discoveryCLIResolvedReceipt(t, metadata, "model.gguf")
}

func discoveryCLIResolvedReceipt(t *testing.T, metadata string, files ...string) (string, source.Resolution) {
	t.Helper()
	resolver := source.NewResolver(sourceTestTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(metadata))}, nil
	}))
	receipt, err := resolver.ResolveHF(t.Context(), source.HFRequest{RepoID: "owner/model", Revision: "main", Files: files})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(sourceTestDirectory(t), "receipt.json")
	if err := source.WriteResolution(path, receipt); err != nil {
		t.Fatal(err)
	}
	return path, receipt
}

func attachDiscoveryCLIReceipt(t *testing.T, ideaID, path string) {
	t.Helper()
	if code := cmdDiscover(t.Context(), []string{"attach-source", ideaID, path, "--display", "none"}); code != exitOK {
		t.Fatalf("attach exited %d", code)
	}
}

func discoveryCLIPlan(t *testing.T, args ...string) discovery.SourceProposal {
	t.Helper()
	command := append([]string{"plan"}, args...)
	command = append(command, "--display", "json")
	output, code := captureTopStdout(t, func() int { return cmdDiscover(t.Context(), command) })
	var inbox discoveryInboxOutput
	if err := json.Unmarshal([]byte(output), &inbox); err != nil || code != exitOK || len(inbox.Proposals) != 1 {
		t.Fatalf("plan failed: code=%d err=%v output=%s", code, err, output)
	}
	if inbox.Schema != "fitr.discovery.inbox.v1" || inbox.Ideas[0].Schema != discovery.Schema || inbox.Proposals[0].Schema != discovery.SourcePlanSchema {
		t.Fatalf("schema compatibility changed: %+v", inbox)
	}
	if strings.Contains(output, `"argv"`) || strings.Contains(output, "fitr advise") {
		t.Fatal("plan emits a mutable alias command")
	}
	return inbox.Proposals[0]
}

func TestDiscoverySourceAttachmentSurvivesMovedReceiptAndDetach(t *testing.T) {
	idea := discoveryCLIIdea(t)
	path, receipt := discoveryCLIReceipt(t, "c")
	ideaPath := filepath.Join(resultsDir(), ".discovery", idea.ID+".json")
	before, err := os.ReadFile(ideaPath)
	if err != nil {
		t.Fatal(err)
	}
	attachDiscoveryCLIReceipt(t, idea.ID, path)
	moved := path + ".moved"
	if err := os.Rename(path, moved); err != nil {
		t.Fatal(err)
	}
	plan := discoveryCLIPlan(t, idea.ID)
	if plan.State != "source_selected" || plan.SelectedResolutionSHA256 != receipt.ResolutionSHA256 || plan.Selected == nil || len(plan.Sources) != 1 {
		t.Fatalf("managed source was lost: %+v", plan)
	}
	if rolePlan := discoveryCLIPlan(t, "--role", "coding"); rolePlan.SelectedResolutionSHA256 != receipt.ResolutionSHA256 {
		t.Fatal("role-filtered plan lost source")
	}
	assertDiscoverySourceListed(t, idea.ID, receipt.ResolutionSHA256)
	if code := cmdDiscover(t.Context(), []string{"detach-source", idea.ID, receipt.ResolutionSHA256, "--display", "none"}); code != exitOK {
		t.Fatalf("detach exited %d", code)
	}
	if plan := discoveryCLIPlan(t, idea.ID); plan.State != "source_missing" || plan.Selected != nil {
		t.Fatal("detached source remained selected")
	}
	after, err := os.ReadFile(ideaPath)
	if err != nil || string(after) != string(before) {
		t.Fatalf("source operations altered idea: %v", err)
	}
	if _, err := source.LoadResolution(moved); err != nil {
		t.Fatalf("detach changed external receipt: %v", err)
	}
}

func TestDiscoveryMultipleSourcesRequireExplicitSelection(t *testing.T) {
	idea := discoveryCLIIdea(t)
	firstPath, first := discoveryCLIReceipt(t, "c")
	secondPath, second := discoveryCLIReceipt(t, "d")
	attachDiscoveryCLIReceipt(t, idea.ID, firstPath)
	attachDiscoveryCLIReceipt(t, idea.ID, secondPath)
	if plan := discoveryCLIPlan(t, idea.ID); plan.State != "selection_required" || plan.Selected != nil {
		t.Fatal("multiple sources silently selected")
	}
	plan := discoveryCLIPlan(t, idea.ID, "--source", second.ResolutionSHA256)
	if plan.SelectedResolutionSHA256 != second.ResolutionSHA256 || plan.Selected.ResolutionSHA256 == first.ResolutionSHA256 {
		t.Fatal("explicit selection ignored")
	}
	output, code := captureTopStdout(t, func() int {
		return cmdDiscover(t.Context(), []string{"plan", idea.ID, "--source", second.ResolutionSHA256, "--display", "plain"})
	})
	flattened := strings.Join(strings.Fields(output), " ")
	if code != exitOK || !strings.Contains(flattened, "selected") || !strings.Contains(output, "model.gguf") || !strings.Contains(flattened, "local bytes unverified") {
		t.Fatalf("selected file detail missing: %s", output)
	}
	cards := discoveryCards([]discovery.Idea{idea}, []discovery.SourceProposal{plan}, map[string][]discovery.SourceSummary{idea.ID: plan.Sources})
	if cards[0].ID != idea.ID || len(cards[0].Sources) != 2 || cards[0].Sources[1].Digest == "" {
		t.Fatal("full actionable source IDs lost")
	}
	found := false
	for _, facet := range cards[0].Facets {
		found = found || facet.Label == "selected" && facet.Text == second.ResolutionSHA256
	}
	if !found {
		t.Fatal("full selected digest not exposed")
	}
	_, code = captureTopStderr(t, func() int {
		return cmdDiscover(t.Context(), []string{"plan", idea.ID, "--source", "sha256:" + strings.Repeat("e", 64), "--display", "none"})
	})
	if code != exitError {
		t.Fatal("unattached source digest accepted with display none")
	}
}

func assertDiscoverySourceListed(t *testing.T, ideaID, digest string) {
	t.Helper()
	output, code := captureTopStdout(t, func() int {
		return cmdDiscover(t.Context(), []string{"list", "--role", "coding", "--display", "json"})
	})
	var inbox discoveryInboxOutput
	if err := json.Unmarshal([]byte(output), &inbox); err != nil || code != exitOK {
		t.Fatalf("list failed: %d %v", code, err)
	}
	summaries := inbox.Sources[ideaID]
	if len(summaries) != 1 || summaries[0].ResolutionSHA256 != digest || summaries[0].Relation != "operator_association" || len(summaries[0].Files) != 1 {
		t.Fatalf("list lost additive metadata summaries: %+v", summaries)
	}
}

func TestDiscoverySourceValidationStillRunsWithDisplayNone(t *testing.T) {
	idea := discoveryCLIIdea(t)
	path, receipt := discoveryCLIReceipt(t, "c")
	attachDiscoveryCLIReceipt(t, idea.ID, path)
	managed := filepath.Join(resultsDir(), ".discovery-sources", idea.ID, strings.TrimPrefix(receipt.ResolutionSHA256, "sha256:")+".json")
	if err := os.WriteFile(managed, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"list"}, {"plan"}, {"plan", idea.ID}, {"attach-source", idea.ID, path}, {"detach-source", idea.ID, receipt.ResolutionSHA256}} {
		args = append(args, "--display", "none")
		_, code := captureTopStderr(t, func() int { return cmdDiscover(t.Context(), args) })
		if code != exitError {
			t.Fatalf("%v hid invalid source: %d", args, code)
		}
	}
}

func TestDiscoverySourcePlanRendersExactDependencyAndGapFindings(t *testing.T) {
	idea := discoveryCLIIdea(t)
	first, second, missing := "q4/model-00001-of-00003.gguf", "q4/model-00002-of-00003.gguf", "q4/model-00003-of-00003.gguf"
	metadata := fmt.Sprintf(`{"id":"owner/model","sha":%q,"siblings":[{"rfilename":%q,"size":1024},{"rfilename":%q,"size":1024},{"rfilename":"mmproj-f16.gguf","size":1024}]}`,
		strings.Repeat("a", 40), first, second)
	path, receipt := discoveryCLIResolvedReceipt(t, metadata, first)
	if receipt.State != "incomplete" {
		t.Fatalf("fixture did not contain a metadata gap: %+v", receipt)
	}
	attachDiscoveryCLIReceipt(t, idea.ID, path)
	for _, mode := range []string{"plain", "rich"} {
		output, code := captureTopStdout(t, func() int {
			return cmdDiscover(t.Context(), []string{"plan", idea.ID, "--display", mode})
		})
		if code != exitOK {
			t.Fatalf("plan exited %d", code)
		}
		for _, want := range []string{first, second, missing, "shard: unselected", "shard: missing", "projector: candidate", "mmproj-f16.gguf", "content_sha256_unavailable"} {
			if !strings.Contains(output, want) {
				t.Fatalf("%s lost exact finding %q: %s", mode, want, output)
			}
		}
	}
}

func TestDiscoverySourceArgumentsAndOutputFailures(t *testing.T) {
	idea := discoveryCLIIdea(t)
	digest := "sha256:" + strings.Repeat("a", 64)
	for _, args := range [][]string{
		{"attach-source", idea.ID}, {"attach-source", "../idea", "receipt.json"},
		{"detach-source", idea.ID, "short"}, {"plan", idea.ID, "extra"},
		{"plan", "--source", digest}, {"plan", idea.ID, "--source", "bad"},
		{"plan", idea.ID, "--role", "coding"}, {"list", "--source", digest},
	} {
		_, code := captureTopStderr(t, func() int { return cmdDiscover(t.Context(), args) })
		if code != exitUsage {
			t.Fatalf("%v exited %d", args, code)
		}
	}
	closed, err := os.CreateTemp(t.TempDir(), "closed-output")
	if err != nil {
		t.Fatal(err)
	}
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	previous := os.Stdout
	os.Stdout = closed
	defer func() { os.Stdout = previous }()
	for _, mode := range []string{"json", "plain", "rich", "auto"} {
		if code := cmdDiscover(t.Context(), []string{"plan", idea.ID, "--display", mode}); code != exitError {
			t.Fatalf("%s ignored output failure: %d", mode, code)
		}
	}
}
