package calibration

import (
	"crypto/ed25519"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/eval"
)

func pair(deviceID, seed string, flips int) PairReport {
	return NewPair("0.3.0", 2, seed, Device{ID: deviceID, GPU: "test-gpu"},
		Run{Model: "m-q8", Quant: "Q8_0", Family: "fam", ParameterSize: "8B", ResultSchemaVersion: 4},
		Run{Model: "m-q4", Quant: "Q4_K_M", Family: "fam", ParameterSize: "8B", ResultSchemaVersion: 4},
		[]eval.ItemStat{{TaskID: "json", Family: "json", Need: "structured_output", Shared: 5, Flips: flips, APass: 5, BPass: 5 - flips}})
}

func decisionPair(deviceID, seed, family string) PairReport {
	return NewPair("0.4.0", 2, seed, Device{ID: deviceID, GPU: "test-gpu"},
		Run{Model: family + "-q8", Quant: "Q8_0", Family: family, ParameterSize: "8B", ResultSchemaVersion: 4},
		Run{Model: family + "-q4", Quant: "Q4_K_M", Family: family, ParameterSize: "8B", ResultSchemaVersion: 4},
		[]eval.ItemStat{
			{TaskID: "contrast", Family: "json", Need: "structured_output", Shared: 10, Flips: 1, APass: 10, BPass: 9},
			{TaskID: "stable", Family: "reasoning", Need: "instruction_precision", Shared: 10, APass: 10, BPass: 10},
		})
}

func TestNewPairOrdersRankedDtypeAndOmitsSensitiveData(t *testing.T) {
	if got := PseudonymousDeviceID("secret-host|gpu"); got != "ef2f98f51b8adf55" {
		t.Fatalf("device pseudonym changed across versions: %q", got)
	}
	r := NewPair("0.3.0", 2, "secret-host-night", Device{
		ID: PseudonymousDeviceID("secret-host|gpu"),
		Config: map[string]string{
			"OLLAMA_MODELS":        `C:\Users\secret-host\.ollama`,
			"OLLAMA_KV_CACHE_TYPE": "q8_0",
			"LLAMA_ARG_FIT":        `C:\Users\secret-host\bin\llama-fit-params.exe`,
		},
	},
		Run{Model: "low", Quant: "Q4_K_M"}, Run{Model: "high", Quant: "Q8_0"},
		[]eval.ItemStat{{TaskID: "json", Shared: 3, Flips: 1, APass: 2, BPass: 3}})
	if r.Reference.Model != "high" || r.Candidate.Model != "low" {
		t.Fatalf("pair was not ordered by precision: %+v", r)
	}
	if r.Items[0].ReferencePasses != 3 || r.Items[0].CandidatePasses != 2 {
		t.Fatalf("pass counts did not follow reordered runs: %+v", r.Items[0])
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"secret-host", "secret-host-night", "raw", "prompt", "response", "result_path"} {
		if strings.Contains(string(b), forbidden) {
			t.Fatalf("shareable report contains %q: %s", forbidden, b)
		}
	}
	if r.Device.Config["OLLAMA_KV_CACHE_TYPE"] != "q8_0" ||
		r.Device.Config["LLAMA_ARG_FIT"] != "configured" || len(r.Device.Config) != 2 {
		t.Fatalf("shareable config was not allowlisted: %+v", r.Device.Config)
	}
	if r.SeedSet != PseudonymousSeedSetID("secret-host-night") || len(r.SeedSet) != 16 {
		t.Fatalf("seedset was not pseudonymized: %q", r.SeedSet)
	}
}

func TestNewPairPseudonymizesLocalModelPaths(t *testing.T) {
	r := NewPair("0.4.0", 2, "seed", Device{ID: "1111111111111111"},
		Run{Model: `C:\Users\private\high.gguf`, Quant: "Q8_0"},
		Run{Model: `/home/private/low.gguf`, Quant: "Q4_K_M"},
		[]eval.ItemStat{{TaskID: "json", Shared: 1, APass: 1, BPass: 1}})
	if !strings.HasPrefix(r.Reference.Model, "local-") || !strings.HasPrefix(r.Candidate.Model, "local-") ||
		r.Reference.Model == r.Candidate.Model {
		t.Fatalf("local model paths were not independently pseudonymized: %+v", r)
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "private") || strings.Contains(string(b), "Users") || strings.Contains(string(b), "home") {
		t.Fatalf("local model path leaked: %s", b)
	}
}

func TestReadPairRejectsUnknownFieldsAndTrailingJSON(t *testing.T) {
	r := pair("1111111111111111", "seed", 1)
	path := filepath.Join(t.TempDir(), "pair.json")
	if err := WriteJSON(path, r); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	unknown := strings.Replace(string(b), "{", `{"unexpected":true,`, 1)
	unknownPath := filepath.Join(t.TempDir(), "unknown.json")
	if err := os.WriteFile(unknownPath, []byte(unknown), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPair(unknownPath); err == nil || !strings.Contains(err.Error(), "unexpected") {
		t.Fatalf("unknown field = %v", err)
	}
	trailingPath := filepath.Join(t.TempDir(), "trailing.json")
	if err := os.WriteFile(trailingPath, append(b, []byte("\n{}\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPair(trailingPath); err == nil || !strings.Contains(strings.ToLower(err.Error()), "after") {
		t.Fatalf("trailing JSON = %v", err)
	}
}

func TestReadPairNormalizesLegacySensitiveIdentifiers(t *testing.T) {
	r := pair("1111111111111111", "seed", 1)
	r.Device.ID = "ABCDEFABCDEFABCD"
	r.SeedSet = "secret-host-night"
	r.Reference.Model = `C:\Users\secret-host\high.gguf`
	r.Candidate.Model = `/home/secret-host/low.gguf`
	r.Device.Config = map[string]string{
		"LLAMA_ARG_FIT": `C:\Users\secret-host\llama-fit-params.exe`,
	}
	r.Items[0].TaskID = "json\x1b[31m\u202e\nspoof"
	path := filepath.Join(t.TempDir(), "legacy-pair.json")
	if err := WriteJSON(path, r); err != nil {
		t.Fatal(err)
	}
	got, err := ReadPair(path)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"secret-host", "Users", "home", "llama-fit-params"} {
		if strings.Contains(string(b), forbidden) {
			t.Fatalf("normalized legacy pair contains %q: %s", forbidden, b)
		}
	}
	if len(got.SeedSet) != 16 || got.Device.ID != "abcdefabcdefabcd" ||
		got.Device.Config["LLAMA_ARG_FIT"] != "configured" {
		t.Fatalf("legacy pair was not normalized: %+v", got)
	}
	if strings.ContainsAny(got.Items[0].TaskID, "\x1b\r\n\u202e") || got.Items[0].TaskID != "json [31m spoof" {
		t.Fatalf("legacy task label was not made terminal-safe: %q", got.Items[0].TaskID)
	}
}

func withLineage(t *testing.T, r PairReport) PairReport {
	t.Helper()
	ref, cand, base := testDigest(0x61), testDigest(0x62), testDigest(0x60)
	r.Reference.ArtifactDigest = ref
	r.Candidate.ArtifactDigest = cand
	receipt, err := LineageFromConversion(conversionFor(base, ref, cand), ref, cand)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.AttachLineage(receipt); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestAssessPairDecisionGradeRequiresLineageAndTrust(t *testing.T) {
	good := withLineage(t, decisionPair("1111111111111111", "s1", "fam-a"))
	key := signTestPair(t, &good)
	if AssessPair(good).DecisionGrade || !AssessPair(good).SameBaseLineageVerified {
		t.Fatalf("unsigned lineage pair became decision-grade: %+v", AssessPair(good))
	}
	a := AssessPairWithTrust(good, NewTrustPolicy(key))
	if !a.DecisionGrade || !a.SameBaseLineageVerified || !a.TrustedEvidence ||
		!a.HigherPrecisionReference || !a.CandidateDamaged || !a.ReferenceHealthy {
		t.Fatalf("trusted lineage pair was not decision-grade: %+v", a)
	}
}

func TestAggregateDecisionGradeReadinessNeedsLineage(t *testing.T) {
	a := withLineage(t, decisionPair("1111111111111111", "s1", "fam-a"))
	b := withLineage(t, decisionPair("2222222222222222", "s2", "fam-b"))
	keyA := signTestPair(t, &a)
	keyB := signTestPair(t, &b)
	s, err := AggregateWithTrust([]PairReport{a, b}, NewTrustPolicy(keyA, keyB))
	if err != nil {
		t.Fatal(err)
	}
	if !s.Readiness.ReadyForReview || s.Readiness.DecisionGradeReports != 2 ||
		s.Readiness.Devices != 2 || s.Readiness.ModelFamilies != 2 {
		t.Fatalf("verified lineage did not create readiness: %+v", s.Readiness)
	}
	items := map[string]SummaryItem{}
	for _, item := range s.Items {
		items[item.TaskID] = item
	}
	if got := items["stable"]; !got.ReviewCandidate || got.DecisionGradeStatus != "review_candidate" || got.DecisionGradeFlips != 0 {
		t.Fatalf("never-flipped item = %+v", got)
	}
	if got := items["contrast"]; got.ReviewCandidate || got.DecisionGradeStatus != "observed" || got.DecisionGradeFlips != 2 {
		t.Fatalf("discriminating item = %+v", got)
	}
}

func TestAssessPairDecisionGradeControls(t *testing.T) {
	good := decisionPair("1111111111111111", "s1", "fam-a")
	key := signTestPair(t, &good)
	if AssessPair(good).DecisionGrade {
		t.Fatal("self-asserted signer became decision-grade without an external trust root")
	}
	a := AssessPairWithTrust(good, NewTrustPolicy(key))
	if a.DecisionGrade || a.SameBaseLineageVerified || a.HigherPrecisionReference || !a.FixedInstances ||
		a.MinimumInstancesPerTask != 10 || a.MaximumInstancesPerTask != 10 ||
		!a.ReferenceHealthy || a.CandidateDamaged ||
		!strings.Contains(strings.Join(a.Reasons, "; "), "same-base model revision lineage is not verified") {
		t.Fatalf("trusted pair escaped the lineage gate: %+v", a)
	}

	tests := []struct {
		name   string
		mutate func(*PairReport)
		want   string
	}{
		{
			name: "too few instances",
			mutate: func(r *PairReport) {
				for i := range r.Items {
					r.Items[i].Shared = 5
					r.Items[i].ReferencePasses = 5
					if r.Items[i].CandidatePasses == 9 {
						r.Items[i].CandidatePasses = 4
					}
				}
			},
			want: "fewer than 10",
		},
		{
			name: "uneven instances",
			mutate: func(r *PairReport) {
				r.Items[1].Shared = 11
				r.Items[1].ReferencePasses = 11
				r.Items[1].CandidatePasses = 11
			},
			want: "fixed instance count",
		},
		{
			name: "unhealthy reference",
			mutate: func(r *PairReport) {
				r.Items[1].ReferencePasses = 9
			},
			want: "not healthy",
		},
		{
			name: "precision unverified",
			mutate: func(r *PairReport) {
				r.Direction = "input_order"
			},
			want: "not verified",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := good
			r.Items = append([]Item(nil), good.Items...)
			tc.mutate(&r)
			got := AssessPair(r)
			if got.DecisionGrade || !strings.Contains(strings.Join(got.Reasons, "; "), tc.want) {
				t.Fatalf("assessment = %+v, want reason containing %q", got, tc.want)
			}
		})
	}
}

func TestAggregateCountsDevicesAndRejectsDuplicates(t *testing.T) {
	a, b := pair("1111111111111111", "s1", 1), pair("2222222222222222", "s2", 0)
	s, err := Aggregate([]PairReport{a, b})
	if err != nil {
		t.Fatal(err)
	}
	if s.Reports != 2 || s.Devices != 2 || len(s.Items) != 1 {
		t.Fatalf("bad summary: %+v", s)
	}
	item := s.Items[0]
	if item.Flips != 1 || item.DiscriminatedDevices != 1 || item.Status != "observed" {
		t.Fatalf("bad item summary: %+v", item)
	}
	if _, err := Aggregate([]PairReport{a, a}); err == nil {
		t.Fatal("duplicate report was accepted")
	}
}

func TestAggregateCannotCreateReadinessWithoutSameBaseLineage(t *testing.T) {
	a := decisionPair("1111111111111111", "s1", "fam-a")
	b := decisionPair("2222222222222222", "s2", "fam-b")
	keyA := signTestPair(t, &a)
	keyB := signTestPair(t, &b)
	s, err := AggregateWithTrust([]PairReport{a, b}, NewTrustPolicy(keyA, keyB))
	if err != nil {
		t.Fatal(err)
	}
	if s.Readiness.ReadyForReview || s.Readiness.DecisionGradeReports != 0 ||
		s.Readiness.Devices != 0 || s.Readiness.ModelFamilies != 0 || len(s.Readiness.Missing) != 2 {
		t.Fatalf("unverified lineage created campaign readiness: %+v", s.Readiness)
	}
	items := map[string]SummaryItem{}
	for _, item := range s.Items {
		items[item.TaskID] = item
	}
	if got := items["contrast"]; got.DecisionGradeStatus != "insufficient_evidence" || got.ReviewCandidate || got.DecisionGradeFlips != 0 {
		t.Fatalf("contrast item = %+v", got)
	}
	if got := items["stable"]; got.DecisionGradeStatus != "insufficient_evidence" || got.ReviewCandidate ||
		got.DecisionGradeReports != 0 || got.DecisionGradeDevices != 0 || got.DecisionGradeShared != 0 {
		t.Fatalf("stable item = %+v", got)
	}
}

func signTestPair(t *testing.T, r *PairReport) ed25519.PublicKey {
	t.Helper()
	key, err := NewTrustKey()
	if err != nil {
		t.Fatal(err)
	}
	if err := SignPair(r, key); err != nil {
		t.Fatal(err)
	}
	return key.Public().(ed25519.PublicKey)
}

func TestAggregateExcludesExploratoryPairsFromReadiness(t *testing.T) {
	a := pair("1111111111111111", "s1", 1)
	b := pair("2222222222222222", "s2", 1)
	b.Reference.Model, b.Candidate.Model = "other-q8", "other-q4"
	b.Reference.Family, b.Candidate.Family = "other", "other"
	s, err := Aggregate([]PairReport{a, b})
	if err != nil {
		t.Fatal(err)
	}
	if s.Readiness.ReadyForReview || s.Readiness.DecisionGradeReports != 0 ||
		s.Readiness.Devices != 0 || s.Readiness.ModelFamilies != 0 || len(s.Readiness.Missing) != 2 {
		t.Fatalf("exploratory evidence counted toward readiness: %+v", s.Readiness)
	}
	if s.Items[0].DecisionGradeStatus != "insufficient_evidence" || s.Items[0].ReviewCandidate {
		t.Fatalf("exploratory item was promoted: %+v", s.Items[0])
	}
}

func TestUnsignedAndForgedReportsCannotCreateReadiness(t *testing.T) {
	unsigned := decisionPair("1111111111111111", "s1", "fam-a")
	if AssessPair(unsigned).DecisionGrade {
		t.Fatal("unsigned imported report became decision-grade")
	}
	forged := decisionPair("2222222222222222", "s2", "fam-b")
	signTestPair(t, &forged)
	forged.FitrVersion = "forged"
	if err := VerifyPairTrust(forged); err == nil {
		t.Fatal("tampered signed report retained a valid trust receipt")
	}
	s, err := Aggregate([]PairReport{unsigned, forged})
	if err != nil {
		t.Fatal(err)
	}
	if s.Readiness.DecisionGradeReports != 0 || s.Readiness.ReadyForReview {
		t.Fatalf("forged readiness = %+v", s.Readiness)
	}
}

func TestAggregateRejectsSpecDrift(t *testing.T) {
	a, b := pair("1111111111111111", "s1", 1), pair("2222222222222222", "s2", 1)
	b.SpecVersion++
	if _, err := Aggregate([]PairReport{a, b}); err == nil {
		t.Fatal("mixed task specifications were accepted")
	}
}

func TestAggregateRejectsTaskSetDrift(t *testing.T) {
	a, b := pair("1111111111111111", "s1", 1), pair("2222222222222222", "s2", 1)
	b.Items = append(b.Items, Item{TaskID: "extra", Shared: 5})
	b.Shared += 5
	b.NeverObserved++
	if _, err := Aggregate([]PairReport{a, b}); err == nil {
		t.Fatal("different task sets were accepted")
	}
}

func TestAggregateRejectsInvalidCounts(t *testing.T) {
	r := pair("1111111111111111", "s1", 1)
	r.Items[0].Flips = r.Items[0].Shared + 1
	if _, err := Aggregate([]PairReport{r}); err == nil {
		t.Fatal("impossible item counts were accepted")
	}

	r = pair("1111111111111111", "s1", 1)
	r.Items[0].CandidatePasses = r.Items[0].ReferencePasses
	if _, err := Aggregate([]PairReport{r}); err == nil || !strings.Contains(err.Error(), "paired flips") {
		t.Fatalf("inconsistent paired outcomes were accepted: %v", err)
	}
}

func TestAggregateRejectsTamperedTotalsAndTaskMetadata(t *testing.T) {
	a := pair("1111111111111111", "s1", 1)
	a.Shared++
	if _, err := Aggregate([]PairReport{a}); err == nil {
		t.Fatal("pair totals that disagree with items were accepted")
	}

	a = pair("1111111111111111", "s1", 1)
	b := pair("2222222222222222", "s2", 1)
	b.Items[0].Need = "different_need"
	if _, err := Aggregate([]PairReport{a, b}); err == nil {
		t.Fatal("task metadata drift was accepted")
	}
}

func TestAggregateRejectsRawDeviceIdentifier(t *testing.T) {
	r := pair("hostname", "s1", 1)
	if _, err := Aggregate([]PairReport{r}); err == nil {
		t.Fatal("raw device identifier was accepted")
	}
}
