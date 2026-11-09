package experiment

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

func TestUnknownObservationKindIsRejected(t *testing.T) {
	plan := sealedPlan(t, ModelSetRuntimeOllama)
	observation := sizedObservation("co_resident", 1, "resident", "resident")
	if _, err := AnalyzeModelSet(plan, observation); err == nil {
		t.Fatal("an unknown kind was analyzed")
	}
	raw := []byte(`{"schema":"fitr.experiment.modelset.observation.v1","kind":"ollama_ps","bodies":1,"other_percent":12}`)
	if _, err := DecodeModelSetObservation(raw); err == nil {
		t.Fatal("a percentage field was accepted")
	}
}

func TestOllamaProcessListIsNotCoResidency(t *testing.T) {
	plan := sealedPlan(t, ModelSetRuntimeOllama)
	observation := sizedObservation(ObservationOllamaPS, 1, "resident", "resident")
	report := mustAnalyze(t, plan, observation)
	if report.CoResident == nil || *report.CoResident {
		t.Fatal("ollama process list was labeled co-resident")
	}
	if report.Claim != ModelSetClaimUnresolved || report.Disposition != DispositionNotOneObservation {
		t.Fatalf("claim=%s disposition=%s", report.Claim, report.Disposition)
	}
	rejectNumbers(t, report, 111111, 222222, 333333)
	if !containsAll(report.DoesNotEstablish, "simultaneous_residency", "additive_fit", "single_model_board", "exact_context_other") {
		t.Fatalf("refusals = %v", report.DoesNotEstablish)
	}
}

func TestSeparateCallsStaySeparate(t *testing.T) {
	plan := sealedPlan(t, ModelSetRuntimeOllama)
	observation := sizedObservation(ObservationSeparateCalls, 2, "resident", "resident")
	report := mustAnalyze(t, plan, observation)
	if report.Disposition != DispositionSeparateCalls || report.CoResident == nil || *report.CoResident {
		t.Fatalf("disposition=%s co=%v", report.Disposition, report.CoResident)
	}
	rejectNumbers(t, report, 111111, 222222, 333333)
}

func TestLlamaPropsZeroIsNotAMeasurement(t *testing.T) {
	plan := sealedPlan(t, ModelSetRuntimeLlamaServer)
	zero := int64(0)
	observation := ModelSetObservation{
		Schema: ModelSetObservationSchema, Kind: ObservationLlamaProps,
		Rows: []ResidentRow{{Name: "model", SizeBytes: &zero}},
	}
	report := mustAnalyze(t, plan, observation)
	if report.Disposition != DispositionBytesUnmeasured || *report.CoResident {
		t.Fatalf("disposition=%s co=%v", report.Disposition, *report.CoResident)
	}
	positive := int64(111111)
	observation.Rows[0].SizeBytes = &positive
	bad := mustAnalyze(t, plan, observation)
	if bad.Disposition != DispositionUnmeasured {
		t.Fatalf("positive props size disposition = %s", bad.Disposition)
	}
}

func TestRouterNoteDoesNotProveTheHandler(t *testing.T) {
	plan := sealedPlan(t, ModelSetRuntimeLlamaServer)
	observation := sizedObservation(ObservationLlamaRouter, 0, "resident", "resident")
	observation.HandlerProof = "llama.cpp draft, not a pinned handler"
	report := mustAnalyze(t, plan, observation)
	if report.Disposition != DispositionCandidateUnproven || *report.CoResident {
		t.Fatalf("disposition=%s co=%v", report.Disposition, *report.CoResident)
	}
	if !containsAll(report.Gaps, "a handler note was recorded and does not establish the read") {
		t.Fatalf("gaps = %v", report.Gaps)
	}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("llama.cpp")) {
		t.Fatalf("report copied the handler note: %s", raw)
	}
}

func TestOpenAIEmptyListIsUnavailable(t *testing.T) {
	plan := sealedPlan(t, ModelSetRuntimeOpenAI)
	observation := ModelSetObservation{Schema: ModelSetObservationSchema, Kind: ObservationOpenAIUnavailable}
	report := mustAnalyze(t, plan, observation)
	if report.Disposition != DispositionUnavailable || *report.CoResident {
		t.Fatalf("disposition=%s co=%v", report.Disposition, *report.CoResident)
	}
	observation.Rows = []ResidentRow{{Name: "model"}}
	bad := mustAnalyze(t, plan, observation)
	if bad.Disposition != DispositionUnmeasured {
		t.Fatalf("rows on an unavailable list disposition = %s", bad.Disposition)
	}
}

func TestIsolatedAllocationsAreNotASum(t *testing.T) {
	plan := sealedPlan(t, ModelSetRuntimeOllama)
	observation := sizedObservation(ObservationIsolatedAllocations, 0, "resident", "resident")
	report := mustAnalyze(t, plan, observation)
	if report.Disposition != DispositionAdditive || *report.CoResident {
		t.Fatalf("disposition=%s co=%v", report.Disposition, *report.CoResident)
	}
	rejectNumbers(t, report, 111111, 222222, 333333)
}

func TestSleepingRowIsSpill(t *testing.T) {
	plan := sealedPlan(t, ModelSetRuntimeOllama)
	observation := sizedObservation(ObservationOllamaPS, 1, "sleeping", "offloaded")
	report := mustAnalyze(t, plan, observation)
	if report.Disposition != DispositionSpill || *report.CoResident {
		t.Fatalf("disposition=%s co=%v", report.Disposition, *report.CoResident)
	}
	if !containsAll(report.Gaps, "the process-list body is still not one simultaneous observation") {
		t.Fatalf("gaps = %v", report.Gaps)
	}
}

func TestMalformedSizesStayUnmeasured(t *testing.T) {
	plan := sealedPlan(t, ModelSetRuntimeOllama)
	negative := int64(-1)
	observation := sizedObservation(ObservationOllamaPS, 1, "resident", "resident")
	observation.Rows[0].SizeBytes = &negative
	if mustAnalyze(t, plan, observation).Disposition != DispositionUnmeasured {
		t.Fatal("negative size was measured")
	}
	total := int64(10)
	vram := int64(11)
	observation = sizedObservation(ObservationOllamaPS, 1, "resident", "resident")
	observation.Rows[0].SizeBytes = &total
	observation.Rows[0].VRAMBytes = &vram
	if mustAnalyze(t, plan, observation).Disposition != DispositionUnmeasured {
		t.Fatal("accelerator size above the row total was measured")
	}
	observation = sizedObservation(ObservationOllamaPS, 1, "resident", "resident")
	observation.Rows[1].Name = observation.Rows[0].Name
	if mustAnalyze(t, plan, observation).Disposition != DispositionUnmeasured {
		t.Fatal("a repeated row name was measured")
	}
}

func TestRuntimeMismatchIsNotReinterpreted(t *testing.T) {
	plan := sealedPlan(t, ModelSetRuntimeLlamaServer)
	observation := sizedObservation(ObservationOllamaPS, 1, "resident", "resident")
	report := mustAnalyze(t, plan, observation)
	if report.Disposition != DispositionRuntimeMismatch || *report.CoResident {
		t.Fatalf("disposition=%s co=%v", report.Disposition, *report.CoResident)
	}
	rejectNumbers(t, report, 111111, 222222, 333333)
}

func TestStoredReportCannotClaimCoResidency(t *testing.T) {
	plan := sealedPlan(t, ModelSetRuntimeOllama)
	bundle, err := NewModelSetBundle(plan, sizedObservation(ObservationOllamaPS, 1, "resident", "resident"))
	if err != nil {
		t.Fatal(err)
	}
	yes := true
	bundle.Report.CoResident = &yes
	bundle.Report.Claim = "co_resident"
	if _, err := bundle.Validate(); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("tampered report err = %v", err)
	}
}

func TestModelSetPlanRejectsASingleMemberAndARepeatedMember(t *testing.T) {
	member := planMember("a", "coding")
	if _, err := NewModelSetPlan(ModelSetRuntimeOllama, deviceKey(), "", []ModelSetMember{member}); err == nil {
		t.Fatal("one member sealed a set")
	}
	if _, err := NewModelSetPlan(ModelSetRuntimeOllama, deviceKey(), "", []ModelSetMember{member, member}); err == nil {
		t.Fatal("a repeated member sealed a set")
	}
}

func TestAbsentCapacityPolicyOmitsItsField(t *testing.T) {
	plan := sealedPlan(t, ModelSetRuntimeOllama)
	raw, err := plan.JSON()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("capacity_policy_sha256")) {
		t.Fatalf("empty policy was written: %s", raw)
	}
	withPolicy, err := NewModelSetPlan(ModelSetRuntimeOllama, deviceKey(), digestHex("policy"), []ModelSetMember{
		planMember("a", "coding"), planMember("b", "embed"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if withPolicy.PlanSHA256 == plan.PlanSHA256 {
		t.Fatal("capacity policy did not change the plan digest")
	}
}

func TestModelSetBundleRoundTripRefusesAForgedPass(t *testing.T) {
	plan := sealedPlan(t, ModelSetRuntimeOllama)
	bundle, err := NewModelSetBundle(plan, sizedObservation(ObservationOllamaPS, 1, "resident", "resident"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := bundle.JSON()
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := decodeModelSetBundle(raw)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Report.Claim != ModelSetClaimUnresolved || *loaded.Report.CoResident {
		t.Fatalf("reopened claim=%s co=%v", loaded.Report.Claim, loaded.Report.CoResident)
	}
	forged := bytes.Replace(raw, []byte(`"co_resident": false`), []byte(`"co_resident": true `), 1)
	if _, err := decodeModelSetBundle(forged); err == nil {
		t.Fatal("a forged co-resident flag reopened")
	}
}

func sealedPlan(t *testing.T, runtime string) ModelSetPlan {
	t.Helper()
	plan, err := NewModelSetPlan(runtime, deviceKey(), "", []ModelSetMember{
		planMember("a", "coding"), planMember("b", "embed"),
	})
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func mustAnalyze(t *testing.T, plan ModelSetPlan, observation ModelSetObservation) ModelSetReport {
	t.Helper()
	report, err := AnalyzeModelSet(plan, observation)
	if err != nil {
		t.Fatal(err)
	}
	return report
}

func sizedObservation(kind string, bodies int, left, right string) ModelSetObservation {
	first := int64(111111)
	second := int64(222222)
	return ModelSetObservation{
		Schema: ModelSetObservationSchema, Kind: kind, Bodies: bodies,
		Rows: []ResidentRow{
			{Name: "left", SizeBytes: &first, State: left},
			{Name: "right", SizeBytes: &second, State: right},
		},
	}
}

func planMember(name, role string) ModelSetMember {
	return ModelSetMember{
		ArtifactDigest: digestHex(name), Role: role, RequestedContext: 8192, PlacementIntention: "accelerator",
	}
}

func deviceKey() string { return "fp2:" + strings.Repeat("ab", 32) }

func digestHex(label string) string {
	sum := sha256.Sum256([]byte(label))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func containsAll(values []string, want ...string) bool {
	have := make(map[string]bool, len(values))
	for _, value := range values {
		have[value] = true
	}
	for _, value := range want {
		if !have[value] {
			return false
		}
	}
	return true
}

func rejectNumbers(t *testing.T, report ModelSetReport, banned ...int64) {
	t.Helper()
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	var payload any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	var found []float64
	walkNumbers(payload, &found)
	for _, ban := range banned {
		for _, got := range found {
			if got == float64(ban) {
				t.Fatalf("report contains %d: %s", ban, raw)
			}
		}
	}
}

func walkNumbers(value any, found *[]float64) {
	switch typed := value.(type) {
	case map[string]any:
		for _, child := range typed {
			walkNumbers(child, found)
		}
	case []any:
		for _, child := range typed {
			walkNumbers(child, found)
		}
	case float64:
		*found = append(*found, typed)
	}
}
