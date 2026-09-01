package decision

import (
	"testing"

	"github.com/blisspixel/fitr/internal/device"
)

func TestSplitLegacyProfileSeparatesPolicyMeanings(t *testing.T) {
	profiles, err := device.LoadEmbeddedProfiles()
	if err != nil {
		t.Fatal(err)
	}
	var profile device.Profile
	for _, candidate := range profiles {
		if candidate.Name == "default" {
			profile = candidate
			break
		}
	}
	if profile.Name == "" {
		t.Fatal("embedded default profile not found")
	}
	bundle, err := SplitLegacyProfile(profile)
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Capacity == nil || bundle.Capacity.RequestedContext != 32768 ||
		bundle.Capacity.MaximumResidentBytes != 24*device.GB {
		t.Fatalf("capacity policy = %+v", bundle.Capacity)
	}
	if _, mixed := bundle.Grading.Performance["always_on_capable"]; mixed {
		t.Fatal("capacity gate remained mixed into grading performance")
	}
	if got := bundle.Grading.Rates["tool_calling"].Minimum; got != 0.7 {
		t.Fatalf("tool screening rate = %v", got)
	}
	if bundle.Presentation.Name != profile.Name || bundle.Calibration.Hints == nil {
		t.Fatalf("presentation/calibration split = %+v", bundle)
	}
	for _, unclassified := range bundle.Unclassified {
		if unclassified == "always_on_capable" {
			t.Fatal("known capacity gate was marked unclassified")
		}
	}

	bundle.Calibration.Match["host"] = "changed"
	if profile.Match["host"] == "changed" {
		t.Fatal("policy adapter aliased the mutable profile match map")
	}
}

func TestSplitLegacyProfileDisclosesUnknownExtensions(t *testing.T) {
	profile := device.Profile{
		Name: "custom", Gates: map[string]device.Gate{
			"custom_probe": {"threshold": 1.0},
		},
	}
	bundle, err := SplitLegacyProfile(profile)
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Unclassified) != 1 || bundle.Unclassified[0] != "custom_probe" {
		t.Fatalf("unclassified gates = %v", bundle.Unclassified)
	}
}

func TestMeasurementProtocolComesFromSealedManifest(t *testing.T) {
	result := sealedDecisionRecord(t)
	protocol, err := ProtocolFromRecord(result)
	if err != nil {
		t.Fatal(err)
	}
	if protocol.Schema != MeasurementProtocolSchema || protocol.ManifestSHA256 == "" ||
		protocol.RequestedContext != result.NumCtx || protocol.TaskPlan.CheckTrialsLimit != 3 {
		t.Fatalf("measurement protocol = %+v", protocol)
	}

	result.Repeats++
	if _, err := ProtocolFromRecord(result); err == nil {
		t.Fatal("mutated manifest inputs produced a measurement protocol")
	}
}
