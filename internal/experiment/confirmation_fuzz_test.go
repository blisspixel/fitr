package experiment

import "testing"

func FuzzDecodeConfirmationBundle(f *testing.F) {
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"schema":"fitr.experiment.confirmation.bundle.v1"}`))
	f.Add([]byte(`{"schema":"fitr.experiment.confirmation.bundle.v1","unknown":true}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		bundle, err := decodeConfirmationBundle(data)
		if err != nil {
			return
		}
		if _, err := bundle.Validate(); err != nil {
			t.Fatalf("decoder accepted an invalid confirmation bundle: %v", err)
		}
	})
}
