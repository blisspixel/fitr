package contextquality

import "testing"

func FuzzDecodeTokenConditionPlan(f *testing.F) {
	plan, err := NewMatchedConditionPlan(TokenConditionPlan{
		TargetUsableInputTokens: 64000, Semantics: SemanticsUsableInput,
		Identity: tokenIdentity(AccountingNative),
	}, IndirectRetrieval, Beginning, 1)
	if err != nil {
		f.Fatal(err)
	}
	raw, err := plan.JSON()
	if err != nil {
		f.Fatal(err)
	}
	f.Add(raw)
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"schema":"fitr.context-token-account.v2","schema":"duplicate"}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		decoded, err := DecodeTokenConditionPlan(data)
		if err != nil {
			return
		}
		if err := decoded.Validate(); err != nil {
			t.Fatalf("decoder accepted an invalid condition plan: %v", err)
		}
	})
}
