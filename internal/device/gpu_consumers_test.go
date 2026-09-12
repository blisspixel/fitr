package device

import "testing"

// The vendor tool substitutes a bracketed marker where it has no name to give.
// Printed as a name it reads as a program to go and close.
func TestBracketedVendorMarkersAreNotProcessNames(t *testing.T) {
	for _, marker := range []string{"[Insufficient Permissions]", "[N/A]", "[Not Supported]"} {
		if got := processLeaf(marker); got != "" {
			t.Fatalf("processLeaf(%q) = %q, want it treated as unnamed", marker, got)
		}
	}
	if got := processLeaf(`C:\Windows\explorer.exe`); got != "explorer.exe" {
		t.Fatalf("processLeaf kept a directory: %q", got)
	}
	if got := processLeaf("/usr/bin/ollama"); got != "ollama" {
		t.Fatalf("processLeaf kept a directory: %q", got)
	}
}

// The serving runtime is the subject of the measurement. Advising that it be
// closed would break the thing being measured.
func TestServingRuntimeIsRecognizedAcrossPlatforms(t *testing.T) {
	for _, name := range []string{"ollama.exe", "ollama_llama_server.exe", "llama-server", "OLLAMA"} {
		if !(GPUConsumer{Name: name}).ServingRuntime() {
			t.Fatalf("%q was not recognized as the serving runtime", name)
		}
	}
	for _, name := range []string{"explorer.exe", "msedgewebview2.exe", ""} {
		if (GPUConsumer{Name: name}).ServingRuntime() {
			t.Fatalf("%q was mistaken for the serving runtime", name)
		}
	}
}

// Busy is a share of capacity, not an absolute figure: the same spare gigabyte
// means different things on a small card and a large one.
func TestBusyIsRelativeToCapacityAndUnobservedIsNotIdle(t *testing.T) {
	if (GPUContention{}).Busy() {
		t.Fatal("an unobserved accelerator must not report as busy")
	}
	small := GPUContention{Observed: true, TotalMiB: 8192, UsedMiB: 1024}
	if !small.Busy() {
		t.Fatalf("1 GiB of 8 GiB is a meaningful share: %+v", small)
	}
	large := GPUContention{Observed: true, TotalMiB: 98304, UsedMiB: 1024}
	if large.Busy() {
		t.Fatalf("1 GiB of 96 GiB is not contention: %+v", large)
	}
}

// Ranking only means something where the driver reported per-process bytes.
// Where it did not, the listed order is preserved rather than invented.
func TestTopRanksOnlyWhenPerProcessBytesExist(t *testing.T) {
	ranked := GPUContention{
		Observed: true, PerProcess: true,
		Consumers: []GPUConsumer{
			{Name: "small", UsedMiB: 100, UsedKnown: true},
			{Name: "large", UsedMiB: 9000, UsedKnown: true},
		},
	}
	if got := ranked.Top(2); got[0].Name != "large" {
		t.Fatalf("largest consumer was not first: %+v", got)
	}
	unranked := GPUContention{
		Observed:  true,
		Consumers: []GPUConsumer{{Name: "first"}, {Name: "second"}},
	}
	if got := unranked.Top(2); got[0].Name != "first" {
		t.Fatalf("order was invented without per-process bytes: %+v", got)
	}
	if got := unranked.Top(1); len(got) != 1 {
		t.Fatalf("Top did not cap the list: %+v", got)
	}
}

func TestUnavailableVendorFieldsParseAsZeroRatherThanError(t *testing.T) {
	for _, field := range []string{"[N/A]", "[Insufficient Permissions]", "", "not-a-number"} {
		if got := atoiOr(field); got != 0 {
			t.Fatalf("atoiOr(%q) = %d, want 0", field, got)
		}
	}
	if got := atoiOr(" 9688 "); got != 9688 {
		t.Fatalf("atoiOr lost a real value: %d", got)
	}
}
