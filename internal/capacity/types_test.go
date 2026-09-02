package capacity

import (
	"math"
	"strings"
	"testing"
	"time"
)

const testArtifactDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestBuildPolicyKeepsEveryCapacitySemanticSeparate(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	available := MemoryObservation{
		Kind: ObservationCurrentAvailable, ResourceDomain: DomainUnified,
		Bytes: 80 << 30, Source: "/proc/meminfo MemAvailable", ObservedAt: now,
	}
	addressable := MemoryObservation{
		Kind: ObservationAddressable, ResourceDomain: DomainUnified,
		Bytes: 120 << 30, Source: "/proc/meminfo MemTotal", ObservedAt: now,
	}
	container := ContainerReceipt{
		ResourceDomain: DomainUnified, LimitBytes: 64 << 30, CurrentBytes: 8 << 30,
		HeadroomBytes: 56 << 30, Source: "cgroup v2 memory.max/current", ObservedAt: now,
	}
	reserve := int64(6 << 30)
	policy, err := BuildPolicy(PolicyInput{
		ResourceDomain: DomainUnified, Addressable: &addressable, CurrentAvailable: &available,
		Container: &container, OperatorReserveBytes: &reserve,
	})
	if err != nil {
		t.Fatal(err)
	}
	if policy.Formula != FormulaConstrainedMinusReserve || policy.UsableBudgetBytes == nil ||
		*policy.UsableBudgetBytes != 50<<30 {
		t.Fatalf("policy = %+v", policy)
	}
	if policy.Addressable.Bytes != 120<<30 || policy.CurrentAvailable.Bytes != 80<<30 ||
		policy.Container.HeadroomBytes != 56<<30 || policy.Swap != SwapExcluded {
		t.Fatalf("capacity semantics collapsed: %+v", policy)
	}
}

func TestPolicyWithoutOperatorChoicePreservesFactsWithoutInventingBudget(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	available := MemoryObservation{
		Kind: ObservationCurrentAvailable, ResourceDomain: DomainAccelerator,
		Bytes: 20 << 30, Source: "nvidia-smi memory.free", ObservedAt: now,
	}
	policy, err := BuildPolicy(PolicyInput{ResourceDomain: DomainAccelerator, CurrentAvailable: &available})
	if err != nil {
		t.Fatal(err)
	}
	if policy.UsableBudgetBytes != nil || policy.Formula != "" {
		t.Fatalf("availability became an implicit safe budget: %+v", policy)
	}
}

func TestExplicitBudgetWinsWithoutRewritingTransientObservation(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	available := MemoryObservation{
		Kind: ObservationCurrentAvailable, ResourceDomain: DomainAccelerator,
		Bytes: 3 << 30, Source: "nvidia-smi memory.free", ObservedAt: now,
	}
	budget := int64(18 << 30)
	policy, err := BuildPolicy(PolicyInput{
		ResourceDomain: DomainAccelerator, CurrentAvailable: &available, OperatorBudgetBytes: &budget,
	})
	if err != nil {
		t.Fatal(err)
	}
	if policy.UsableBudgetBytes == nil || *policy.UsableBudgetBytes != budget ||
		policy.CurrentAvailable.Bytes != 3<<30 || policy.Formula != FormulaOperatorBudget {
		t.Fatalf("explicit and transient values were conflated: %+v", policy)
	}
}

func TestPolicyRejectsMalformedArithmetic(t *testing.T) {
	base := validTestPolicy(t)
	tests := map[string]func(*Policy){
		"domain":  func(p *Policy) { p.ResourceDomain = "mystery" },
		"swap":    func(p *Policy) { p.Swap = "included" },
		"formula": func(p *Policy) { value := *p.UsableBudgetBytes - 1; p.UsableBudgetBytes = &value },
		"source":  func(p *Policy) { p.CurrentAvailable.Source = "" },
		"kind":    func(p *Policy) { p.CurrentAvailable.Kind = ObservationAddressable },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			policy := ClonePlan(&Plan{Policy: base}).Policy
			mutate(&policy)
			if err := policy.Validate(); err == nil {
				t.Fatalf("malformed policy accepted: %+v", policy)
			}
		})
	}
}

func TestPredictionIsExplicitlyComponentsOnlyAndPolicyBound(t *testing.T) {
	policy := validTestPolicy(t)
	artifact, kv := int64(10<<30), int64(2<<30)
	prediction, err := BuildPrediction(policy, PredictionInput{
		CreatedAt: time.Date(2026, 9, 1, 12, 0, 1, 0, time.UTC), ArtifactSHA256: testArtifactDigest,
		ResourceDomain: DomainAccelerator, RequestedContext: 32768, Architecture: "qwen3",
		KVDataType: "q8_0", KVElementBytes: 1, PlacementAssumption: "runtime default; unverified",
		ArtifactBytes: &artifact, KVBytes: &kv,
		Excluded: []string{"runtime buffers", "in-flight peaks"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if prediction.State != PredictionComponentProjection || prediction.KnownComponentBytes == nil ||
		*prediction.KnownComponentBytes != 12<<30 {
		t.Fatalf("prediction = %+v", prediction)
	}
	if strings.Contains(string(prediction.State), "fit") || strings.Contains(string(prediction.State), "lower") {
		t.Fatalf("component projection acquired a fit claim: %+v", prediction)
	}
	plan := Plan{Schema: PlanSchema, Policy: policy, Prediction: prediction}
	if err := plan.Validate(); err != nil {
		t.Fatal(err)
	}
	other := policy
	otherBudget := int64(17 << 30)
	other.OperatorBudgetBytes, other.UsableBudgetBytes = &otherBudget, &otherBudget
	plan.Policy = other
	if err := plan.Validate(); err == nil || !strings.Contains(err.Error(), "different policy") {
		t.Fatalf("prediction was detached from its policy: %v", err)
	}
}

func TestPredictionRejectsComponentOverflow(t *testing.T) {
	policy := validTestPolicy(t)
	artifact, kv := int64(math.MaxInt64), int64(1)
	_, err := BuildPrediction(policy, PredictionInput{
		CreatedAt: time.Now().UTC(), ArtifactSHA256: testArtifactDigest,
		ResourceDomain: DomainAccelerator, RequestedContext: 8192,
		PlacementAssumption: "runtime default", ArtifactBytes: &artifact, KVBytes: &kv,
		Excluded: []string{"runtime allocation"},
	})
	if err == nil || !strings.Contains(err.Error(), "overflows") {
		t.Fatalf("BuildPrediction error = %v, want overflow rejection", err)
	}
}

func TestPolicyRejectsImpossibleCapacityRelationships(t *testing.T) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	addressable := MemoryObservation{
		Kind: ObservationAddressable, ResourceDomain: DomainUnified,
		Bytes: 8 << 30, Source: "test total", ObservedAt: now,
	}
	tests := []struct {
		name      string
		available int64
		budget    int64
	}{
		{name: "availability above addressable", available: 9 << 30},
		{name: "budget above addressable", budget: 9 << 30},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			input := PolicyInput{ResourceDomain: DomainUnified, Addressable: &addressable}
			if tc.available > 0 {
				input.CurrentAvailable = &MemoryObservation{
					Kind: ObservationCurrentAvailable, ResourceDomain: DomainUnified,
					Bytes: tc.available, Source: "test available", ObservedAt: now,
				}
			}
			if tc.budget > 0 {
				input.OperatorBudgetBytes = &tc.budget
			}
			if _, err := BuildPolicy(input); err == nil {
				t.Fatal("BuildPolicy accepted an impossible capacity relationship")
			}
		})
	}
}

func validTestPolicy(t *testing.T) Policy {
	t.Helper()
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	available := MemoryObservation{
		Kind: ObservationCurrentAvailable, ResourceDomain: DomainAccelerator,
		Bytes: 20 << 30, Source: "nvidia-smi memory.free", ObservedAt: now,
	}
	budget := int64(18 << 30)
	policy, err := BuildPolicy(PolicyInput{
		ResourceDomain: DomainAccelerator, CurrentAvailable: &available, OperatorBudgetBytes: &budget,
	})
	if err != nil {
		t.Fatal(err)
	}
	return policy
}
