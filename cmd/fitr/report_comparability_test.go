package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/device"
	"github.com/blisspixel/fitr/internal/record"
)

func reportGateFixture(t *testing.T, gate string) *Result {
	t.Helper()
	r := golden(t)
	r.Model, r.Scorecard.Model = gate, gate
	if gate == "contaminated" {
		r.Contamination = []string{"another model was resident"}
	}
	sealCurrentResult(t, r)
	identity, provenance, profile := r.Manifest.Model, *r.Manifest.Provenance, r.Completion.Profile
	verification := r.DeviceV2.Context
	switch gate {
	case "context":
		verification.EffectiveTokens, verification.EffectiveSource = nil, ""
	case "config":
		r.Device.ConfigSource = device.ConfigSourceUnobserved
	case "compute", "observed", "contaminated":
		r.Device.AccelSource, r.Device.GPUBackend = device.AccelSourceUnobserved, ""
	}
	if gate == "observed" {
		identity.Kind, identity.Binding = record.IdentityLocalFile, record.IdentityBindingObserved
	}
	fingerprint, err := device.NewFingerprintV2(r.Device, verification)
	if err != nil {
		t.Fatal(err)
	}
	r.DeviceV2 = &fingerprint
	r.DeviceKey, err = fingerprint.EvidenceKey()
	if err != nil {
		t.Fatal(err)
	}
	r.Manifest, r.Completion = nil, nil
	if err := r.AttachManifest(identity, provenance); err != nil {
		t.Fatal(err)
	}
	if err := r.CompleteEvidence(profile); err != nil {
		t.Fatal(err)
	}
	if err := r.ValidateEvidenceContract(); err != nil {
		t.Fatalf("fixture must remain valid sealed evidence: %v", err)
	}
	return r
}

func TestBoardReportsTheMissingComparisonEvidence(t *testing.T) {
	for _, tc := range []struct{ gate, key, explanation string }{
		{"context", "context_unverified_excluded", "without verified effective context"},
		{"config", "config_unverified_excluded", "without observed serving-runtime configuration"},
		{"compute", "compute_unverified_excluded", "without an observed serving-runtime compute backend"},
		{"observed", "unverified_excluded", "without a valid evidence contract"},
		{"contaminated", "inconclusive_excluded", "contaminated result(s)"},
	} {
		t.Run(tc.gate, func(t *testing.T) {
			t.Setenv("FITR_RESULTS", t.TempDir())
			saveCurrentResults(t, reportGateFixture(t, "clean"), reportGateFixture(t, tc.gate))
			var stdout string
			stderr, code := captureTopStderr(t, func() int {
				var inner int
				stdout, inner = captureTopStdout(t, func() int {
					return cmdBoard(context.Background(), []string{"--display=json"})
				})
				return inner
			})
			var payload map[string]any
			if code != exitOK || json.Unmarshal([]byte(stdout), &payload) != nil {
				t.Fatalf("board exit=%d stdout=%q stderr=%q", code, stdout, stderr)
			}
			if payload[tc.key] != float64(1) || payload["results"] != float64(1) {
				t.Errorf("missing exact exclusion count for %s: %s", tc.gate, stdout)
			}
			for key := range payload {
				if strings.HasSuffix(key, "_excluded") && key != tc.key {
					t.Errorf("row counted again as %s: %s", key, stdout)
				}
			}
			if !strings.Contains(stderr, tc.explanation) || (tc.gate != "context" && strings.Contains(stderr, "verified effective context")) {
				t.Errorf("wrong %s exclusion explanation: %s", tc.gate, stderr)
			}
		})
	}
}

func TestCompareReportsTheMissingComparisonEvidence(t *testing.T) {
	for _, tc := range []struct{ gate, heading string }{
		{"context", "verified effective context is required for comparison"},
		{"config", "observed serving-runtime configuration is required for comparison"},
		{"compute", "an observed serving-runtime compute backend is required for comparison"},
		{"observed", "a valid sealed evidence contract is required for comparison"},
	} {
		t.Run(tc.gate, func(t *testing.T) {
			a, b := reportGateFixture(t, "clean"), reportGateFixture(t, tc.gate)
			stdout, code := captureTopStdout(t, func() int { return validateComparison(a, b) })
			if code != exitError || !strings.Contains(stdout, tc.heading) {
				t.Errorf("compare exit=%d missing %q: %s", code, tc.heading, stdout)
			}
			if tc.gate != "context" && strings.Contains(stdout, "verified effective context") {
				t.Errorf("verified context was mislabeled missing: %s", stdout)
			}
		})
	}
}

func TestBoardCurrentDoesNotCountExcludedOtherMachines(t *testing.T) {
	r := reportGateFixture(t, "compute")
	current := r.Device
	current.GPU += " different"
	groups, order, excluded := groupBoardResults([]*Result{r}, true, current)
	if len(groups) != 0 || len(order) != 0 || !reflect.ValueOf(excluded).IsZero() {
		t.Fatalf("other-machine row affected --current: groups=%v order=%v exclusions=%+v", groups, order, excluded)
	}
}

func TestEmptyBoardNamesEachMissingEvidenceClass(t *testing.T) {
	for _, tc := range []struct {
		excluded boardExclusions
		want     string
	}{
		{boardExclusions{context: 1}, "lacked verified effective context"},
		{boardExclusions{config: 1}, "lacked observed serving-runtime configuration"},
		{boardExclusions{compute: 1}, "lacked an observed serving-runtime compute backend"},
		{boardExclusions{fingerprint: 1}, "lacked a valid comparison fingerprint"},
		{boardExclusions{unverified: 1}, "lacked a valid evidence contract"},
		{boardExclusions{contaminated: 1}, "were contaminated"},
		{boardExclusions{performance: 1}, "lacked supported decode evidence"},
		{boardExclusions{context: 1, compute: 1}, "lacked claimable evidence"},
		{boardExclusions{}, "no results for this machine"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			stderr, code := captureTopStderr(t, func() int { return emptyBoardResult(tc.excluded) })
			if code != exitError || !strings.Contains(stderr, tc.want) {
				t.Fatalf("empty board exit=%d missing %q: %s", code, tc.want, stderr)
			}
		})
	}
}

func TestComparisonGapPreservesTypedWrappedAndUnknownErrors(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want boardExclusions
	}{
		{device.ErrUnverifiedContext, boardExclusions{context: 1}},
		{device.ErrContextProbeMinimum, boardExclusions{context: 1}},
		{fmt.Errorf("wrapped: %w", device.ErrUnobservedConfig), boardExclusions{config: 1}},
		{fmt.Errorf("wrapped: %w", device.ErrUnobservedAccelerator), boardExclusions{compute: 1}},
		{errors.New("unknown future validation failure"), boardExclusions{fingerprint: 1}},
		{errors.New("effective context is unverified"), boardExclusions{fingerprint: 1}},
	} {
		var excluded boardExclusions
		excluded.addComparisonError(tc.err)
		if excluded != tc.want {
			t.Errorf("%v produced %+v, want %+v", tc.err, excluded, tc.want)
		}
	}
}

func TestCompareMixedGapsAndInvalidFingerprintRemainDistinct(t *testing.T) {
	a, b := reportGateFixture(t, "config"), reportGateFixture(t, "compute")
	stdout, code := captureTopStdout(t, func() int { return validateComparison(a, b) })
	for _, want := range []string{"verified comparison evidence is required", "configuration is unobserved", "compute backend is unobserved"} {
		if code != exitError || !strings.Contains(stdout, want) {
			t.Errorf("mixed comparison exit=%d missing %q: %s", code, want, stdout)
		}
	}
	// Validation errors without a classified evidence gap must never become
	// a context-only remedy. The outer comparison still rejects these at its
	// earlier evidence-integrity gate.
	a.DeviceV2.Schema = "invalid"
	stdout, code = captureTopStdout(t, func() int { return validateComparisonKeys(a, b) })
	if code != exitError || !strings.Contains(stdout, "unsupported fingerprint schema") || strings.Contains(stdout, "verified effective context") {
		t.Fatalf("invalid fingerprint became a context gap: exit=%d %s", code, stdout)
	}
	stdout, code = captureTopStdout(t, func() int { return validateComparison(a, b) })
	if code != exitError || !strings.Contains(stdout, "a valid sealed evidence contract is required") {
		t.Fatalf("invalid fingerprint escaped integrity precedence: exit=%d %s", code, stdout)
	}
}
