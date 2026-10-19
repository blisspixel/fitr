package reference

import (
	"strings"
	"testing"
)

func TestDefaultSubsetIsTheAprilCut(t *testing.T) {
	subset := DefaultSubset()
	if len(subset) != 24 {
		t.Fatalf("subset length = %d, want 24", len(subset))
	}
	seen := map[string]bool{}
	for i, item := range subset {
		if seen[item.ID] || !strings.HasPrefix(item.Created, "2025-04-") {
			t.Fatalf("instance %d is not a unique April 2025 id: %+v", i, item)
		}
		if strings.Contains(item.ID, " ") || strings.Contains(item.Created, " ") {
			t.Fatalf("instance carries more than an id and a date: %+v", item)
		}
		seen[item.ID] = true
		if i > 0 && subset[i-1].ID >= item.ID {
			t.Fatalf("subset is not ordered by instance id at %s", item.ID)
		}
	}
	if subset[0].ID != "ASPP__pelita-875" || subset[23].ID != "pyvista__pyvista-7455" {
		t.Fatalf("cut endpoints = %s .. %s", subset[0].ID, subset[23].ID)
	}
}

func TestPlanSealsTheFrozenSubset(t *testing.T) {
	plan := sealedPlan(t, "2024-01-01", fakeSHA("artifact"))
	if err := plan.Validate(); err != nil {
		t.Fatal(err)
	}
	raw, err := planJSON(plan)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodePlan(raw)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.PlanSHA256 != plan.PlanSHA256 || decoded.ManifestSHA256 == "" {
		t.Fatalf("decoded plan did not keep its digests")
	}
}

func TestTamperedPlanDigestIsRejected(t *testing.T) {
	plan := sealedPlan(t, "2024-01-01", fakeSHA("artifact"))
	plan.K = 2
	if err := plan.Validate(); err == nil {
		t.Fatal("a changed k still matched the sealed digest")
	}
}

func TestRawFingerprintKeyIsRejected(t *testing.T) {
	draft := sealedPlan(t, "2024-01-01", fakeSHA("artifact"))
	draft.DeviceKey = "windows-host|NVIDIA|ollama"
	draft.PlanSHA256 = ""
	draft.ManifestSHA256 = ""
	draft.HarnessBindingSHA256 = ""
	if _, err := NewPlan(draft); err == nil {
		t.Fatal("a raw fingerprint key sealed")
	}
}

func TestImageFileMustCoverTheFrozenSubset(t *testing.T) {
	if _, err := ApplyImages([]byte(`{"schema":"fitr.external-reference.images.v1","images":{}}`)); err == nil {
		t.Fatal("an empty image map matched the frozen subset")
	}
	body := `{"schema":"fitr.external-reference.images.v1","images":{"ASPP__pelita-875":"not-a-digest"}}`
	if _, err := ApplyImages([]byte(body)); err == nil {
		t.Fatal("an image name was accepted as a digest")
	}
}

func TestZeroRateDoesNotSeal(t *testing.T) {
	draft := sealedPlan(t, "2024-01-01", fakeSHA("artifact"))
	draft.PrefillTokensPerSecond = 0
	draft.PlanSHA256 = ""
	draft.ManifestSHA256 = ""
	draft.HarnessBindingSHA256 = ""
	if _, err := NewPlan(draft); err == nil {
		t.Fatal("an unmeasured prefill rate sealed as zero")
	}
}
