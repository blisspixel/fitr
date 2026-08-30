package advise

import (
	"strings"
	"testing"
)

func TestParseFitLogTakesTheFittedProjection(t *testing.T) {
	log := `
llama_params_fit_impl: projected memory use with initial parameters [MiB]:
llama_params_fit_impl: projected to use 66721 MiB of device memory vs.
48161 MiB of free device memory
llama_params_fit_impl: projected to use 19721 MiB of device memory vs.
21429 MiB of free device memory
llama_params_fit: successfully fit params to free device memory
`
	used, cannot, ok := ParseFitLog(log)
	if !ok {
		t.Fatal("must parse a llama-fit-params log")
	}
	if cannot {
		t.Fatal("successful fit is not cannot-fulfill")
	}
	want := int64(19721) * 1024 * 1024
	if used != want {
		t.Fatalf("used = %d, want the LAST projection %d (not the 66721 initial guess)", used, want)
	}
}

func TestParseFitLogCannotFulfill(t *testing.T) {
	log := `
llama_params_fit_impl: projected to use 29885 MiB of device memory vs.
15094 MiB of free device memory
llama_params_fit_impl: cannot fulfill margin of 1024 MiB on all devices
`
	used, cannot, ok := ParseFitLog(log)
	if !ok || !cannot {
		t.Fatalf("ok=%v cannot=%v, want a parsed cannot-fulfill", ok, cannot)
	}
	if used != 29885*1024*1024 {
		t.Fatalf("used = %d", used)
	}
}

func TestParseFitLogIntermediateFailureThenSuccess(t *testing.T) {
	log := `
llama_params_fit_impl: projected to use 66721 MiB of device memory vs.
llama_params_fit_impl: cannot fulfill margin of 1024 MiB on all devices
llama_params_fit_impl: projected to use 19721 MiB of device memory vs.
llama_params_fit: successfully fit params to free device memory
`
	used, cannot, ok := ParseFitLog(log)
	if !ok || cannot || used != 19721*1024*1024 {
		t.Fatalf("used=%d cannot=%v ok=%v, want final successful projection", used, cannot, ok)
	}
}

func TestParseFitLogEmptyIsNotAGuess(t *testing.T) {
	if _, _, ok := ParseFitLog("hello"); ok {
		t.Fatal("unrelated text must not invent a GB number")
	}
}

func TestAllocatorProjectionIsDescriptiveNotAVerdict(t *testing.T) {
	r := Evaluate(Input{
		Model: "m.gguf", HaveGB: 24, HaveSrc: "nvidia-smi",
		WeightsB: 10 * GiB, Arch: llama8B(), Ctx: 8192,
		FitB: 18 * GiB, FitSrc: "llama-fit-params",
	})
	if r.Tier != Skip {
		t.Fatalf("tier = %s (%s), want skip from unbound allocator projection", r.Tier, r.Why)
	}
	if r.NeedGB != 18.0 {
		t.Fatalf("need_gb = %v, want the dummy-allocation 18, not weights+KV", r.NeedGB)
	}
	if !strings.Contains(strings.Join(r.Gaps, " "), "not bound") {
		t.Fatalf("must disclose missing projection bindings: %v", r.Gaps)
	}
}

func TestAllocatorProjectionOverBudgetStillCannotProveARequestedPoint(t *testing.T) {
	r := Evaluate(Input{
		WeightsB: 6 * GiB, HaveGB: 8, HaveSrc: "--vram-gb",
		Arch: llama8B(), Ctx: 131072,
		FitB: 20 * GiB, FitSrc: "llama-fit-params",
	})
	if r.Tier != Skip {
		t.Fatalf("tier = %s, want skip from unbound allocator projection", r.Tier)
	}
	if r.Flag != "" || r.FlagValue != 0 || r.FitsGB != 0 {
		t.Fatalf("unbound projection emitted a point-specific remedy: %+v", r)
	}
}

func TestLoadFailureIsIncompatibleNotSkip(t *testing.T) {
	r := Evaluate(Input{
		HaveGB: 8, HaveSrc: "--vram-gb",
		LoadErr: "ollama 500: model requires more memory",
	})
	if r.Tier != Incompatible {
		t.Fatalf("tier = %s, want incompatible - the box refused the load", r.Tier)
	}
	if r.Remedy == "" {
		t.Fatal("a refused load must name a next step")
	}
	if r.ExitCode() != 3 {
		t.Fatal("Incompatible is a failed measurement")
	}
}

func TestResidentBeatsDummyAllocation(t *testing.T) {
	r := Evaluate(Input{
		HaveGB: 24, HaveSrc: "nvidia-smi",
		ResidentB: 19 * GiB, ResidentSrc: "ollama /api/ps",
		FitB: 22 * GiB, FitSrc: "llama-fit-params",
	})
	if r.NeedGB != 19.0 {
		t.Fatalf("need_gb = %v, want the live resident, not the dummy 22", r.NeedGB)
	}
	if !strings.Contains(r.Why, "measured") {
		t.Fatalf("live resident must win: %q", r.Why)
	}
}
