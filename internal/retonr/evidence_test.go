package retonr

import (
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/device"
	"github.com/blisspixel/fitr/internal/score"
)

func TestEvidenceIsMeasurementNotQualification(t *testing.T) {
	sc := score.Scorecard{
		Needs: map[string]score.Verdict{
			"structured_output": {State: score.Pass, Why: "6/7"},
			"vision":            {State: score.NA, Why: "text-only"},
		},
		Serves: []string{"structured_output"},
		UseFor: "JSON pipelines",
	}
	e := FromScorecard("0.2.0", "demo:8b", "Q4_K_M", "qwen3", "8B", "full", 3,
		device.Fingerprint{GPU: "demo-gpu", Runtime: "0.32.14"},
		"demo-key", "default", sc, "healthy", "/tmp/demo.json")
	if e.Schema != Schema || e.Kind != "device_measurement" {
		t.Fatalf("schema/kind = %s %s", e.Schema, e.Kind)
	}
	if e.Sister != SisterURL {
		t.Fatal("must name the sister project")
	}
	d := strings.ToLower(e.Disclaimer)
	for _, word := range []string{"not a retonr qualification", "not", "activation"} {
		if !strings.Contains(d, word) {
			t.Fatalf("disclaimer must refuse to qualify: %q missing %q", e.Disclaimer, word)
		}
	}
	if e.Needs["structured_output"].State != "PASS" {
		t.Fatalf("need observation lost: %+v", e.Needs)
	}
	if e.Needs["vision"].State != "n/a" {
		t.Fatal("n/a must survive; a missing vision claim is not a fail")
	}
	if _, ok := e.Needs["coding"]; ok {
		t.Fatal("unmeasured needs must stay absent, not invented")
	}
}

func TestHintIsEmptyWithoutRetonr(t *testing.T) {
	// This machine may or may not have retonr. The contract is: empty means
	// "do not mention it", non-empty must refuse to call itself a qualification.
	h := Hint("m")
	if h == "" {
		return
	}
	if !strings.Contains(h, "--retonr") || !strings.Contains(h, "not a qualification") {
		t.Fatalf("hint must stay optional and honest: %q", h)
	}
}
