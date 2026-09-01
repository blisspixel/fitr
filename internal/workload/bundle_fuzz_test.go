package workload

import "testing"

func FuzzDecodeBundle(f *testing.F) {
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"schema":"fitr.workload.bundle.v1"}`))
	f.Add([]byte(`{"schema":"fitr.workload.bundle.v1","unknown":true}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		bundle, err := decodeBundle(data)
		if err != nil {
			return
		}
		if err := bundle.Validate(); err != nil {
			t.Fatalf("decoder accepted an invalid workload bundle: %v", err)
		}
	})
}
