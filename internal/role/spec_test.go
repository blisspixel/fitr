package role

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/decision"
	"github.com/blisspixel/fitr/internal/score"
)

func persistenceSpec() Spec {
	return Spec{
		Schema: SpecSchema, Name: "coding", MaxAgeDays: 30,
		Decision: decision.DecisionSpec{Schema: decision.SpecSchema, Name: "Coding role", Evidence: decision.EvidenceDecide,
			Requirements: []decision.Requirement{
				{ID: "quality", Behavior: &decision.BehaviorRequirement{Need: "coding", RequiredState: score.Pass}},
				{ID: "context", Context: &decision.ContextRequirement{MinimumEffectiveTokens: 4096}},
				{ID: "memory", Capacity: &decision.CapacityRequirement{MaximumResidentBytes: 16 << 30}},
			}},
		Preferences: []Preference{{Requirement: "memory", Weight: 1, Worst: 32 << 30, Best: 0}},
	}
}

func persistenceNumber(value float64) *float64 { return &value }

func TestSpecIdentityAndDigest(t *testing.T) {
	spec := persistenceSpec()
	digest, err := spec.Digest()
	if err != nil || !roleDigestValid(digest) {
		t.Fatalf("digest=%q err=%v", digest, err)
	}
	again, err := spec.Digest()
	if err != nil || again != digest {
		t.Fatalf("repeated digest=%q err=%v", again, err)
	}
	spec.Description = "A different requirement revision"
	changed, err := spec.Digest()
	if err != nil || changed == digest {
		t.Fatalf("changed digest=%q err=%v", changed, err)
	}
}

func TestSpecRejectsInvalidRoleContract(t *testing.T) {
	tests := map[string]func(*Spec){
		"schema":              func(s *Spec) { s.Schema = "other" },
		"path name":           func(s *Spec) { s.Name = "../escape" },
		"uppercase":           func(s *Spec) { s.Name = "Coding" },
		"long name":           func(s *Spec) { s.Name = strings.Repeat("a", 65) },
		"description control": func(s *Spec) { s.Description = "hello\x1b[0m" },
		"description bidi":    func(s *Spec) { s.Description = "hello\u202e" },
		"description long":    func(s *Spec) { s.Description = strings.Repeat("a", 513) },
		"description UTF8":    func(s *Spec) { s.Description = string([]byte{255}) },
		"age zero":            func(s *Spec) { s.MaxAgeDays = 0 },
		"age excessive":       func(s *Spec) { s.MaxAgeDays = 3651 },
		"invalid decision":    func(s *Spec) { s.Decision.Schema = "other" },
		"screen evidence":     func(s *Spec) { s.Decision.Evidence = decision.EvidenceScreen },
		"confirmation":        func(s *Spec) { s.Decision.Confirmation.FreshEvidence = true },
		"fresh tasks":         func(s *Spec) { s.Decision.Confirmation.FreshTasks = true },
		"objective": func(s *Spec) {
			s.Decision.Objective = &decision.Objective{Metric: "decode_tps", Direction: decision.Maximize}
		},
		"implicit quality": func(s *Spec) { s.Decision.Requirements[0].Behavior.RequiredState = "" },
		"no context": func(s *Spec) {
			s.Decision.Requirements = append(s.Decision.Requirements[:1], s.Decision.Requirements[2])
		},
		"no capacity":          func(s *Spec) { s.Decision.Requirements = s.Decision.Requirements[:2] },
		"control ID":           func(s *Spec) { s.Decision.Requirements[0].ID = "q\x1b" },
		"no preferences":       func(s *Spec) { s.Preferences = nil },
		"unknown preference":   func(s *Spec) { s.Preferences[0].Requirement = "other" },
		"duplicate preference": func(s *Spec) { s.Preferences = append(s.Preferences, s.Preferences[0]) },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			spec := persistenceSpec()
			mutate(&spec)
			if _, err := spec.Digest(); err == nil {
				t.Fatal("invalid spec accepted")
			}
		})
	}
}

func TestPreferenceNumericContracts(t *testing.T) {
	tests := []struct {
		name        string
		requirement decision.Requirement
		preference  Preference
		valid       bool
	}{
		{"rate", decision.Requirement{Behavior: &decision.BehaviorRequirement{MinimumRate: persistenceNumber(.8)}}, Preference{Weight: 1, Worst: 0, Best: 1}, true},
		{"rate above one", decision.Requirement{Behavior: &decision.BehaviorRequirement{MinimumRate: persistenceNumber(.8)}}, Preference{Weight: 1, Worst: 0, Best: 2}, false},
		{"rate reversed", decision.Requirement{Behavior: &decision.BehaviorRequirement{MinimumRate: persistenceNumber(.8)}}, Preference{Weight: 1, Worst: 1, Best: 0}, false},
		{"throughput", decision.Requirement{Performance: &decision.PerformanceRequirement{AtLeast: persistenceNumber(10)}}, Preference{Weight: 1, Worst: 0, Best: 100}, true},
		{"latency", decision.Requirement{Performance: &decision.PerformanceRequirement{AtMost: persistenceNumber(10)}}, Preference{Weight: 1, Worst: 100, Best: 0}, true},
		{"context", decision.Requirement{Context: &decision.ContextRequirement{}}, Preference{Weight: 1, Worst: 0, Best: 100}, false},
		{"state", decision.Requirement{Behavior: &decision.BehaviorRequirement{RequiredState: score.Pass}}, Preference{Weight: 1, Worst: 0, Best: 1}, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validatePreference(test.preference, test.requirement)
			if (err == nil) != test.valid {
				t.Fatalf("err=%v valid=%v", err, test.valid)
			}
		})
	}
}

func TestPreferenceRejectsUnsafeNumbers(t *testing.T) {
	tests := []Preference{
		{Weight: 0, Worst: 1}, {Weight: -1, Worst: 1}, {Weight: 1e6 + 1, Worst: 1},
		{Weight: math.NaN(), Worst: 1}, {Weight: math.Inf(1), Worst: 1},
		{Weight: 1, Worst: math.Inf(1)}, {Weight: 1, Best: math.NaN()},
		{Weight: 1, Worst: -1}, {Weight: 1, Best: -1},
		{Weight: 1, Worst: 1e19}, {Weight: 1, Best: 1e19}, {Weight: 1},
	}
	for index, preference := range tests {
		spec := persistenceSpec()
		preference.Requirement = "memory"
		spec.Preferences[0] = preference
		if spec.Validate() == nil {
			t.Fatalf("unsafe preference %d accepted", index)
		}
	}
	spec := persistenceSpec()
	spec.Decision.Requirements[0].Behavior = &decision.BehaviorRequirement{Need: "tool_calling", MinimumRate: persistenceNumber(.8)}
	if err := spec.Validate(); err != nil {
		t.Fatal(err)
	}
	spec.Decision.Requirements[0].Behavior.MinimumRate = persistenceNumber(0)
	if spec.Validate() == nil {
		t.Fatal("zero quality floor accepted")
	}
}

func TestRoleRejectsUnavailableBehaviorRatesAndAllowsStateFloors(t *testing.T) {
	for _, need := range []string{"coding", "unattended_agentic", "uncensored", "output_health"} {
		spec := persistenceSpec()
		spec.Decision.Requirements[0].Behavior = &decision.BehaviorRequirement{Need: need, MinimumRate: persistenceNumber(0.8)}
		if err := spec.Validate(); err == nil || !strings.Contains(err.Error(), "required_state") {
			t.Fatalf("unavailable %s rate did not provide an actionable error: %v", need, err)
		}
		spec.Decision.Requirements[0].Behavior = &decision.BehaviorRequirement{Need: need, RequiredState: score.Pass}
		if err := spec.Validate(); err != nil {
			t.Fatalf("supported %s categorical floor was rejected: %v", need, err)
		}
	}
	for _, need := range []string{"structured_output", "instruction_precision", "reasoning", "tool_calling", "tool_restraint", "user_tasks"} {
		spec := persistenceSpec()
		spec.Decision.Requirements[0].Behavior = &decision.BehaviorRequirement{Need: need, MinimumRate: persistenceNumber(0.8)}
		if err := spec.Validate(); err != nil {
			t.Fatalf("supported %s rate floor was rejected: %v", need, err)
		}
	}
}

func TestLoadSpecStrictAndBounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spec.json")
	data, err := json.Marshal(persistenceSpec())
	if err != nil {
		t.Fatal(err)
	}
	tests := [][]byte{
		[]byte(`null`), []byte(`{}`), []byte(`{"schema":"x","schema":"y"}`),
		append(append([]byte{}, data...), []byte(` {}`)...),
		[]byte(strings.Replace(string(data), `"weight":1`, `"weight":1,"unknown":true`, 1)),
		[]byte(strings.Repeat(" ", maximumLibraryBytes+1)),
	}
	for _, invalid := range tests {
		if err := os.WriteFile(path, invalid, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadSpec(path); err == nil {
			t.Fatal("invalid spec JSON accepted")
		}
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if spec, err := LoadSpec(path); err != nil || spec.Name != "coding" {
		t.Fatalf("spec=%+v err=%v", spec, err)
	}
	if _, err := LoadSpec(path + ".missing"); err == nil {
		t.Fatal("missing spec accepted")
	}
}
