package desktop

import (
	"context"
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/analysis"
	"github.com/blisspixel/fitr/internal/record"
	"github.com/blisspixel/fitr/internal/role"
)

func TestBenchmarkConfirmRunsOnlyAnAllowlistedLocalArgv(t *testing.T) {
	status := provenLocalStatus(t)
	plan := PlanBenchmark(status, nil)
	if !plan.Eligible || plan.Started || len(plan.Argv) != 11 {
		t.Fatalf("%+v", plan)
	}
	var called []string
	ran, code := ExecuteBenchmark(context.Background(), plan, false, func(_ context.Context, argv []string) int {
		called = append([]string(nil), argv...)
		return 9
	})
	if ran || code != 0 || called != nil {
		t.Fatalf("unconfirmed run started: ran %v code %d argv %v", ran, code, called)
	}
	ran, code = ExecuteBenchmark(context.Background(), plan, true, func(_ context.Context, argv []string) int {
		called = append([]string(nil), argv...)
		return 0
	})
	if !ran || code != 0 || strings.Join(called, "\x00") != strings.Join(plan.Argv, "\x00") {
		t.Fatalf("ran %v code %d called %v plan %v", ran, code, called, plan.Argv)
	}
}

func TestBenchmarkRefusesAmbientRoutesOpenAIProtocolAndExtraFlags(t *testing.T) {
	status := provenLocalStatus(t)
	if plan := PlanBenchmark(status, []string{"OPENAI_API_KEY=secret"}); plan.Eligible {
		t.Fatal("paid ambient was eligible")
	}
	if plan := PlanBenchmark(status, []string{"FITR_OPENAI_URL=http://127.0.0.1:9"}); plan.Eligible {
		t.Fatal("ambient endpoint was eligible")
	}
	for _, entry := range []string{
		"OLLAMA_BASE_URL=http://gpu.example.invalid:11434",
		"LLAMA_SERVER_URL=https://10.0.0.5:8080",
		"OLLAMA_BASE_URL=not a url",
	} {
		if plan := PlanBenchmark(status, []string{entry}); plan.Eligible {
			t.Fatalf("%s was eligible", entry)
		}
	}
	for _, entry := range []string{"OLLAMA_BASE_URL=http://127.0.0.1:11434", "LLAMA_SERVER_URL=http://localhost:8080/", "OLLAMA_BASE_URL=http://[::1]:11434"} {
		if plan := PlanBenchmark(status, []string{entry}); !plan.Eligible {
			t.Fatalf("%s was refused: %s", entry, plan.Reason)
		}
	}
	remote := provenLocalStatus(t)
	remote.Next.Argv[6] = "openai"
	remote.Model = "remote-model"
	remote.Next.Argv[2] = "remote-model"
	if plan := PlanBenchmark(remote, nil); plan.Eligible {
		t.Fatal("openai backend was eligible")
	}
	pulled := provenLocalStatus(t)
	pulled.Next.Argv = append(append([]string(nil), pulled.Next.Argv...), "--pull")
	pulled.Next.Effect = EffectExplicitLocal
	pulled.Next.LocalProven = true
	runner := func(_ context.Context, _ []string) int { t.Fatal("runner called"); return 0 }
	if plan := PlanBenchmark(pulled, nil); plan.Eligible {
		t.Fatal("pull flag stayed eligible")
	}
	forged := PlanBenchmark(status, nil)
	forged.Argv = append(forged.Argv, "--endpoint", "https://example.invalid")
	if ran, _ := ExecuteBenchmark(context.Background(), forged, true, runner); ran {
		t.Fatal("forged endpoint ran")
	}
	alias := provenLocalStatus(t)
	alias.Model = "owner/model"
	alias.Next.Argv[2] = "owner/model"
	if ran, _ := ExecuteBenchmark(context.Background(), PlanBenchmark(alias, nil), true, runner); ran {
		t.Fatal("repository alias ran")
	}
}

func TestOpenAIProtocolDoesNotBecomeALocalRun(t *testing.T) {
	report := localReport()
	result := localRecord()
	result.Manifest.Model.Backend = "openai"
	result.Manifest.Provenance.BackendProtocol = record.BackendProtocolOpenAICompatible
	status := Project(Evidence{
		RoleName: "coding", Report: &report, Record: result,
		Review:    &role.ReviewReport{Candidates: []role.Candidate{{State: "eligible"}}},
		Selection: qualifiedSelection(),
	})
	if status.Next.Effect == EffectExplicitLocal || status.Next.LocalProven {
		t.Fatalf("openai protocol was proven local: %+v", status.Next)
	}
	if plan := PlanBenchmark(status, nil); plan.Eligible {
		t.Fatal(plan.Reason)
	}
}

func provenLocalStatus(t *testing.T) Status {
	t.Helper()
	report := localReport()
	status := Project(Evidence{
		RoleName: "coding", Report: &report, Record: localRecord(),
		Review:    &role.ReviewReport{Candidates: []role.Candidate{{State: "eligible"}}},
		Selection: qualifiedSelection(),
	})
	if status.Next.Effect != EffectExplicitLocal || !status.Next.LocalProven || status.Model != "qwen3:8b" {
		t.Fatalf("fixture was not a proven local run: %+v", status.Next)
	}
	return status
}

func localReport() analysis.Report {
	return analysis.Report{NextActions: []analysis.Action{{
		Argv:   []string{"fitr", "run", analysis.CurrentModelPlaceholder, "--ctx", "4096", "--backend", "ollama", "--profile", "default", "-k", "3"},
		Reason: "measure the standard battery",
	}}}
}

func localRecord() *record.Record {
	return &record.Record{Manifest: &record.RunManifest{
		Model:      record.ModelIdentity{Resolved: "qwen3:8b", Backend: "ollama"},
		Provenance: &record.RunProvenance{BackendProtocol: record.BackendProtocolOllama},
		Profile:    "default", NumCtx: 4096,
	}}
}

func qualifiedSelection() *role.SelectionStatus {
	return &role.SelectionStatus{State: "qualified", EvaluatedAt: "2026-10-09T00:00:00Z", Selection: &role.SelectionReceipt{
		Selected: role.ConfirmationPoint{Model: record.ModelIdentity{Resolved: "qwen3:8b"}},
	}}
}
