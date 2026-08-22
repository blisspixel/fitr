package render

import (
	"strings"
	"testing"
)

func TestWriteContextFitPlain(t *testing.T) {
	var out strings.Builder
	WriteContextFit(&out, ContextFit{
		HaveGB: 20,
		Note:   "weights + KV only; compute buffers not included until --load or --fit",
		Points: []ContextFitPoint{
			{Ctx: 4096, Tier: "compatible", WeightsGB: 18, KVGB: 0.5, NeedGB: 18.5, HeadroomGB: 1.5, Suggested: true, DecodeTPS: 24.8},
			{Ctx: 32768, Tier: "incompatible", WeightsGB: 18, KVGB: 4, NeedGB: 22, HeadroomGB: -2, Requested: true},
		},
	}, "plain")
	got := out.String()
	for _, want := range []string{"4096", "32768", "compatible", "incompatible", "n/a", "24.8", "suggested", "buffers n/a"} {
		if !strings.Contains(got, want) {
			t.Fatalf("plain fit table missing %q:\n%s", want, got)
		}
	}
	if strings.ContainsRune(got, '\x1b') {
		t.Fatalf("plain leaked escapes:\n%s", got)
	}
}
