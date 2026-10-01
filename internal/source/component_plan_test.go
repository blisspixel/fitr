package source

import (
	"strings"
	"testing"
)

func TestComponentPlanValidation(t *testing.T) {
	bytesVal := int64(1000)
	valid := &ComponentPlan{
		Schema:       ComponentPlanSchema,
		SourceSHA256: "sha256:" + strings.Repeat("a", 64),
		Status:       PlanComplete,
		Components: []PlannedComponent{
			{Path: "model.gguf", Kind: ComponentKindModelShard, State: ComponentRequired, SizeBytes: &bytesVal},
		},
		TotalRequiredBytes: 1000,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid plan failed validation: %v", err)
	}

	// Bad schema
	badSchema := *valid
	badSchema.Schema = "bad.schema"
	if err := badSchema.Validate(); err == nil {
		t.Fatal("expected error for bad schema")
	}

	// Bad source sha
	badSource := *valid
	badSource.SourceSHA256 = "not-a-sha"
	if err := badSource.Validate(); err == nil {
		t.Fatal("expected error for bad source_sha256")
	}

	// Bad status
	badStatus := *valid
	badStatus.Status = "unknown"
	if err := badStatus.Validate(); err == nil {
		t.Fatal("expected error for bad status")
	}

	// Negative bytes
	badBytes := *valid
	badBytes.TotalRequiredBytes = -1
	if err := badBytes.Validate(); err == nil {
		t.Fatal("expected error for negative bytes")
	}

	// Duplicate path
	dupPath := *valid
	dupPath.Components = []PlannedComponent{
		{Path: "model.gguf", Kind: ComponentKindModelShard, State: ComponentRequired},
		{Path: "model.gguf", Kind: ComponentKindModelShard, State: ComponentRequired},
	}
	if err := dupPath.Validate(); err == nil {
		t.Fatal("expected error for duplicate component path")
	}
}

func TestComponentPlanDigestAndSeal(t *testing.T) {
	bytesVal := int64(5000)
	plan := &ComponentPlan{
		Schema:       ComponentPlanSchema,
		SourceSHA256: "sha256:" + strings.Repeat("b", 64),
		Status:       PlanComplete,
		Components: []PlannedComponent{
			{Path: "shard-02.gguf", Kind: ComponentKindModelShard, State: ComponentRequired, SizeBytes: &bytesVal},
			{Path: "shard-01.gguf", Kind: ComponentKindModelShard, State: ComponentRequired, SizeBytes: &bytesVal},
		},
		TotalRequiredBytes: 10000,
	}
	digest1, err := plan.Digest()
	if err != nil {
		t.Fatalf("unexpected digest error: %v", err)
	}
	if err := plan.Seal(); err != nil {
		t.Fatalf("seal failed: %v", err)
	}
	if plan.PlanSHA256 != digest1 {
		t.Fatalf("seal digest mismatch: %s vs %s", plan.PlanSHA256, digest1)
	}
}

func mockResolution(files []FileMetadata, deps []DependencyFinding, inventory []string) Resolution {
	return Resolution{
		Schema:           ResolutionSchema,
		ResolutionSHA256: "sha256:" + strings.Repeat("c", 64),
		State:            "resolved",
		Files:            files,
		Dependencies:     deps,
		InventoryPaths:   inventory,
	}
}

func TestBuildComponentPlanCompleteSingleModel(t *testing.T) {
	size := int64(4 * 1024 * 1024 * 1024)
	res := mockResolution([]FileMetadata{
		{Path: "model.gguf", State: "present", SizeBytes: &size, DeclaredSHA256: "sha256:" + strings.Repeat("d", 64)},
	}, nil, []string{"model.gguf"})

	plan, err := BuildComponentPlan(res, BuildComponentPlanOptions{})
	if err != nil {
		t.Fatalf("build component plan error: %v", err)
	}
	if plan.Status != PlanComplete {
		t.Fatalf("expected plan complete, got %s: %s", plan.Status, plan.Reason)
	}
	if plan.TotalRequiredBytes != size {
		t.Fatalf("expected required bytes %d, got %d", size, plan.TotalRequiredBytes)
	}
	if len(plan.Components) != 1 || plan.Components[0].State != ComponentRequired {
		t.Fatalf("unexpected components: %+v", plan.Components)
	}
}

func TestBuildComponentPlanShards(t *testing.T) {
	size1, size2 := int64(2000), int64(3000)
	resComplete := mockResolution([]FileMetadata{
		{Path: "model-00001-of-00002.gguf", State: "present", SizeBytes: &size1, DeclaredSHA256: "sha256:" + strings.Repeat("1", 64)},
		{Path: "model-00002-of-00002.gguf", State: "present", SizeBytes: &size2, DeclaredSHA256: "sha256:" + strings.Repeat("2", 64)},
	}, nil, []string{"model-00001-of-00002.gguf", "model-00002-of-00002.gguf"})

	plan, err := BuildComponentPlan(resComplete, BuildComponentPlanOptions{})
	if err != nil {
		t.Fatalf("build component plan error: %v", err)
	}
	if plan.Status != PlanComplete || plan.TotalRequiredBytes != 5000 {
		t.Fatalf("expected complete with 5000 bytes, got %s, %d bytes", plan.Status, plan.TotalRequiredBytes)
	}

	// Incomplete shards with unselected shard dependency
	resIncomplete := mockResolution([]FileMetadata{
		{Path: "model-00001-of-00002.gguf", State: "present", SizeBytes: &size1, DeclaredSHA256: "sha256:" + strings.Repeat("1", 64)},
	}, []DependencyFinding{
		{Kind: "shard", SourceFile: "model-00001-of-00002.gguf", TargetFile: "model-00002-of-00002.gguf", Status: "unselected"},
	}, []string{"model-00001-of-00002.gguf", "model-00002-of-00002.gguf"})

	planIncomplete, err := BuildComponentPlan(resIncomplete, BuildComponentPlanOptions{})
	if err != nil {
		t.Fatalf("build error: %v", err)
	}
	if planIncomplete.Status != PlanUnresolved {
		t.Fatalf("expected unresolved for missing shard, got %s", planIncomplete.Status)
	}
	foundUnresolvedShard := false
	for _, c := range planIncomplete.Components {
		if c.Path == "model-00002-of-00002.gguf" && c.State == ComponentUnresolved {
			foundUnresolvedShard = true
		}
	}
	if !foundUnresolvedShard {
		t.Fatalf("expected unresolved component for shard 2: %+v", planIncomplete.Components)
	}
}

func TestBuildComponentPlanRequiredCompanions(t *testing.T) {
	modelSize := int64(10000)
	projSize := int64(500)
	sha := "sha256:" + strings.Repeat("f", 64)

	// Case 1: Vision model with projector selected -> Complete
	resWithProj := mockResolution([]FileMetadata{
		{Path: "llava-model.gguf", State: "present", SizeBytes: &modelSize, DeclaredSHA256: sha},
		{Path: "mmproj-model-f16.gguf", State: "present", SizeBytes: &projSize, DeclaredSHA256: sha},
	}, nil, []string{"llava-model.gguf", "mmproj-model-f16.gguf"})

	plan, err := BuildComponentPlan(resWithProj, BuildComponentPlanOptions{
		Architecture:       "llava",
		RequiredCompanions: []string{"projector"},
	})
	if err != nil {
		t.Fatalf("build error: %v", err)
	}
	if plan.Status != PlanComplete {
		t.Fatalf("expected complete plan, got %s: %s", plan.Status, plan.Reason)
	}
	if plan.TotalRequiredBytes != modelSize+projSize {
		t.Fatalf("expected total %d, got %d", modelSize+projSize, plan.TotalRequiredBytes)
	}

	// Case 2: Vision model with projector in inventory but NOT selected -> Unresolved
	resUnselectedProj := mockResolution([]FileMetadata{
		{Path: "llava-model.gguf", State: "present", SizeBytes: &modelSize, DeclaredSHA256: sha},
	}, nil, []string{"llava-model.gguf", "mmproj-model-f16.gguf"})

	planUnsel, err := BuildComponentPlan(resUnselectedProj, BuildComponentPlanOptions{
		Architecture:       "llava",
		RequiredCompanions: []string{"projector"},
	})
	if err != nil {
		t.Fatalf("build error: %v", err)
	}
	if planUnsel.Status != PlanUnresolved {
		t.Fatalf("expected unresolved plan, got %s", planUnsel.Status)
	}
	if len(planUnsel.Gaps) == 0 || !strings.Contains(planUnsel.Gaps[0], "required_companion_unselected") {
		t.Fatalf("expected unselected companion gap, got: %v", planUnsel.Gaps)
	}

	// Case 3: Required companion disabled -> Blocked
	planBlocked, err := BuildComponentPlan(resWithProj, BuildComponentPlanOptions{
		Architecture:       "llava",
		RequiredCompanions: []string{"projector"},
		DisabledPaths:      []string{"mmproj-model-f16.gguf"},
	})
	if err != nil {
		t.Fatalf("build error: %v", err)
	}
	if planBlocked.Status != PlanBlocked {
		t.Fatalf("expected blocked plan when required companion disabled, got %s", planBlocked.Status)
	}
}

func TestBuildComponentPlanOptionalFiles(t *testing.T) {
	modelSize := int64(10000)
	draftSize := int64(1500)
	sha := "sha256:" + strings.Repeat("e", 64)

	res := mockResolution([]FileMetadata{
		{Path: "model.gguf", State: "present", SizeBytes: &modelSize, DeclaredSHA256: sha},
		{Path: "draft-model.gguf", State: "present", SizeBytes: &draftSize, DeclaredSHA256: sha},
	}, nil, []string{"model.gguf", "draft-model.gguf"})

	plan, err := BuildComponentPlan(res, BuildComponentPlanOptions{
		OptionalPaths: []string{"draft-model.gguf"},
	})
	if err != nil {
		t.Fatalf("build error: %v", err)
	}
	if plan.Status != PlanComplete {
		t.Fatalf("expected complete plan, got %s", plan.Status)
	}
	if plan.TotalRequiredBytes != modelSize {
		t.Fatalf("expected required bytes %d, got %d", modelSize, plan.TotalRequiredBytes)
	}
	if plan.TotalOptionalBytes != draftSize {
		t.Fatalf("expected optional bytes %d, got %d", draftSize, plan.TotalOptionalBytes)
	}
}
