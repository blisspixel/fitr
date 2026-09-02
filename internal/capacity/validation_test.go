package capacity

import (
	"math"
	"strings"
	"testing"
	"time"
)

func TestBuildPolicyReserveFormulasAndRejections(t *testing.T) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	available := MemoryObservation{
		Kind: ObservationCurrentAvailable, ResourceDomain: DomainHost,
		Bytes: 12 << 30, Source: "test available", ObservedAt: now,
	}
	reserve := int64(2 << 30)
	policy, err := BuildPolicy(PolicyInput{
		ResourceDomain: DomainHost, CurrentAvailable: &available, OperatorReserveBytes: &reserve,
	})
	if err != nil {
		t.Fatal(err)
	}
	if policy.Formula != FormulaAvailableMinusReserve || policy.UsableBudgetBytes == nil ||
		*policy.UsableBudgetBytes != 10<<30 {
		t.Fatalf("reserve policy = %+v", policy)
	}

	budget := int64(8 << 30)
	tests := []struct {
		name  string
		input PolicyInput
		want  string
	}{
		{name: "both choices", input: PolicyInput{
			ResourceDomain: DomainHost, CurrentAvailable: &available,
			OperatorBudgetBytes: &budget, OperatorReserveBytes: &reserve,
		}, want: "mutually exclusive"},
		{name: "reserve without availability", input: PolicyInput{
			ResourceDomain: DomainHost, OperatorReserveBytes: &reserve,
		}, want: "requires a current-availability"},
		{name: "reserve consumes base", input: PolicyInput{
			ResourceDomain: DomainHost, CurrentAvailable: &available,
			OperatorReserveBytes: &available.Bytes,
		}, want: "leaves no usable"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := BuildPolicy(tc.input)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("BuildPolicy error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestMemoryObservationValidationRejectsMalformedFields(t *testing.T) {
	valid := MemoryObservation{
		Kind: ObservationAddressable, ResourceDomain: DomainAccelerator,
		Bytes: 1, Source: "source", ObservedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	tests := map[string]func(*MemoryObservation){
		"kind":       func(o *MemoryObservation) { o.Kind = "mystery" },
		"domain":     func(o *MemoryObservation) { o.ResourceDomain = "mystery" },
		"bytes":      func(o *MemoryObservation) { o.Bytes = 0 },
		"source":     func(o *MemoryObservation) { o.Source = "" },
		"whitespace": func(o *MemoryObservation) { o.Source = " source " },
		"time":       func(o *MemoryObservation) { o.ObservedAt = "later" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			observation := valid
			mutate(&observation)
			if err := observation.Validate(); err == nil {
				t.Fatalf("malformed observation accepted: %+v", observation)
			}
		})
	}
}

func TestContainerReceiptValidationRejectsMalformedFields(t *testing.T) {
	valid := ContainerReceipt{
		ResourceDomain: DomainHost, LimitBytes: 10, CurrentBytes: 2, HeadroomBytes: 8,
		Source: "cgroup", ObservedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	tests := map[string]func(*ContainerReceipt){
		"domain":     func(c *ContainerReceipt) { c.ResourceDomain = "mystery" },
		"limit":      func(c *ContainerReceipt) { c.LimitBytes = 0 },
		"current":    func(c *ContainerReceipt) { c.CurrentBytes = -1 },
		"full":       func(c *ContainerReceipt) { c.CurrentBytes = c.LimitBytes },
		"headroom":   func(c *ContainerReceipt) { c.HeadroomBytes++ },
		"source":     func(c *ContainerReceipt) { c.Source = "" },
		"whitespace": func(c *ContainerReceipt) { c.Source = " cgroup" },
		"time":       func(c *ContainerReceipt) { c.ObservedAt = "later" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			receipt := valid
			mutate(&receipt)
			if err := receipt.Validate(); err == nil {
				t.Fatalf("malformed container receipt accepted: %+v", receipt)
			}
		})
	}
}

func TestPredictionValidationRejectsMalformedFields(t *testing.T) {
	prediction := validTestPrediction(t)
	tests := map[string]func(*Prediction){
		"schema":      func(p *Prediction) { p.Schema = "future" },
		"time":        func(p *Prediction) { p.CreatedAt = "later" },
		"artifact":    func(p *Prediction) { p.ArtifactSHA256 = "sha256:no" },
		"domain":      func(p *Prediction) { p.ResourceDomain = "mystery" },
		"context":     func(p *Prediction) { p.RequestedContext = 0 },
		"placement":   func(p *Prediction) { p.PlacementAssumption = "" },
		"policy":      func(p *Prediction) { p.PolicySHA256 = "sha256:no" },
		"kv negative": func(p *Prediction) { p.KVElementBytes = -1 },
		"kv nan":      func(p *Prediction) { p.KVElementBytes = math.NaN() },
		"kv infinite": func(p *Prediction) { p.KVElementBytes = math.Inf(1) },
		"bytes":       func(p *Prediction) { value := int64(0); p.ArtifactBytes = &value },
		"sum":         func(p *Prediction) { value := *p.KnownComponentBytes + 1; p.KnownComponentBytes = &value },
		"excluded":    func(p *Prediction) { p.Excluded = nil },
		"state":       func(p *Prediction) { p.State = "mystery" },
		"no sum":      func(p *Prediction) { p.KnownComponentBytes = nil },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			candidate := prediction
			mutate(&candidate)
			if err := candidate.Validate(); err == nil {
				t.Fatalf("malformed prediction accepted: %+v", candidate)
			}
		})
	}

	unavailable := prediction
	unavailable.State = PredictionUnavailable
	if err := unavailable.Validate(); err == nil {
		t.Fatal("unavailable prediction retained known components")
	}
}

func TestPlanValidationRejectsMalformedBindings(t *testing.T) {
	policy := validTestPolicy(t)
	prediction := validTestPrediction(t)
	valid := Plan{Schema: PlanSchema, Policy: policy, Prediction: prediction}
	tests := map[string]func(*Plan){
		"schema":     func(p *Plan) { p.Schema = "future" },
		"policy":     func(p *Plan) { p.Policy.Schema = "future" },
		"prediction": func(p *Plan) { p.Prediction.Schema = "future" },
		"domain":     func(p *Plan) { p.Prediction.ResourceDomain = DomainHost },
		"binding":    func(p *Plan) { p.Prediction.PolicySHA256 = testArtifactDigest },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			candidate := *ClonePlan(&valid)
			mutate(&candidate)
			if err := candidate.Validate(); err == nil {
				t.Fatalf("malformed plan accepted: %+v", candidate)
			}
		})
	}
}

func validTestPrediction(t *testing.T) Prediction {
	t.Helper()
	policy := validTestPolicy(t)
	artifact, kv := int64(10<<30), int64(2<<30)
	prediction, err := BuildPrediction(policy, PredictionInput{
		CreatedAt: time.Now().UTC(), ArtifactSHA256: testArtifactDigest,
		ResourceDomain: DomainAccelerator, RequestedContext: 32768,
		KVElementBytes: 2, PlacementAssumption: "runtime default",
		ArtifactBytes: &artifact, KVBytes: &kv, Excluded: []string{"runtime buffers"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return prediction
}
