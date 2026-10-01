package discovery

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/source"
)

func TestBuildDownloadPlanNilAndUnresolved(t *testing.T) {
	if got := BuildDownloadPlan(nil); got != nil {
		t.Fatalf("BuildDownloadPlan(nil) = %+v, want nil", got)
	}

	receipt := source.Resolution{
		State: "unavailable",
	}
	plan := BuildDownloadPlan(&receipt)
	if plan == nil || plan.Status != "unresolved" || plan.Schema != DownloadPlanSchema {
		t.Fatalf("unavailable receipt plan = %+v", plan)
	}

	size := int64(1024)
	incompleteReceipt := source.Resolution{
		State: "resolved",
		Files: []source.FileMetadata{
			{Path: "model.gguf", SizeBytes: &size, DeclaredSHA256: ""},
		},
	}
	plan = BuildDownloadPlan(&incompleteReceipt)
	if plan == nil || plan.Status != "incomplete" {
		t.Fatalf("missing SHA plan = %+v", plan)
	}
}

func TestBuildDownloadPlanFromResolvedFiles(t *testing.T) {
	size1 := int64(2048)
	size2 := int64(1024)
	sha1 := "sha256:" + strings.Repeat("1", 64)
	sha2 := "sha256:" + strings.Repeat("2", 64)

	receipt := source.Resolution{
		State: "resolved",
		Files: []source.FileMetadata{
			{Path: "model-00001-of-00002.gguf", SizeBytes: &size1, DeclaredSHA256: sha1},
			{Path: "model-00002-of-00002.gguf", SizeBytes: &size2, DeclaredSHA256: sha2},
		},
	}

	plan := BuildDownloadPlan(&receipt)
	if plan == nil || plan.Status != "ready" {
		t.Fatalf("resolved files plan = %+v", plan)
	}
	if plan.TotalRequiredBytes != size1+size2 {
		t.Fatalf("total required = %d, want %d", plan.TotalRequiredBytes, size1+size2)
	}
	if len(plan.Files) != 2 || plan.Files[0].ComponentRole != "shard" {
		t.Fatalf("download files = %+v", plan.Files)
	}
	if plan.Verification != "sha256" {
		t.Fatalf("verification = %q, want sha256", plan.Verification)
	}

	// ArtifactSpec template generation
	resSHA := "sha256:" + strings.Repeat("a", 64)
	localDir := filepath.Join(t.TempDir(), "models")
	spec, err := plan.ArtifactSpec(resSHA, localDir)
	if err != nil {
		t.Fatalf("ArtifactSpec failed: %v", err)
	}
	if err := spec.Validate(); err != nil {
		t.Fatalf("generated artifact spec is invalid: %v", err)
	}
	if len(spec.Files) != 2 || spec.Files[0].ComponentRole != "shard" {
		t.Fatalf("spec files = %+v", spec.Files)
	}
	if spec.Files[0].LocalPath != filepath.Join(localDir, "model-00001-of-00002.gguf") {
		t.Fatalf("spec local path = %q", spec.Files[0].LocalPath)
	}
}

func TestBuildDownloadPlanFromComponentPlan(t *testing.T) {
	sizeModel := int64(4096)
	sizeProj := int64(512)
	sizeDraft := int64(1024)
	shaModel := "sha256:" + strings.Repeat("a", 64)
	shaProj := "sha256:" + strings.Repeat("b", 64)
	shaDraft := "sha256:" + strings.Repeat("c", 64)

	cp := &source.ComponentPlan{
		Schema: source.ComponentPlanSchema,
		Status: source.PlanComplete,
		Reason: "all required components selected",
		Components: []source.PlannedComponent{
			{Path: "model.gguf", Kind: source.ComponentKindCompanion, State: source.ComponentRequired, SizeBytes: &sizeModel},
			{Path: "mmproj-f16.gguf", Kind: source.ComponentKindProjector, State: source.ComponentRequired, SizeBytes: &sizeProj},
			{Path: "draft.gguf", Kind: source.ComponentKindCompanion, State: source.ComponentOptional, SizeBytes: &sizeDraft},
			{Path: "ignore.txt", Kind: "other", State: source.ComponentDisabled},
		},
		TotalRequiredBytes: sizeModel + sizeProj,
		TotalOptionalBytes: sizeDraft,
	}

	receipt := source.Resolution{
		State:         "resolved",
		ComponentPlan: cp,
		Files: []source.FileMetadata{
			{Path: "model.gguf", SizeBytes: &sizeModel, DeclaredSHA256: shaModel},
			{Path: "mmproj-f16.gguf", SizeBytes: &sizeProj, DeclaredSHA256: shaProj},
			{Path: "draft.gguf", SizeBytes: &sizeDraft, DeclaredSHA256: shaDraft},
		},
	}

	plan := BuildDownloadPlan(&receipt)
	if plan == nil || plan.Status != "ready" {
		t.Fatalf("component plan download = %+v", plan)
	}
	if plan.TotalRequiredBytes != sizeModel+sizeProj {
		t.Fatalf("required bytes = %d, want %d", plan.TotalRequiredBytes, sizeModel+sizeProj)
	}
	if plan.TotalOptionalBytes != sizeDraft {
		t.Fatalf("optional bytes = %d, want %d", plan.TotalOptionalBytes, sizeDraft)
	}
	if len(plan.Files) != 3 {
		t.Fatalf("download files count = %d, want 3", len(plan.Files))
	}
}

func TestBuildDownloadPlanBlockedAndUnresolvedComponentPlan(t *testing.T) {
	cpBlocked := &source.ComponentPlan{
		Schema: source.ComponentPlanSchema,
		Status: source.PlanBlocked,
		Reason: "architecture explicitly unsupported",
	}
	receiptBlocked := source.Resolution{
		State:         "resolved",
		ComponentPlan: cpBlocked,
	}
	planBlocked := BuildDownloadPlan(&receiptBlocked)
	if planBlocked == nil || planBlocked.Status != "blocked" {
		t.Fatalf("blocked plan = %+v", planBlocked)
	}

	cpUnresolved := &source.ComponentPlan{
		Schema: source.ComponentPlanSchema,
		Status: source.PlanUnresolved,
		Reason: "missing required vision projector",
	}
	receiptUnres := source.Resolution{
		State:         "resolved",
		ComponentPlan: cpUnresolved,
	}
	planUnres := BuildDownloadPlan(&receiptUnres)
	if planUnres == nil || planUnres.Status != "unresolved" {
		t.Fatalf("unresolved plan = %+v", planUnres)
	}
}

func TestArtifactSpecRejectsUnreadyDownloadPlan(t *testing.T) {
	var dp *DownloadPlan
	if _, err := dp.ArtifactSpec("sha256:test", "/tmp"); err == nil {
		t.Fatal("nil plan ArtifactSpec succeeded")
	}

	unready := &DownloadPlan{Status: "unresolved"}
	if _, err := unready.ArtifactSpec("sha256:test", "/tmp"); err == nil {
		t.Fatal("unready ArtifactSpec succeeded")
	}

	emptyReady := &DownloadPlan{Status: "ready", Files: nil}
	if _, err := emptyReady.ArtifactSpec("sha256:test", "/tmp"); err == nil {
		t.Fatal("empty files ArtifactSpec succeeded")
	}
}
