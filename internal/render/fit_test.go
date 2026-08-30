package render

import (
	"strings"
	"testing"
)

func TestWriteContextFitPlain(t *testing.T) {
	var out strings.Builder
	WriteContextFit(&out, ContextFit{
		HaveGB:     20,
		HaveSource: "--vram-gb",
		Note:       "weights + KV only; other runtime allocation not observed until --load",
		Points: []ContextFitPoint{
			{Ctx: 4096, Tier: "compatible", WeightsGB: 18, KVGB: 0.5, NeedGB: 18.5, MarginGB: 1.5, Suggested: true, DecodeTPS: 24.8},
			{Ctx: 32768, Tier: "incompatible", WeightsGB: 18, KVGB: 4, NeedGB: 22, MarginGB: -2, Requested: true},
		},
	}, "plain")
	got := out.String()
	for _, want := range []string{
		"4096", "32768", "compatible", "incompatible", "n/a", "24.8",
		"suggested", "other n/a", "budget 20.0 GB (--vram-gb)", "ROOM is derived",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("plain fit table missing %q:\n%s", want, got)
		}
	}
	if strings.ContainsRune(got, '\x1b') {
		t.Fatalf("plain leaked escapes:\n%s", got)
	}
}

func TestWriteContextFitLabelsAddressableCapacityWithoutHeadroomClaim(t *testing.T) {
	var out strings.Builder
	WriteContextFit(&out, ContextFit{
		HaveGB: 121.7, HaveSource: "linux /proc/meminfo MemTotal",
		CapacityOnly: true,
		Points: []ContextFitPoint{{
			Ctx: 8192, Tier: "skip", WeightsGB: 104, KVGB: 4,
			NeedGB: 108, MarginGB: 13.7,
		}},
	}, "plain")
	got := out.String()
	for _, want := range []string{"DELTA", "addressable capacity 121.7 GB", "not usable room"} {
		if !strings.Contains(got, want) {
			t.Fatalf("addressable table missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "ROOM") || strings.Contains(got, "budget 121.7") {
		t.Fatalf("addressable capacity was presented as a usable budget:\n%s", got)
	}
}
