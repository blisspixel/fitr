package main

import (
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/device"
)

func TestRunNamesTheComparabilityGateThatActuallyFailed(t *testing.T) {
	for _, scenario := range []struct {
		name          string
		contextKnown  bool
		configuration string
		want          string
	}{
		{"unobserved compute", true, device.ConfigSourceOwnedLaunch, "the serving runtime's compute backend is unobserved"},
		{"context before configuration", false, device.ConfigSourceUnobserved, "effective context is unverified"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			result := golden(t)
			sealCurrentResult(t, result)
			identity, provenance := result.Manifest.Model, *result.Manifest.Provenance
			result.Manifest, result.Completion, result.DeviceV2 = nil, nil, nil
			result.Device.ConfigSource = scenario.configuration
			result.Device.AccelSource = device.AccelSourceUnobserved
			result.Device.GPUBackend = ""
			result.DeviceKey = result.Device.Key()
			display := newBackendTraceDisplay(t)
			run := runExecution{result: result, display: display, provenance: provenance,
				resolved: resolvedRunModel{Identity: identity}}
			context := device.ContextVerification{RequestedTokens: result.NumCtx}
			if scenario.contextKnown {
				context.EffectiveTokens = &result.NumCtx
				context.EffectiveSource = device.ContextSourceRuntimeReport
			}
			if err := run.sealFingerprint(context); err != nil {
				t.Fatal(err)
			}
			if _, err := result.ComparableDeviceKey(); err == nil {
				t.Fatal("missing comparison evidence gained a comparison key")
			}
			want := "warn:" + scenario.want + "; this run remains visible but is excluded from ranking and comparison"
			if len(display.notes) != 1 || display.notes[0] != want {
				t.Fatalf("comparison diagnostic = %q, want %q", strings.Join(display.notes, "\n"), want)
			}
		})
	}
}
