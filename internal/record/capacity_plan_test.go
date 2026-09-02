package record

import (
	"strings"
	"testing"
	"time"

	"github.com/blisspixel/fitr/internal/capacity"
)

func TestRunManifestSealsCapacityPlanBeforeEvidence(t *testing.T) {
	r := manifestRecord("model", "2026-09-01T12:00:00Z")
	r.SchemaVersion = EvidenceSchemaVersion
	addTestFingerprintV2(t, r)
	r.CapacityPlan = testCapacityPlan(t)
	if err := r.AttachManifest(digestIdentity(t, "model", "model"), testRunProvenance(t)); err != nil {
		t.Fatal(err)
	}
	if r.Manifest.CapacityPlan == r.CapacityPlan || r.Manifest.CapacityPlan.Policy.UsableBudgetBytes == r.CapacityPlan.Policy.UsableBudgetBytes {
		t.Fatal("manifest retained mutable capacity plan pointers")
	}
	if err := r.ValidateManifest(); err != nil {
		t.Fatal(err)
	}
	(*r.CapacityPlan.Policy.UsableBudgetBytes)--
	if err := r.ValidateManifest(); err == nil || !strings.Contains(err.Error(), "capacity plan differs") {
		t.Fatalf("mutated capacity plan validation = %v", err)
	}
}

func TestRunManifestRejectsCapacityPlanWithoutMemoryProbe(t *testing.T) {
	r := manifestRecord("model", "2026-09-01T12:00:00Z")
	r.SchemaVersion = EvidenceSchemaVersion
	r.TaskPlan.Memory = false
	r.CapacityPlan = testCapacityPlan(t)
	addTestFingerprintV2(t, r)
	if err := r.AttachManifest(digestIdentity(t, "model", "model"), testRunProvenance(t)); err == nil ||
		!strings.Contains(err.Error(), "without a memory probe") {
		t.Fatalf("unplanned capacity plan validation = %v", err)
	}
}

func testCapacityPlan(t *testing.T) *capacity.Plan {
	t.Helper()
	budget := int64(18 << 30)
	policy, err := capacity.BuildPolicy(capacity.PolicyInput{
		ResourceDomain: capacity.DomainAccelerator, OperatorBudgetBytes: &budget,
	})
	if err != nil {
		t.Fatal(err)
	}
	artifact, kv := int64(10<<30), int64(2<<30)
	prediction, err := capacity.BuildPrediction(policy, capacity.PredictionInput{
		CreatedAt:      time.Date(2026, 9, 1, 12, 0, 1, 0, time.UTC),
		ArtifactSHA256: testArtifactDigest, ResourceDomain: capacity.DomainAccelerator,
		RequestedContext: 32768, Architecture: "qwen3", KVDataType: "q8_0", KVElementBytes: 1,
		PlacementAssumption: "runtime default; unverified", ArtifactBytes: &artifact, KVBytes: &kv,
		Excluded: []string{"runtime buffers", "in-flight peaks"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &capacity.Plan{Schema: capacity.PlanSchema, Policy: policy, Prediction: prediction}
}
