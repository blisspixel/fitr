package experiment

import "testing"

func FuzzDecodeModelSetBundle(f *testing.F) {
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"schema":"fitr.experiment.modelset.bundle.v1"}`))
	f.Add([]byte(`{"schema":"fitr.experiment.modelset.bundle.v1","unknown":true}`))
	plan, err := NewModelSetPlan(ModelSetRuntimeOllama, deviceKey(), "", []ModelSetMember{
		planMember("a", "coding"), planMember("b", "embed"),
	})
	if err != nil {
		f.Fatal(err)
	}
	bundle, err := NewModelSetBundle(plan, sizedObservation(ObservationOllamaPS, 1, "resident", "resident"))
	if err != nil {
		f.Fatal(err)
	}
	raw, err := bundle.JSON()
	if err != nil {
		f.Fatal(err)
	}
	f.Add(raw)
	f.Fuzz(func(t *testing.T, data []byte) {
		loaded, err := decodeModelSetBundle(data)
		if err != nil {
			return
		}
		if _, err := loaded.Validate(); err != nil {
			t.Fatalf("decoder accepted an invalid model-set bundle: %v", err)
		}
		if loaded.Report.CoResident == nil || *loaded.Report.CoResident || loaded.Report.Claim != ModelSetClaimUnresolved {
			t.Fatalf("decoder accepted a co-residency claim: %+v", loaded.Report)
		}
	})
}
