package main

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/decision"
	"github.com/blisspixel/fitr/internal/device"
	"github.com/blisspixel/fitr/internal/eval"
	"github.com/blisspixel/fitr/internal/experiment"
	"github.com/blisspixel/fitr/internal/record"
	"github.com/blisspixel/fitr/internal/stats"
)

func TestTimedConfirmationRefusesBeforeARuntime(t *testing.T) {
	machineErrors = false
	t.Cleanup(func() { machineErrors = false })
	dir := t.TempDir()
	spec := filepath.Join(dir, "timed.json")
	if err := os.WriteFile(spec, []byte(timedConfirmSpec), 0o600); err != nil {
		t.Fatal(err)
	}
	bytesSpec := filepath.Join(dir, "bytes.json")
	if err := os.WriteFile(bytesSpec, []byte(byteConfirmSpec), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	stderr, code := captureTopStderr(t, func() int {
		return cmdExperiment(ctx, []string{"confirm", "model-a", "model-b", "--spec", spec, "--min-gap", "0.08"})
	})
	if code != exitUnresolved || !strings.Contains(stderr, "--noise-from") {
		t.Fatalf("missing noise exit=%d stderr=%q", code, stderr)
	}

	stderr, code = captureTopStderr(t, func() int {
		return cmdExperiment(ctx, []string{"confirm", "model-a", "model-b", "--spec", spec, "--min-gap", "0", "--noise-from", "missing.json"})
	})
	if code != exitUsage || !strings.Contains(stderr, "min-gap") {
		t.Fatalf("zero gap exit=%d stderr=%q", code, stderr)
	}

	stderr, code = captureTopStderr(t, func() int {
		return cmdExperiment(ctx, []string{"confirm", "model-a", "model-b", "--spec", spec, "--min-gap", "8%", "--noise-from", "missing.json"})
	})
	if code != exitUsage || !strings.Contains(stderr, "min-gap") {
		t.Fatalf("percent gap exit=%d stderr=%q", code, stderr)
	}

	stderr, code = captureTopStderr(t, func() int {
		return cmdExperiment(ctx, []string{"confirm", filepath.Join(dir, "bundle.json"), "--min-gap", "0.08"})
	})
	if code != exitUsage || !strings.Contains(stderr, "saved bundle") {
		t.Fatalf("bundle gap exit=%d stderr=%q", code, stderr)
	}

	stderr, code = captureTopStderr(t, func() int {
		return cmdExperiment(ctx, []string{"confirm", "model-a", "model-b", "--spec", bytesSpec, "--min-gap", "0.08", "--noise-from", "missing.json"})
	})
	if code != exitUsage || !strings.Contains(stderr, "timed confirmation") {
		t.Fatalf("byte objective exit=%d stderr=%q", code, stderr)
	}

	stderr, code = captureTopStderr(t, func() int {
		return cmdExperiment(ctx, []string{"confirm", "model-a", "model-b", "--spec", spec, "--min-gap", "0.08", "--noise-from", filepath.Join(dir, "absent.json")})
	})
	if code != exitError || !strings.Contains(stderr, "could not read noise") {
		t.Fatalf("absent noise exit=%d stderr=%q", code, stderr)
	}
}

func TestExternalNoiseCannotAuthorizeABudget(t *testing.T) {
	machineErrors = false
	t.Cleanup(func() { machineErrors = false })
	dir := t.TempDir()
	spec := filepath.Join(dir, "timed.json")
	if err := os.WriteFile(spec, []byte(timedConfirmSpec), 0o600); err != nil {
		t.Fatal(err)
	}
	noise := filepath.Join(dir, "noise.json")
	body, err := os.ReadFile(filepath.Join("testdata", "golden_result.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(noise, body, 0o600); err != nil {
		t.Fatal(err)
	}
	stderr, code := captureTopStderr(t, func() int {
		return cmdExperiment(context.Background(), []string{
			"confirm", "model-a", "model-b", "--spec", spec, "--min-gap", "0.08", "--noise-from", noise,
		})
	})
	if code != exitUnresolved || !strings.Contains(stderr, "not claimable") {
		t.Fatalf("external noise exit=%d stderr=%q", code, stderr)
	}
}

func TestRefuseInseparableBudgetNamesTheRepeatCount(t *testing.T) {
	machineErrors = false
	t.Cleanup(func() { machineErrors = false })
	stderr, code := captureTopStderr(t, func() int {
		code, _ := refuseInseparableBudget(0.016, 0.08, 3)
		return code
	})
	if code != exitUnresolved || !strings.Contains(stderr, "-k 4") || !strings.Contains(stderr, "cannot separate") {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	stderr, code = captureTopStderr(t, func() int {
		code, ok := refuseInseparableBudget(0.016, 0.08, 4)
		if !ok {
			t.Fatal(stderr)
		}
		return code
	})
	if code != exitOK || stderr != "" {
		t.Fatalf("separable budget exit=%d stderr=%q", code, stderr)
	}
}

func TestNoiseDeviceRefusalStaysUnresolved(t *testing.T) {
	machineErrors = false
	t.Cleanup(func() { machineErrors = false })
	noise := &record.Record{DeviceV2: &device.FingerprintV2{Device: device.Fingerprint{GPU: "a"}}}
	stderr, code := captureTopStderr(t, func() int {
		code, _ := applyConfirmationBudget("decode_tps", 0.08, noise, device.Fingerprint{GPU: "b"}, 3)
		return code
	})
	if code != exitUnresolved || !strings.Contains(stderr, "fingerprint") {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if code, ok := applyConfirmationBudget("decode_tps", 0.08, nil, device.Fingerprint{}, 3); !ok || code != exitOK {
		t.Fatalf("untimed budget = %d %v", code, ok)
	}
}

func TestClaimableNoiseSetsTheBudgetBeforeARuntime(t *testing.T) {
	machineErrors = false
	t.Cleanup(func() { machineErrors = false })
	t.Setenv("FITR_RESULTS", t.TempDir())
	t.Setenv("FITR_BACKEND", "invalid-must-not-run")
	saved, err := save(mockResult("noise", 20, 0.4, 200, 0, 1, 1, 1, 1))
	if err != nil {
		t.Fatal(err)
	}
	specPath := filepath.Join(t.TempDir(), "timed.json")
	if err := os.WriteFile(specPath, []byte(timedConfirmSpec), 0o600); err != nil {
		t.Fatal(err)
	}
	spec, err := decision.LoadSpec(specPath)
	if err != nil {
		t.Fatal(err)
	}
	gap, noise, code, ok := loadConfirmationNoise(spec, confirmationCommand{
		minGap: "0.08", noiseFrom: saved, ctx: eval.NumCtx,
	})
	if !ok || code != exitOK || gap != 0.08 || noise == nil || noise.EvidenceIntegrityIssue() != "" {
		t.Fatalf("claimable noise code=%d ok=%v gap=%v issue=%v err path %s", code, ok, gap, noise, saved)
	}
	cv, err := experiment.ClaimableVariation(noise, "decode_tps")
	if err != nil {
		t.Fatal(err)
	}
	separation, err := stats.PredictMeanSeparation(cv, 1+gap, 3)
	if err != nil || separation.Separable || separation.RequiredRepeats < 4 {
		t.Fatalf("fixture no longer refuses three repeats: %+v %v", separation, err)
	}
	_, _, code, ok = loadConfirmationNoise(spec, confirmationCommand{
		minGap: "0.08", noiseFrom: saved, ctx: eval.NumCtx - 1,
	})
	if ok || code != exitUnresolved {
		t.Fatalf("wrong context code=%d ok=%v", code, ok)
	}
	stderr, code := captureTopStderr(t, func() int {
		code, _ := applyConfirmationBudget("decode_tps", gap, noise, noise.DeviceV2.Device, 3)
		return code
	})
	if code != exitUnresolved || !strings.Contains(stderr, "-k "+strconv.Itoa(separation.RequiredRepeats)) {
		t.Fatalf("matching device exit=%d stderr=%q", code, stderr)
	}
	if code, ok = applyConfirmationBudget("decode_tps", gap, noise, noise.DeviceV2.Device, separation.RequiredRepeats); !ok || code != exitOK {
		t.Fatalf("separable budget = %d %v", code, ok)
	}
}

func TestUntimedConfirmationKeepsTheExistingPath(t *testing.T) {
	machineErrors = false
	t.Cleanup(func() { machineErrors = false })
	if objectiveMetric(decision.DecisionSpec{}) != "" {
		t.Fatal("a spec with no objective named a metric")
	}
	specPath := filepath.Join(t.TempDir(), "bytes.json")
	if err := os.WriteFile(specPath, []byte(byteConfirmSpec), 0o600); err != nil {
		t.Fatal(err)
	}
	spec, err := decision.LoadSpec(specPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, code, ok := loadConfirmationNoise(spec, confirmationCommand{}); !ok || code != exitOK {
		t.Fatalf("byte objective without a gap = %d %v", code, ok)
	}
}

func TestUnresolvableBudgetNamesTheLimit(t *testing.T) {
	machineErrors = false
	t.Cleanup(func() { machineErrors = false })
	stderr, code := captureTopStderr(t, func() int {
		code, _ := refuseInseparableBudget(0, 0.08, 3)
		return code
	})
	if code != exitUnresolved || !strings.Contains(stderr, "unmeasured") {
		t.Fatalf("unmeasured variation exit=%d stderr=%q", code, stderr)
	}
	stderr, code = captureTopStderr(t, func() int {
		code, _ := refuseInseparableBudget(0.5, 0.01, 3)
		return code
	})
	if code != exitUnresolved || !strings.Contains(stderr, "no repeat count") {
		t.Fatalf("inseparable window exit=%d stderr=%q", code, stderr)
	}
	noise := &record.Record{DeviceV2: &device.FingerprintV2{Device: device.Fingerprint{GPU: "a"}}}
	stderr, code = captureTopStderr(t, func() int {
		code, _ := applyConfirmationBudget("decode_tps", 0.08, noise, noise.DeviceV2.Device, 3)
		return code
	})
	if code != exitUnresolved || !strings.Contains(stderr, "not claimable") {
		t.Fatalf("matched unclaimable noise exit=%d stderr=%q", code, stderr)
	}
}

const timedConfirmSpec = `{
  "schema": "fitr.decision.spec.v1",
  "name": "timed",
  "evidence_level": "confirm",
  "requirements": [{"id": "context", "context": {"minimum_effective_tokens": 4096}}],
  "objective": {"metric": "decode_tps", "direction": "maximize"}
}`

const byteConfirmSpec = `{
  "schema": "fitr.decision.spec.v1",
  "name": "bytes",
  "evidence_level": "confirm",
  "requirements": [{"id": "context", "context": {"minimum_effective_tokens": 4096}}],
  "objective": {"metric": "artifact_bytes", "direction": "minimize"}
}`
