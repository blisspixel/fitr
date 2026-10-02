package experiment

import (
	"context"
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/ollama"
	"github.com/blisspixel/fitr/internal/record"
)

type mockBackend struct {
	name string
}

func (m *mockBackend) Name() string                       { return m.name }
func (m *mockBackend) URL() string                        { return "http://mock" }
func (m *mockBackend) Version(ctx context.Context) string { return "1.0" }
func (m *mockBackend) Reachable(ctx context.Context) bool { return true }
func (m *mockBackend) Generate(ctx context.Context, model, prompt string, s ollama.Sampling) (string, ollama.Metrics, error) {
	return "ok", ollama.Metrics{}, nil
}
func (m *mockBackend) Chat(ctx context.Context, model string, msgs []ollama.Message, tools []ollama.Tool, s ollama.Sampling) (ollama.Message, ollama.Metrics, error) {
	return ollama.Message{Role: "assistant", Content: "ok"}, ollama.Metrics{}, nil
}
func (m *mockBackend) Tags(ctx context.Context) ([]ollama.ModelInfo, error) { return nil, nil }
func (m *mockBackend) Show(ctx context.Context, model string) (ollama.ModelInfo, error) {
	return ollama.ModelInfo{}, nil
}
func (m *mockBackend) PS(ctx context.Context) ([]ollama.RunningModel, error) { return nil, nil }
func (m *mockBackend) StopAll(ctx context.Context) ([]string, error)         { return nil, nil }

type mockSlotBackend struct {
	mockBackend
	slots    int
	observed bool
	err      error
}

func (m *mockSlotBackend) ObserveSlots(ctx context.Context) (int, bool, error) {
	return m.slots, m.observed, m.err
}

func testModelIdentity() record.ModelIdentity {
	return record.ModelIdentity{
		Requested:        "qwen3:8b",
		Resolved:         "qwen3:8b-q4_k_m",
		Backend:          "ollama",
		Runtime:          "0.5.0",
		Kind:             "digest",
		Value:            "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		ContentAddressed: true,
	}
}

func TestServingPlanValidation(t *testing.T) {
	model := testModelIdentity()
	plan, err := NewServingPlan(model, "dev-123", 4, 20, 2, 4096)
	if err != nil {
		t.Fatalf("unexpected error creating plan: %v", err)
	}
	if err := plan.Validate(); err != nil {
		t.Fatalf("valid plan failed validation: %v", err)
	}

	tampered := plan
	tampered.Concurrency = 8
	if err := tampered.Validate(); err == nil {
		t.Fatal("tampered plan should fail validation due to digest mismatch")
	}

	for _, tc := range []struct {
		name        string
		concurrency int
		requests    int
		warmup      int
		ctx         int
		deviceKey   string
		model       record.ModelIdentity
		wantErr     string
	}{
		{"invalid concurrency low", 0, 20, 2, 4096, "dev", model, "concurrency must be between"},
		{"invalid concurrency high", 65, 20, 2, 4096, "dev", model, "concurrency must be between"},
		{"invalid requests low", 4, 0, 2, 4096, "dev", model, "requests must be between"},
		{"invalid requests high", 4, 1001, 2, 4096, "dev", model, "requests must be between"},
		{"invalid warmup negative", 4, 20, -1, 4096, "dev", model, "warmup requests must be between"},
		{"invalid warmup high", 4, 20, 51, 4096, "dev", model, "warmup requests must be between"},
		{"invalid context", 4, 20, 2, 0, "dev", model, "requested context must be between"},
		{"empty device key", 4, 20, 2, 4096, "   ", model, "device key is required"},
		{"empty model", 4, 20, 2, 4096, "dev", record.ModelIdentity{}, "model is required"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewServingPlan(tc.model, tc.deviceKey, tc.concurrency, tc.requests, tc.warmup, tc.ctx)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want substring %q", err, tc.wantErr)
			}
		})
	}
}

func TestObserveConcurrency(t *testing.T) {
	ctx := context.Background()

	// 1. Backend without SlotObserver
	unobserved := &mockBackend{name: "ollama"}
	obs := ObserveConcurrency(ctx, unobserved, 4)
	if obs.State != ConcurrencyDeclared {
		t.Fatalf("state = %s, want %s", obs.State, ConcurrencyDeclared)
	}
	if obs.ObservedSlots != nil {
		t.Fatalf("observed slots = %v, want nil", obs.ObservedSlots)
	}

	// 2. Backend with SlotObserver matching declared
	verified := &mockSlotBackend{mockBackend: mockBackend{name: "llama-server"}, slots: 4, observed: true}
	obs = ObserveConcurrency(ctx, verified, 4)
	if obs.State != ConcurrencyVerified {
		t.Fatalf("state = %s, want %s", obs.State, ConcurrencyVerified)
	}
	if obs.ObservedSlots == nil || *obs.ObservedSlots != 4 {
		t.Fatalf("observed slots = %v, want 4", obs.ObservedSlots)
	}

	// 3. Backend with SlotObserver mismatching declared
	mismatched := &mockSlotBackend{mockBackend: mockBackend{name: "llama-server"}, slots: 2, observed: true}
	obs = ObserveConcurrency(ctx, mismatched, 4)
	if obs.State != ConcurrencyMismatch {
		t.Fatalf("state = %s, want %s", obs.State, ConcurrencyMismatch)
	}
	if obs.ObservedSlots == nil || *obs.ObservedSlots != 2 {
		t.Fatalf("observed slots = %v, want 2", obs.ObservedSlots)
	}
}

func TestAnalyzeServingExcludesWarmup(t *testing.T) {
	model := testModelIdentity()
	plan, err := NewServingPlan(model, "dev-123", 2, 2, 1, 4096)
	if err != nil {
		t.Fatal(err)
	}
	concurrency := ConcurrencyObservation{DeclaredLevel: 2, State: ConcurrencyDeclared, Backend: "ollama"}

	requests := []ServingRequestObservation{
		{
			Index:                1,
			IsWarmup:             true,
			QueueDurationMillis:  10,
			ServerDurationMillis: 500,
			PromptTokens:         100,
			CompletionTokens:     1000, // Should be excluded!
			AcceptedOutcome:      true,
		},
		{
			Index:                2,
			IsWarmup:             false,
			QueueDurationMillis:  20,
			ServerDurationMillis: 200,
			PromptTokens:         50,
			CompletionTokens:     50,
			AcceptedOutcome:      true,
		},
		{
			Index:                3,
			IsWarmup:             false,
			QueueDurationMillis:  30,
			ServerDurationMillis: 200,
			PromptTokens:         50,
			CompletionTokens:     50,
			AcceptedOutcome:      true,
		},
	}

	report, err := AnalyzeServing(plan, concurrency, requests, 1000)
	if err != nil {
		t.Fatal(err)
	}

	if report.WarmupExcluded != 1 {
		t.Fatalf("warmup excluded = %d, want 1", report.WarmupExcluded)
	}
	if report.TotalThroughput.TotalCompletionTokens != 100 {
		t.Fatalf("total completion tokens = %d, want 100 (excluding warmup 1000)", report.TotalThroughput.TotalCompletionTokens)
	}
	if report.AcceptedWork.EvaluatedCount != 2 {
		t.Fatalf("evaluated count = %d, want 2", report.AcceptedWork.EvaluatedCount)
	}
}

func TestAnalyzeServingQueueVersusServerSeparation(t *testing.T) {
	model := testModelIdentity()
	plan, err := NewServingPlan(model, "dev-123", 2, 3, 0, 4096)
	if err != nil {
		t.Fatal(err)
	}
	concurrency := ConcurrencyObservation{DeclaredLevel: 2, State: ConcurrencyDeclared, Backend: "ollama"}

	requests := []ServingRequestObservation{
		{Index: 1, QueueDurationMillis: 100, ServerDurationMillis: 400, PromptTokens: 10, CompletionTokens: 20, AcceptedOutcome: true},
		{Index: 2, QueueDurationMillis: 200, ServerDurationMillis: 500, PromptTokens: 10, CompletionTokens: 20, AcceptedOutcome: true},
		{Index: 3, QueueDurationMillis: 300, ServerDurationMillis: 600, PromptTokens: 10, CompletionTokens: 20, AcceptedOutcome: true},
	}

	report, err := AnalyzeServing(plan, concurrency, requests, 2000)
	if err != nil {
		t.Fatal(err)
	}

	if report.ClientQueue.MeanMillis != 200.0 {
		t.Fatalf("queue mean = %f, want 200.0", report.ClientQueue.MeanMillis)
	}
	if report.ServerTiming.MeanMillis != 500.0 {
		t.Fatalf("server timing mean = %f, want 500.0", report.ServerTiming.MeanMillis)
	}
	// P50 should be populated since N=3 >= 3
	if report.ClientQueue.P50Millis == nil || *report.ClientQueue.P50Millis != 200.0 {
		t.Fatalf("queue p50 = %v, want 200.0", report.ClientQueue.P50Millis)
	}
	if report.ServerTiming.P50Millis == nil || *report.ServerTiming.P50Millis != 500.0 {
		t.Fatalf("server timing p50 = %v, want 500.0", report.ServerTiming.P50Millis)
	}
	// P95 must be nil because N=3 < 20
	if report.ClientQueue.P95Millis != nil {
		t.Fatalf("queue p95 = %v, want nil when N=3 < 20", report.ClientQueue.P95Millis)
	}
	if report.ServerTiming.P95Millis != nil {
		t.Fatalf("server timing p95 = %v, want nil when N=3 < 20", report.ServerTiming.P95Millis)
	}
}

func TestAnalyzeServingPercentileSampleThresholds(t *testing.T) {
	model := testModelIdentity()
	concurrency := ConcurrencyObservation{DeclaredLevel: 4, State: ConcurrencyDeclared, Backend: "ollama"}

	// 1. N = 2: P50 and P95 must both be withheld (nil)
	plan2, _ := NewServingPlan(model, "dev-123", 4, 2, 0, 4096)
	reqs2 := []ServingRequestObservation{
		{Index: 1, QueueDurationMillis: 10, ServerDurationMillis: 100, AcceptedOutcome: true},
		{Index: 2, QueueDurationMillis: 20, ServerDurationMillis: 200, AcceptedOutcome: true},
	}
	rep2, err := AnalyzeServing(plan2, concurrency, reqs2, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if rep2.ClientQueue.P50Millis != nil || rep2.ClientQueue.P95Millis != nil {
		t.Fatalf("expected P50 and P95 nil at N=2, got P50=%v P95=%v", rep2.ClientQueue.P50Millis, rep2.ClientQueue.P95Millis)
	}

	// 2. N = 20: P50 and P95 must both be reported
	plan20, _ := NewServingPlan(model, "dev-123", 4, 20, 0, 4096)
	var reqs20 []ServingRequestObservation
	ttftBase := int64(50)
	for i := 1; i <= 20; i++ {
		ttft := ttftBase + int64(i*5)
		reqs20 = append(reqs20, ServingRequestObservation{
			Index:                i,
			QueueDurationMillis:  int64(i * 10),
			ServerDurationMillis: int64(i * 50),
			TTFTMillis:           &ttft,
			PromptTokens:         10,
			CompletionTokens:     20,
			AcceptedOutcome:      true,
		})
	}
	rep20, err := AnalyzeServing(plan20, concurrency, reqs20, 5000)
	if err != nil {
		t.Fatal(err)
	}
	if rep20.ClientQueue.P50Millis == nil || rep20.ClientQueue.P95Millis == nil {
		t.Fatalf("expected P50 and P95 reported at N=20, got P50=%v P95=%v", rep20.ClientQueue.P50Millis, rep20.ClientQueue.P95Millis)
	}
	if rep20.ServerTiming.P50Millis == nil || rep20.ServerTiming.P95Millis == nil {
		t.Fatalf("expected server timing P50 and P95 reported at N=20, got P50=%v P95=%v", rep20.ServerTiming.P50Millis, rep20.ServerTiming.P95Millis)
	}
	if rep20.TTFT == nil || rep20.TTFT.P50Millis == nil || rep20.TTFT.P95Millis == nil {
		t.Fatalf("expected TTFT P50 and P95 reported at N=20, got %+v", rep20.TTFT)
	}
}

func TestServingBundleRoundtripAndValidation(t *testing.T) {
	model := testModelIdentity()
	plan, err := NewServingPlan(model, "dev-123", 2, 3, 1, 4096)
	if err != nil {
		t.Fatal(err)
	}
	concurrency := ConcurrencyObservation{DeclaredLevel: 2, State: ConcurrencyVerified, Backend: "llama-server"}

	requests := []ServingRequestObservation{
		{Index: 1, IsWarmup: true, QueueDurationMillis: 5, ServerDurationMillis: 100, AcceptedOutcome: true},
		{Index: 2, IsWarmup: false, QueueDurationMillis: 10, ServerDurationMillis: 120, PromptTokens: 10, CompletionTokens: 20, AcceptedOutcome: true},
		{Index: 3, IsWarmup: false, QueueDurationMillis: 15, ServerDurationMillis: 130, PromptTokens: 10, CompletionTokens: 20, AcceptedOutcome: true},
		{Index: 4, IsWarmup: false, QueueDurationMillis: 20, ServerDurationMillis: 140, PromptTokens: 10, CompletionTokens: 20, AcceptedOutcome: false, RefusalOrFault: "model refusal: safe policy"},
	}

	bundle, err := NewServingBundle(plan, concurrency, requests, 1500)
	if err != nil {
		t.Fatalf("unexpected error creating bundle: %v", err)
	}

	data, err := bundle.JSON()
	if err != nil {
		t.Fatalf("unexpected error marshaling bundle: %v", err)
	}

	decoded, err := decodeServingBundle(data)
	if err != nil {
		t.Fatalf("unexpected error decoding bundle: %v", err)
	}

	if decoded.Report.AcceptedWork.RefusalCount != 1 {
		t.Fatalf("refusal count = %d, want 1", decoded.Report.AcceptedWork.RefusalCount)
	}

	// Tamper test: modify a request in the bundle, ensure Validate() rejects it
	tampered := decoded
	tampered.Requests[1].QueueDurationMillis = 999
	if _, err := tampered.Validate(); err == nil {
		t.Fatal("tampered bundle should fail Validate()")
	}
}

func TestAnalyzeServingNegativeDurationAndWallTimeErrors(t *testing.T) {
	model := testModelIdentity()
	plan, _ := NewServingPlan(model, "dev-123", 2, 2, 0, 4096)
	concurrency := ConcurrencyObservation{DeclaredLevel: 2, State: ConcurrencyDeclared, Backend: "ollama"}

	// 1. Non-positive exposure wall
	reqs := []ServingRequestObservation{
		{Index: 1, QueueDurationMillis: 10, ServerDurationMillis: 100, AcceptedOutcome: true},
	}
	_, err := AnalyzeServing(plan, concurrency, reqs, 0)
	if err == nil || !strings.Contains(err.Error(), "exposure wall duration must be positive") {
		t.Fatalf("error = %v, want exposure wall error", err)
	}

	// 2. Negative queue duration
	negQueue := []ServingRequestObservation{
		{Index: 1, QueueDurationMillis: -5, ServerDurationMillis: 100, AcceptedOutcome: true},
	}
	_, err = AnalyzeServing(plan, concurrency, negQueue, 1000)
	if err == nil || !strings.Contains(err.Error(), "negative queue or server duration") {
		t.Fatalf("error = %v, want negative duration error", err)
	}

	// 3. All requests are warmup
	allWarmup := []ServingRequestObservation{
		{Index: 1, IsWarmup: true, QueueDurationMillis: 10, ServerDurationMillis: 100, AcceptedOutcome: true},
	}
	_, err = AnalyzeServing(plan, concurrency, allWarmup, 1000)
	if err == nil || !strings.Contains(err.Error(), "warm-up cannot constitute serving evidence") {
		t.Fatalf("error = %v, want no evaluated requests error", err)
	}
}
