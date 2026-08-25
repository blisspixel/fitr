package advise

import (
	"bytes"
	"strings"
	"testing"
)

// A fit verdict is computed against the whole card, which is the right
// question only on a machine doing nothing else. Nobody uses a machine only
// for inference, so a box reporting 24 GB total with 0.7 GB free will not load
// a 17 GB model whatever the arithmetic says. The verdict stays, because the
// card is still capable; the caveat says the card is currently busy.
func TestAdviseSaysWhenTheCardIsNotActuallyFree(t *testing.T) {
	in := Input{
		Model: "big:30b", WeightsB: 17 * GiB,
		HaveGB: 24, HaveSrc: "nvidia-smi", FreeGB: 0.7,
		Ctx: 8192, Arch: llama8B(), Backend: "ollama",
	}
	var buf bytes.Buffer
	Write(&buf, Evaluate(in))
	out := buf.String()
	if !strings.Contains(out, "free now") {
		t.Fatalf("no caveat when only 0.7 GB of 24 GB is free:\n%s", out)
	}
	if !strings.Contains(out, "0.7") || !strings.Contains(out, "17.0") {
		t.Fatalf("caveat does not compare free memory to the weights:\n%s", out)
	}
}

// The caveat must stay quiet when it has nothing to add, or it becomes noise
// that readers learn to skip.
func TestFreeMemoryCaveatStaysQuietWhenThereIsRoom(t *testing.T) {
	base := Input{
		Model: "small:7b", WeightsB: 4 * GiB,
		HaveGB: 24, HaveSrc: "nvidia-smi",
		Ctx: 8192, Arch: llama8B(), Backend: "ollama",
	}
	for _, tc := range []struct {
		name string
		free float64
	}{
		{"plenty free", 20},
		{"exactly enough", 4},
		{"not measured", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := base
			in.FreeGB = tc.free
			var buf bytes.Buffer
			Write(&buf, Evaluate(in))
			if strings.Contains(buf.String(), "free now") {
				t.Fatalf("caveat fired with %v GB free against 4 GB of weights", tc.free)
			}
		})
	}
}

// Free memory moves minute to minute. It is reported, never sealed: a volatile
// number in the comparability key would put every run in its own block.
func TestFreeMemoryIsReportedNotPartOfTheVerdict(t *testing.T) {
	in := Input{
		Model: "m", WeightsB: 4 * GiB, HaveGB: 24, HaveSrc: "nvidia-smi",
		Ctx: 8192, Arch: llama8B(), Backend: "ollama",
	}
	busy, idle := in, in
	busy.FreeGB, idle.FreeGB = 0.5, 23
	if Evaluate(busy).Tier != Evaluate(idle).Tier {
		t.Fatal("free memory changed the fit verdict; it must only annotate it")
	}
}
