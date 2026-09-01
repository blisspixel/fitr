package decision

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadSpecIsStrictAndValidated(t *testing.T) {
	directory := t.TempDir()
	validPath := filepath.Join(directory, "valid.json")
	valid := `{
  "schema": "fitr.decision.spec.v1",
  "name": "local coding",
  "evidence_level": "decide",
  "requirements": [
    {"id": "context", "context": {"minimum_effective_tokens": 16384}},
    {"id": "memory", "capacity": {"maximum_resident_gb": 22, "requested_context": 16384}}
  ]
}`
	if err := os.WriteFile(validPath, []byte(valid), 0o600); err != nil {
		t.Fatal(err)
	}
	spec, err := LoadSpec(validPath)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Name != "local coding" || spec.Requirements[1].Capacity.MaximumResidentGB != 22 {
		t.Fatalf("loaded spec = %+v", spec)
	}

	for name, contents := range map[string]string{
		"duplicate": strings.Replace(valid, `"name": "local coding"`, `"name": "local coding", "name": "other"`, 1),
		"unknown":   strings.Replace(valid, `"name": "local coding"`, `"name": "local coding", "naem": "typo"`, 1),
		"trailing":  valid + `{}`,
		"invalid":   strings.Replace(valid, `"evidence_level": "decide"`, `"evidence_level": "guess"`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(directory, name+".json")
			if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadSpec(path); err == nil {
				t.Fatal("invalid spec was accepted")
			}
		})
	}
}

func TestDecisionSpecRejectsUntypedAndMetadataOnlyBehaviorNeeds(t *testing.T) {
	for _, need := range []string{
		"strctured_output", "vision", " structured_output", "fast_and_decent", "low_footprint",
	} {
		spec := DecisionSpec{
			Schema: SpecSchema, Name: "behavior boundary", Evidence: EvidenceDecide,
			Requirements: []Requirement{{
				ID: "behavior", Behavior: &BehaviorRequirement{Need: need, RequiredState: "PASS"},
			}},
		}
		if err := spec.Validate(); err == nil {
			t.Fatalf("behavior need %q was accepted", need)
		}
	}
}

func TestDecisionSpecRejectsUnknownObjectiveMetric(t *testing.T) {
	spec := DecisionSpec{
		Schema: SpecSchema, Name: "objective boundary", Evidence: EvidenceDecide,
		Requirements: []Requirement{{ID: "context", Context: &ContextRequirement{MinimumEffectiveTokens: 4096}}},
		Objective:    &Objective{Metric: "deocde_tps", Direction: Maximize},
	}
	if err := spec.Validate(); err == nil || !strings.Contains(err.Error(), "objective metric") {
		t.Fatalf("unknown objective metric error = %v", err)
	}
}
