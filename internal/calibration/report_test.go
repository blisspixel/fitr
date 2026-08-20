package calibration

import (
	"encoding/json"
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

func TestNewPairOrdersHigherPrecisionAndOmitsSensitiveData(t *testing.T) {
	r := NewPair("0.3.0", 2, "night", Device{
		ID: PseudonymousDeviceID("secret-host|gpu"),
		Config: map[string]string{
			"OLLAMA_MODELS":        `C:\Users\secret-host\.ollama`,
			"OLLAMA_KV_CACHE_TYPE": "q8_0",
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
	for _, forbidden := range []string{"secret-host", "raw", "prompt", "response", "result_path"} {
		if strings.Contains(string(b), forbidden) {
			t.Fatalf("shareable report contains %q: %s", forbidden, b)
		}
	}
	if r.Device.Config["OLLAMA_KV_CACHE_TYPE"] != "q8_0" || len(r.Device.Config) != 1 {
		t.Fatalf("shareable config was not allowlisted: %+v", r.Device.Config)
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
