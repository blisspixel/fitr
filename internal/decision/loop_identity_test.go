package decision

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/analysis"
	"github.com/blisspixel/fitr/internal/record"
)

func TestServingLoopObservationsAreNotOneConfiguration(t *testing.T) {
	unobserved := loopConfiguration(t, nil, "")
	again := loopConfiguration(t, nil, "")
	if unobserved.ServingLoopCount != nil || unobserved.RecurrentKVCacheMode != "" {
		t.Fatal("file num_loops was copied into the sealed configuration")
	}
	if !strings.Contains(unobserved.LoopObservation(), "serving loop count unobserved") ||
		!strings.Contains(unobserved.LoopObservation(), "serving recurrent KV cache mode unobserved") {
		t.Fatalf("unobserved text = %q", unobserved.LoopObservation())
	}
	encoded, err := json.Marshal(unobserved)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "serving_loop_count") || strings.Contains(string(encoded), "recurrent_kv_cache_mode") {
		t.Fatalf("unobserved configuration changed the digest payload: %s", encoded)
	}
	if !OneConfiguration(unobserved, again) {
		t.Fatal("two unobserved results of the same artifact diverged")
	}
	four := 4
	perLoop := loopConfiguration(t, &four, record.RecurrentKVCachePerLoop)
	one := 1
	lastLoop := loopConfiguration(t, &one, record.RecurrentKVCacheLastLoop)
	countOnly := loopConfiguration(t, &four, "")
	if OneConfiguration(perLoop, lastLoop) || OneConfiguration(perLoop, countOnly) || OneConfiguration(perLoop, unobserved) {
		t.Fatal("different loop observations were ranked as one configuration")
	}
	if perLoop.ID == "" || !strings.Contains(perLoop.LoopObservation(), "serving loop count 4") ||
		!strings.Contains(perLoop.LoopObservation(), "per_loop") {
		t.Fatalf("observed configuration = %+v %q", perLoop, perLoop.LoopObservation())
	}
	if !strings.Contains(countOnly.LoopObservation(), "serving recurrent KV cache mode unobserved") {
		t.Fatalf("partial observation = %q", countOnly.LoopObservation())
	}
	report, err := analysis.FromRecord(loopRecord(t, nil, ""))
	if err != nil {
		t.Fatal(err)
	}
	if report.ServingLoop != "" || !strings.Contains(report.ServingLoopObservation(), "unobserved") {
		t.Fatalf("analysis stored an unobserved sentence: %q / %q", report.ServingLoop, report.ServingLoopObservation())
	}
	observedReport, err := analysis.FromRecord(loopRecord(t, &four, record.RecurrentKVCachePerLoop))
	if err != nil {
		t.Fatal(err)
	}
	if observedReport.ServingLoop != perLoop.LoopObservation() {
		t.Fatalf("analysis = %q, configuration = %q", observedReport.ServingLoop, perLoop.LoopObservation())
	}
}

func loopConfiguration(t *testing.T, count *int, mode string) ConfigurationUnderTest {
	t.Helper()
	subject, err := ConfigurationFromRecord(loopRecord(t, count, mode))
	if err != nil {
		t.Fatal(err)
	}
	return subject
}

func loopRecord(t *testing.T, count *int, mode string) *record.Record {
	t.Helper()
	checks := decisionTestChecks()
	planDigest, err := record.ObservedCheckPlanSHA256(checks)
	if err != nil {
		t.Fatal(err)
	}
	result := newDecisionTestRecord(checks, planDigest)
	attachDecisionTestDevice(t, result)
	if count != nil || mode != "" {
		if err := result.ObserveServingLoop(count, mode); err != nil {
			t.Fatal(err)
		}
	}
	sealDecisionTestRecord(t, result, decisionTestProfile())
	return result
}
