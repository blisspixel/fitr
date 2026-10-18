package contextquality

import "testing"

func FuzzDecodeTokenPlan(f *testing.F) {
	plan, err := NewTokenPlan(draftTokenPlan(AccountingNative, nil))
	if err != nil {
		f.Fatal(err)
	}
	raw, err := plan.JSON()
	if err != nil {
		f.Fatal(err)
	}
	f.Add(raw)
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"schema":"fitr.context-token-account.v1","schema":"duplicate"}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		decoded, err := DecodeTokenPlan(data)
		if err != nil {
			return
		}
		if err := decoded.Validate(); err != nil {
			t.Fatalf("decoder accepted an invalid token plan: %v", err)
		}
	})
}
