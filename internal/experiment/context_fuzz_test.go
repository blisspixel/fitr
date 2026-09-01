package experiment

import "testing"

func FuzzDecodeContextBundle(f *testing.F) {
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"schema":"fitr.experiment.context.bundle.v1"}`))
	f.Add([]byte(`{"schema":"fitr.experiment.context.bundle.v1","unknown":true}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		bundle, err := decodeContextBundle(data)
		if err != nil {
			return
		}
		if _, err := bundle.Validate(); err != nil {
			t.Fatalf("decoder accepted an invalid context bundle: %v", err)
		}
	})
}
