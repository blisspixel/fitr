package experiment

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/decision"
	"github.com/blisspixel/fitr/internal/record"
)

func TestConfirmationBindsFreshCandidateSetAndRebuildsBundle(t *testing.T) {
	firstTemplate := sealedExperimentRecord(t, 8192, nil, "2026-08-31T10:00:00Z", quantDigestA, "Q8_0", 18)
	secondTemplate := sealedExperimentRecord(t, 8192, nil, "2026-08-31T11:00:00Z", quantDigestB, "Q4_K_M", 24)
	spec := quantDecisionSpec()
	spec.Evidence = decision.EvidenceConfirm
	plan, err := NewConfirmationPlan([]record.ModelIdentity{
		firstTemplate.Manifest.Model, secondTemplate.Manifest.Model,
	}, firstTemplate.Device.Key(), spec, 8192, firstTemplate.Repeats)
	if err != nil {
		t.Fatal(err)
	}
	first := sealedExperimentRecordWithSeed(t, 8192, confirmationBinding(plan, 1),
		"2026-08-31T12:00:00Z", quantDigestA, "Q8_0", 18, plan.SeedSet)
	second := sealedExperimentRecordWithSeed(t, 8192, confirmationBinding(plan, 2),
		"2026-08-31T13:00:00Z", quantDigestB, "Q4_K_M", 24, plan.SeedSet)
	bundle, err := NewConfirmationBundle(plan, spec, []*record.Record{first, second})
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Report.Stage != StageConfirm || !bundle.Report.Comparison.Ready {
		t.Fatalf("confirmation report = %+v", bundle.Report)
	}
	data, err := bundle.JSON()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "confirmation.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadConfirmationBundle(path)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Report.Comparison.Ready = false
	if _, err := loaded.Validate(); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("tampered confirmation report error = %v", err)
	}
}

func TestConfirmationRejectsReusedOrUnboundEvidence(t *testing.T) {
	first := sealedExperimentRecord(t, 8192, nil, "2026-08-31T10:00:00Z", quantDigestA, "Q8_0", 18)
	second := sealedExperimentRecord(t, 8192, nil, "2026-08-31T11:00:00Z", quantDigestB, "Q4_K_M", 24)
	spec := quantDecisionSpec()
	spec.Evidence = decision.EvidenceConfirm
	plan, err := NewConfirmationPlan([]record.ModelIdentity{first.Manifest.Model, second.Manifest.Model},
		first.Device.Key(), spec, 8192, first.Repeats)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AnalyzeConfirmation([]*record.Record{first, second}, plan, spec); err == nil {
		t.Fatal("ordinary exploration evidence was reused as confirmation")
	}
	changed := spec
	changed.Name = "changed after plan sealing"
	if err := plan.Validate(changed); err == nil {
		t.Fatal("changed decision spec was accepted by the confirmation plan")
	}
}

func TestConfirmationPlanRejectsAmbiguousOrChangedInputs(t *testing.T) {
	first := sealedExperimentRecord(t, 8192, nil, "2026-08-31T10:00:00Z", quantDigestA, "Q8_0", 18)
	second := sealedExperimentRecord(t, 8192, nil, "2026-08-31T11:00:00Z", quantDigestB, "Q4_K_M", 24)
	identities := []record.ModelIdentity{first.Manifest.Model, second.Manifest.Model}
	decideSpec := quantDecisionSpec()
	if _, err := NewConfirmationPlan(identities, first.Device.Key(), decideSpec, 8192, 3); err == nil {
		t.Fatal("decide-level spec created a confirmation plan")
	}
	confirmSpec := decideSpec
	confirmSpec.Evidence = decision.EvidenceConfirm
	for _, test := range []struct {
		name       string
		candidates []record.ModelIdentity
		device     string
		context    int
		repeats    int
	}{
		{"one candidate", identities[:1], first.Device.Key(), 8192, 3},
		{"duplicate artifact", []record.ModelIdentity{identities[0], identities[0]}, first.Device.Key(), 8192, 3},
		{"missing device", identities, "", 8192, 3},
		{"context bound", identities, first.Device.Key(), maximumConfirmationCtx + 1, 3},
		{"too few repeats", identities, first.Device.Key(), 8192, 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewConfirmationPlan(test.candidates, test.device, confirmSpec,
				test.context, test.repeats); err == nil {
				t.Fatal("invalid confirmation plan was accepted")
			}
		})
	}
}

func confirmationBinding(plan ConfirmationPlan, index int) *record.ExperimentBinding {
	binding := ConfirmationPlanBinding(plan, index)
	return &binding
}
