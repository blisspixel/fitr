package fitting

import (
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/autoruntime"
	"github.com/blisspixel/fitr/internal/ollama"
)

func TestExplicitEndpointIgnoresMalformedUnusedEnvironment(t *testing.T) {
	got, err := ResolveEndpoint(EndpointInput{
		Explicit: "http://127.0.0.1:11435", FitEnv: "not a URL", CLIEnv: "https://user:secret@host",
	})
	if err != nil || got.URL != "http://127.0.0.1:11435" {
		t.Fatal(got, err)
	}
}

func TestEndpointComparisonKeepsSchemeAndPath(t *testing.T) {
	for _, other := range []string{"https://localhost:11434", "http://localhost:11434/other", "http://localhost"} {
		if sameClient("http://127.0.0.1:11434", other) {
			t.Fatalf("different client URLs pooled: %s", other)
		}
	}
}

func TestResolveEndpointKeepsAnExplicitURLAndReportsConflicts(t *testing.T) {
	got, err := ResolveEndpoint(EndpointInput{Explicit: "http://127.0.0.1:11435"})
	if err != nil || got.URL != "http://127.0.0.1:11435" || got.Source != "explicit" || got.Locality != "loopback-unproven" {
		t.Fatal(got, err)
	}
	got, err = ResolveEndpoint(EndpointInput{
		Explicit: "http://10.1.2.3:11434",
		FitEnv:   "http://127.0.0.1:11434",
		CLIEnv:   "10.9.9.9:11434",
	})
	if err != nil || got.URL != "http://10.1.2.3:11434" || !strings.Contains(got.Note, "explicit endpoint is kept") {
		t.Fatal(got, err)
	}
	if _, err := ResolveEndpoint(EndpointInput{FitEnv: "http://127.0.0.1:11434", CLIEnv: "http://10.1.2.3:11434"}); err == nil || !strings.Contains(err.Error(), "OLLAMA_BASE_URL") || !strings.Contains(err.Error(), "OLLAMA_HOST") {
		t.Fatal(err)
	}
	if _, err := ResolveEndpoint(EndpointInput{CLIEnv: "http://10.1.2.3:11434"}); err == nil || !strings.Contains(err.Error(), "fitr does not use it") {
		t.Fatal(err)
	}
	got, err = ResolveEndpoint(EndpointInput{CLIEnv: "0.0.0.0:11434"})
	if err != nil || got.URL != ollama.DefaultURL || !strings.Contains(got.Note, "did not adopt OLLAMA_HOST") {
		t.Fatal(got, err)
	}
	got, err = ResolveEndpoint(EndpointInput{FitEnv: "http://127.0.0.1:11434", CLIEnv: "localhost:11434"})
	if err != nil || got.URL != "http://127.0.0.1:11434" {
		t.Fatal(got, err)
	}
	if _, err := ResolveEndpoint(EndpointInput{Explicit: "http://user:secret@127.0.0.1:11434"}); err == nil || !strings.Contains(err.Error(), "userinfo") {
		t.Fatal(err)
	}
}

func TestApplyToRuntimeRefusesToCopyACeilingIntoTheReserve(t *testing.T) {
	plan, err := Draft(ceilingRequest(t), testTasks(t))
	if err != nil {
		t.Fatal(err)
	}
	inspected := inspectedRuntime()
	out, err := ApplyToRuntime(plan, inspected)
	if err == nil {
		t.Fatal("ceiling became owned runtime settings")
	}
	if out.ReserveBytes == plan.CapacityBytes || out.NumCtx == plan.ContextTokens {
		t.Fatalf("ceiling leaked into the runtime spec: %+v", out)
	}
	if !strings.Contains(err.Error(), "approved reserve") {
		t.Fatal(err)
	}
}

func TestApplyToRuntimeUsesThePlanInsteadOfInspectionDefaults(t *testing.T) {
	req := ceilingRequest(t)
	req.OwnedRuntime = true
	req.CapacityKind = CapacityReserve
	reserve, err := ParseGiB(4, true)
	if err != nil {
		t.Fatal(err)
	}
	limit, err := ParseGiB(24, true)
	if err != nil {
		t.Fatal(err)
	}
	req.CapacityBytes, req.ResidentLimitBytes = reserve, limit
	req.Candidates = []string{"qwen3:30b", "devstral:24b"}
	req.Endpoint, req.EndpointSource, req.Locality = "owned-process", "owned-runtime", "owned-process"
	req.ContextTokens = 32768
	plan, err := Draft(req, testTasks(t))
	if err != nil || plan.Blocked {
		t.Fatal(err, plan.BlockReason)
	}
	out, err := ApplyToRuntime(plan, inspectedRuntime())
	if err != nil {
		t.Fatal(err)
	}
	if out.NumCtx != 32768 || out.ReserveBytes != reserve || out.ReserveBytes == 2<<30 || out.KVCacheType != "f16" {
		t.Fatalf("runtime = %+v", out)
	}
}

func inspectedRuntime() autoruntime.Spec {
	return autoruntime.Spec{
		Schema: autoruntime.SpecSchema, Executable: `C:\runtime\ollama.exe`,
		ExecutableSHA256: "sha256:" + strings.Repeat("a", 64),
		LibrariesSHA256:  "sha256:" + strings.Repeat("b", 64),
		RuntimeVersion:   "0.34.3", ModelStore: `C:\runtime\models`,
		NumCtx: 8192, KVCacheType: "f16", FlashAttention: true, ReserveBytes: 2 << 30,
	}
}
