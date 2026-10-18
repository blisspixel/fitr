package main

import (
	"context"
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/device"
	"github.com/blisspixel/fitr/internal/llm"
	"github.com/blisspixel/fitr/internal/suggest"
)

func TestSharedMemoryDoesNotFitOrPull(t *testing.T) {
	cases := []device.Fingerprint{
		{GPU: "AMD Radeon 780M", VRAMGb: 16, VRAMSource: device.AMDGTTMappingSource, RAMGb: 64},
		{GPU: "AMD Radeon 780M", VRAMGb: 4, VRAMSource: "drm sysfs", RAMGb: 32},
	}
	item := suggest.Candidate{Name: "qwen2.5-coder:1.5b", Use: "coding"}
	weights := int64(1 << 30)
	for _, fp := range cases {
		t.Run(fp.VRAMSource, func(t *testing.T) {
			plan := planSuggestion(fp, 0)
			groups := suggest.Partition([]suggest.Candidate{item}, []int64{weights}, plan.Budget, suggest.PreferMoE(fp.GPU, fp.GPUBackend, fp.VRAMSource))
			if plan.Kind != "unmeasured" || len(groups) != 1 || groups[0].Class != suggest.WeightUnmeasured {
				t.Fatalf("plan=%+v groups=%+v", plan, groups)
			}
			line := formatSuggestCandidate(item, observedWeight{bytes: weights}, plan.Budget, groups[0].Class, "")
			if strings.Contains(line, "ollama pull") || !strings.Contains(plan.budgetLine(), "resident fit is unmeasured") {
				t.Fatalf("line=%s budget=%s", line, plan.budgetLine())
			}
		})
	}
}

func TestOperatorCeilingProjectsWithoutBecomingResidentFit(t *testing.T) {
	fp := device.Fingerprint{GPU: "AMD Radeon 780M", VRAMGb: 16, VRAMSource: device.AMDGTTMappingSource, RAMGb: 64}
	plan := planSuggestion(fp, 8)
	if plan.Kind != "ceiling" || plan.Budget != 8 || !strings.Contains(plan.budgetLine(), "operator ceiling") || strings.Contains(plan.budgetLine(), "resident budget") && !strings.Contains(plan.budgetLine(), "not a measured resident budget") {
		t.Fatalf("ceiling plan: %+v %s", plan, plan.budgetLine())
	}
	item := suggest.Candidate{Name: "qwen2.5-coder:1.5b", Use: "coding"}
	line := formatSuggestCandidate(item, observedWeight{bytes: 1 << 30}, plan.Budget, suggest.WeightFits, "")
	if !strings.Contains(line, "catalog tag coding") || !strings.Contains(line, "Ollama library recipe") || !strings.Contains(line, "ollama pull "+item.Name) {
		t.Fatalf("ceiling recipe: %s", line)
	}
	other := formatSuggestCandidate(item, observedWeight{bytes: 1 << 30}, plan.Budget, suggest.WeightFits, "llama-server")
	if strings.Contains(other, "ollama pull") || !strings.Contains(other, "unmeasured on this runtime") {
		t.Fatalf("other runtime: %s", other)
	}
}

func TestDiscreteBudgetStillPartitionsAndIgnoresACeiling(t *testing.T) {
	fp := device.Fingerprint{GPU: "NVIDIA GeForce RTX 4090", VRAMGb: 24, VRAMSource: "nvidia-smi"}
	plan := planSuggestion(fp, 4)
	if plan.Kind != "accelerator" || plan.Budget != 24 || plan.Source != "nvidia-smi" {
		t.Fatalf("discrete plan: %+v", plan)
	}
	if !strings.Contains(plan.budgetLine(), "discrete accelerator") {
		t.Fatalf("ceiling was silent: %s", plan.budgetLine())
	}
	small := suggest.Candidate{Name: "qwen2.5-coder:1.5b", Use: "coding"}
	large := suggest.Candidate{Name: "mixtral:8x7b", Use: "agent", CatalogMoE: true}
	// 30 GiB exceeds the 24 GiB discrete budget. 20 GiB would still fit it.
	groups := suggest.Partition([]suggest.Candidate{large, small}, []int64{30 << 30, 1 << 30}, plan.Budget, false)
	if len(groups) < 2 || groups[0].Class != suggest.WeightFits || groups[0].Items[0].Name != small.Name || groups[1].Class != suggest.WeightOverBudget {
		t.Fatalf("groups: %+v", groups)
	}
}

func TestDiscoverErrorIsNotAnEmptyRuntime(t *testing.T) {
	item := suggest.Candidate{Name: "qwen2.5-coder:1.5b", Use: "coding"}
	line := formatSuggestCandidate(item, observedWeight{bytes: 1 << 30}, 8, suggest.WeightFits, reachabilityUnmeasured)
	if strings.Contains(line, "ollama pull") || !strings.Contains(line, "reachability was unmeasured") {
		t.Fatalf("discover error: %s", line)
	}
}

func TestCeilingFlagRejectsNonPositive(t *testing.T) {
	previous := machineErrors
	machineErrors = false
	t.Cleanup(func() { machineErrors = previous })
	for _, arg := range []string{"NaN", "-1", "+Inf", "-Inf"} {
		t.Run(arg, func(t *testing.T) {
			stdout, stderr, code := captureCommandOutput(t, func() int {
				return cmdSuggest(context.Background(), []string{"--ceiling-gb", arg})
			})
			if code != exitUsage || stdout != "" || !strings.Contains(stderr, "invalid --ceiling-gb") {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
			}
		})
	}
}

func TestLibraryRuntimePrefersOllamaAndNone(t *testing.T) {
	if got := reachableLibraryRuntime(nil); got != "" {
		t.Fatalf("none = %q", got)
	}
	if got := reachableLibraryRuntime([]llm.Found{{Kind: "llama-server"}, {Kind: "ollama"}}); got != "ollama" {
		t.Fatalf("ollama present = %q", got)
	}
	if got := reachableLibraryRuntime([]llm.Found{{Kind: "llama-server"}}); got != "llama-server" {
		t.Fatalf("other = %q", got)
	}
}
