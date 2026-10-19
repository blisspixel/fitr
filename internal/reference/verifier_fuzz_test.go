package reference

import "testing"

func FuzzDecodeVerifier(f *testing.F) {
	f.Add([]byte(`{"schema":"fitr.external-reference.verifier.v1","tasks":[]}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"schema":"fitr.external-reference.verifier.v1","schema":"duplicate","tasks":[]}`))
	f.Add([]byte(`{"schema":"fitr.external-reference.verifier.v1","tasks":[{"problem_statement":"hidden"}]}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		rows, err := DecodeVerifier(data)
		if err != nil {
			return
		}
		for _, row := range rows {
			if !knownVerdict(row.Verdict) {
				t.Fatalf("decoder accepted verdict %q", row.Verdict)
			}
		}
	})
}
