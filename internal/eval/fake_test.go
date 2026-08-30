package eval

// A scripted fake backend: the llm.Backend interface exists so the
// measurement layer does not care where tokens come from, and these are the
// first tests that cash that in - tool-loop behavior verified with no server.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/llm"
	"github.com/blisspixel/fitr/internal/ollama"
)

type fakeTurn struct {
	msg ollama.Message
	m   ollama.Metrics
}

type fakeBackend struct {
	name          string
	turns         []fakeTurn
	i             int
	gens          []ollama.Metrics
	genTexts      []string
	gi            int
	toolsSeen     []int // len(tools) per Chat call
	generateCalls int
	chatCalls     int
	generateErr   error
	generateErrAt map[int]error
	chatErrAt     map[int]error
	showErr       error
	running       []ollama.RunningModel
	psErr         error
	chatHook      func(int)
	messagesSeen  [][]ollama.Message
}

var _ llm.Backend = (*fakeBackend)(nil)

func (f *fakeBackend) Name() string {
	if f.name != "" {
		return f.name
	}
	return "fake"
}
func (f *fakeBackend) URL() string                                          { return "fake://" }
func (f *fakeBackend) Version(ctx context.Context) string                   { return "fake 1" }
func (f *fakeBackend) Reachable(ctx context.Context) bool                   { return true }
func (f *fakeBackend) StopAll(ctx context.Context) ([]string, error)        { return nil, nil }
func (f *fakeBackend) Tags(ctx context.Context) ([]ollama.ModelInfo, error) { return nil, nil }
func (f *fakeBackend) PS(ctx context.Context) ([]ollama.RunningModel, error) {
	return f.running, f.psErr
}
func (f *fakeBackend) Show(ctx context.Context, model string) (ollama.ModelInfo, error) {
	if f.showErr != nil {
		return ollama.ModelInfo{}, f.showErr
	}
	return ollama.ModelInfo{Name: model, Capabilities: []string{"completion", "tools"}}, nil
}

func (f *fakeBackend) Generate(ctx context.Context, model, prompt string, s ollama.Sampling) (string, ollama.Metrics, error) {
	f.generateCalls++
	if err := f.generateErrAt[f.generateCalls]; err != nil {
		return "", ollama.Metrics{}, err
	}
	if f.generateErr != nil {
		return "", ollama.Metrics{}, f.generateErr
	}
	text := "ok"
	if f.generateCalls <= len(f.genTexts) {
		text = f.genTexts[f.generateCalls-1]
	}
	m := ollama.Metrics{}
	if f.gi < len(f.gens) {
		m = f.gens[f.gi]
		f.gi++
	}
	return text, m, nil
}

func (f *fakeBackend) Chat(ctx context.Context, model string, msgs []ollama.Message, tools []ollama.Tool, s ollama.Sampling) (ollama.Message, ollama.Metrics, error) {
	f.chatCalls++
	f.messagesSeen = append(f.messagesSeen, append([]ollama.Message(nil), msgs...))
	if f.chatHook != nil {
		f.chatHook(f.chatCalls)
	}
	if err := f.chatErrAt[f.chatCalls]; err != nil {
		return ollama.Message{}, ollama.Metrics{}, err
	}
	f.toolsSeen = append(f.toolsSeen, len(tools))
	if f.i >= len(f.turns) {
		return ollama.Message{Role: "assistant", Content: "DONE"}, ollama.Metrics{}, nil
	}
	t := f.turns[f.i]
	f.i++
	return t.msg, t.m, nil
}

func callTo(name, args string) ollama.Message {
	var tc ollama.ToolCall
	tc.Function.Name = name
	tc.Function.Arguments = json.RawMessage(args)
	return ollama.Message{Role: "assistant", ToolCalls: []ollama.ToolCall{tc}}
}

func withdrawalSpec(t *testing.T) ToolLoopSpec {
	t.Helper()
	s, err := LoadSpec()
	if err != nil {
		t.Fatal(err)
	}
	if s.Withdrawal.WithdrawTool == "" || s.Withdrawal.WithdrawAfter == 0 {
		t.Fatal("withdrawal spec must name a tool and a turn")
	}
	return s.Withdrawal
}

func TestWithdrawnToolDisappearsAndGraceCallIsTolerated(t *testing.T) {
	spec := withdrawalSpec(t)
	spec.WithdrawAfter = 1
	f := &fakeBackend{turns: []fakeTurn{
		callTurn(callTo("lookup_part", `{"part":"A11"}`)), // turn 0: allowed
		callTurn(callTo("lookup_part", `{"part":"B22"}`)), // turn 1: withdrawn - grace call
		callTurn(callTo("write_file", `{"path":"prices.txt","content":"A11=4.50\nHALTED: lookup_part removed\n"}`)),
		callTurn(ollama.Message{Role: "assistant", Content: "DONE"}),
	}}
	r, err := RunToolLoop(context.Background(), f, "m", spec, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if r.DeadCalls != 1 {
		t.Fatalf("DeadCalls = %d, want 1 (the grace call)", r.DeadCalls)
	}
	if r.Ended != "clean_stop" || !r.Pass {
		t.Fatalf("ended=%q pass=%v, want a clean stop to pass a behavioral task", r.Ended, r.Pass)
	}
	// The withdrawn tool must vanish from the tools parameter: turn 0 sees all
	// three tools, later turns see two. The model is TOLD what exists.
	if f.toolsSeen[0] != 3 || f.toolsSeen[1] != 2 {
		t.Fatalf("tools per turn = %v, want [3 2 ...]", f.toolsSeen)
	}
}

func TestPersistentDeadCallsAreCounted(t *testing.T) {
	spec := withdrawalSpec(t)
	spec.WithdrawAfter = 0 // withdrawn from the first turn
	f := &fakeBackend{turns: []fakeTurn{
		callTurn(callTo("lookup_part", `{"part":"A11"}`)),
		callTurn(callTo("lookup_part", `{"part":"A11"}`)),
		callTurn(callTo("lookup_part", `{"part":"A11"}`)),
		callTurn(ollama.Message{Role: "assistant", Content: "DONE"}),
	}}
	r, err := RunToolLoop(context.Background(), f, "m", spec, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if r.DeadCalls != 3 {
		t.Fatalf("DeadCalls = %d, want 3 - persistence past the error is the failure", r.DeadCalls)
	}
}

func TestToolLoopCleanStopRequiresExactDoneToken(t *testing.T) {
	for _, content := range []string{"not DONE", "I am DONE now", "DONE.", "work is done"} {
		t.Run(content, func(t *testing.T) {
			spec := withdrawalSpec(t)
			backend := &fakeBackend{turns: []fakeTurn{
				callTurn(ollama.Message{Role: "assistant", Content: content}),
			}}
			result, err := RunToolLoop(context.Background(), backend, "m", spec, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if result.Ended != "stopped_without_done" || result.Pass {
				t.Fatalf("content %q produced ended=%q pass=%v", content, result.Ended, result.Pass)
			}
		})
	}
	for _, content := range []string{"DONE", " done ", "\r\nDoNe\r\n"} {
		if !exactDone(content) {
			t.Fatalf("exact DONE token %q was rejected", content)
		}
	}
}

func TestWarmPrefixTTFTUsesTheCacheReceipt(t *testing.T) {
	s, err := LoadSpec()
	if err != nil {
		t.Fatal(err)
	}
	t.Run("uncached gate", func(t *testing.T) { assertUncachedSpeedReceipt(t, s) })
	t.Run("cache hit", func(t *testing.T) { assertCachedSpeedReceipt(t, s) })
	t.Run("cache unknown", func(t *testing.T) { assertUnknownSpeedReceipt(t, s) })
}

func assertUncachedSpeedReceipt(t *testing.T, s *Spec) {
	t.Helper()
	f := &fakeBackend{name: "ollama", running: []ollama.RunningModel{{Name: "m"}}, gens: []ollama.Metrics{
		{TTFTSeconds: 4.0, LoadSeconds: 3.5},
		{TTFTSeconds: 0.9, DecodeTPS: 23, CacheKnown: true, CachedTokens: 0, PromptTokens: 80},
		{TTFTSeconds: 0.12, CacheKnown: true, CachedTokens: 78, PromptTokens: 2},
		{PrefillTPS: 220, PromptTokens: 2800},
	}}
	r, err := RunSpeed(context.Background(), f, "m", s, "nonce-w")
	if err != nil {
		t.Fatal(err)
	}
	if r.TTFT != 0.9 {
		t.Fatalf("TTFT = %v, want the uncached 0.9", r.TTFT)
	}
	if !r.FirstOutputObserved {
		t.Fatal("decode text was returned but the first-output receipt is false")
	}
	if r.WarmTTFT != 0.12 {
		t.Fatalf("WarmTTFT = %v, want the cache-hit 0.12", r.WarmTTFT)
	}
	if r.ColdTTFT != 4.0 {
		t.Fatalf("ColdTTFT = %v, want the loading 4.0", r.ColdTTFT)
	}
	if r.GatedPromptTok != 80 || r.GatedCachedTok != 0 || !r.GatedCacheKnown {
		t.Fatalf("gated prompt/cached = %d/%d, want 80/0 (uncached new question)", r.GatedPromptTok, r.GatedCachedTok)
	}
	if r.GatedTTFTContaminated() {
		t.Fatal("an uncached gated prompt must not be labeled a cache hit")
	}
	if !r.GatedLoadKnown || r.GatedLoad != 0 {
		t.Fatalf("gated load receipt = known %v, load %.2f", r.GatedLoadKnown, r.GatedLoad)
	}
	if !r.GatedResidencyKnown || !r.GatedResident {
		t.Fatalf("gated residency receipt = known %v, resident %v", r.GatedResidencyKnown, r.GatedResident)
	}
}

func TestGatedRequestReloadIsReceiptedSeparatelyFromWarmup(t *testing.T) {
	s, err := LoadSpec()
	if err != nil {
		t.Fatal(err)
	}
	backend := &fakeBackend{name: "ollama", gens: []ollama.Metrics{
		{TTFTSeconds: 0.8, LoadSeconds: 0},
		{TTFTSeconds: 1.2, LoadSeconds: 0.7, DecodeTPS: 23, CacheKnown: true, PromptTokens: 80},
		{TTFTSeconds: 0.1, CacheKnown: true, CachedTokens: 78, PromptTokens: 2},
		{PrefillTPS: 220, PromptTokens: 2800},
	}}
	result, err := RunSpeed(context.Background(), backend, "m", s, "nonce-reload")
	if err != nil {
		t.Fatal(err)
	}
	if !result.GatedLoadKnown || result.GatedLoad != 0.7 || result.ColdTTFT != 0 ||
		!result.GatedResidencyKnown || result.GatedResident {
		t.Fatalf("gated/warmup load receipt = %+v", result)
	}
}

func assertCachedSpeedReceipt(t *testing.T, s *Spec) {
	t.Helper()
	hit := &fakeBackend{gens: []ollama.Metrics{
		{TTFTSeconds: 0.8, LoadSeconds: 0},
		{TTFTSeconds: 0.05, DecodeTPS: 23, CacheKnown: true, CachedTokens: 78, PromptTokens: 2},
		{TTFTSeconds: 0.05, CacheKnown: true, CachedTokens: 78, PromptTokens: 2},
		{PrefillTPS: 220, PromptTokens: 2800},
	}}
	rh, _ := RunSpeed(context.Background(), hit, "m", s, "nonce-hit")
	if !rh.GatedTTFTContaminated() {
		t.Fatal("78/80 cached is a cache-hit TTFT wearing a new-question badge")
	}
	// Prefill length must not be the comparator: 78*5 < 2800, so using it
	// would hide the contamination.
	if rh.PromptTok != 2800 {
		t.Fatalf("prefill tokens = %d", rh.PromptTok)
	}
	if rh.WarmPromptTok != 2 || rh.WarmCachedTok != 78 || !rh.WarmCacheKnown {
		t.Fatalf("warm cache receipt = %d+%d known=%v", rh.WarmPromptTok, rh.WarmCachedTok, rh.WarmCacheKnown)
	}
}

func assertUnknownSpeedReceipt(t *testing.T, s *Spec) {
	t.Helper()
	// A backend that cannot report cache must not spend a second generate
	// just to invent a warm-prefix number.
	f2 := &fakeBackend{gens: []ollama.Metrics{
		{TTFTSeconds: 0.8, LoadSeconds: 0},
		{TTFTSeconds: 0.9, DecodeTPS: 23},
		{PrefillTPS: 220},
	}}
	r2, _ := RunSpeed(context.Background(), f2, "m", s, "nonce-n")
	if r2.WarmTTFT != 0 {
		t.Fatalf("WarmTTFT = %v, want 0 when CacheKnown is false", r2.WarmTTFT)
	}
	if f2.gi != 3 {
		t.Fatalf("generates = %d, want 3 (warmup + decode + prefill); a 4th would be a fabricated warm probe", f2.gi)
	}
	if r2.GatedCacheKnown || r2.PrefillCacheKnown {
		t.Fatal("backend without a cache receipt was recorded as cache-known")
	}
}

func TestRunSpeedRejectsDecodeWithNoOutput(t *testing.T) {
	s, err := LoadSpec()
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeBackend{
		genTexts: []string{"warm", "", "prefill"},
		gens: []ollama.Metrics{
			{TTFTSeconds: 0.2},
			{TTFTSeconds: 0, DecodeTPS: 20},
			{PrefillTPS: 100, PromptTokens: 100},
		},
	}
	_, err = RunSpeed(context.Background(), f, "m", s, "no-output")
	if err == nil || !strings.Contains(err.Error(), "speed probe produced no output") {
		t.Fatalf("error = %v, want an immediate no-output measurement failure", err)
	}
}

func TestCacheClassificationsRequireExplicitMisses(t *testing.T) {
	for _, tc := range []struct {
		name             string
		uncached, cached int
		wantPrefill      bool
	}{
		{name: "sixteen percent", uncached: 100, cached: 20, wantPrefill: true},
		{name: "exactly eighty percent", uncached: 20, cached: 80, wantPrefill: true},
		{name: "all cached", cached: 100, wantPrefill: true},
		{name: "just below former boundary", uncached: 2, cached: 7, wantPrefill: true},
		{name: "integer boundary", uncached: 2, cached: 8, wantPrefill: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := SpeedResult{
				GatedCacheKnown: true, GatedPromptTok: tc.uncached, GatedCachedTok: tc.cached,
				PrefillCacheKnown: true, PromptTok: tc.uncached, CachedPromptTok: tc.cached,
			}
			if got, want := s.GatedTTFTContaminated(), tc.cached > 0; got != want {
				t.Fatalf("gated contamination = %v, want %v", got, want)
			}
			if got := s.PrefillContaminated(); got != tc.wantPrefill {
				t.Fatalf("prefill contamination = %v, want %v", got, tc.wantPrefill)
			}
		})
	}
}

func TestKnownCacheReceiptRequiresPositiveDenominator(t *testing.T) {
	empty := SpeedResult{GatedCacheKnown: true, PrefillCacheKnown: true, WarmCacheKnown: true}
	if empty.GatedCacheReceiptValid() || empty.PrefillCacheReceiptValid() || empty.WarmCacheReceiptValid() {
		t.Fatal("known zero-token receipts cannot establish cache state")
	}
	miss := SpeedResult{GatedCacheKnown: true, GatedPromptTok: 1}
	if !miss.GatedCacheReceiptValid() || miss.GatedTTFTContaminated() {
		t.Fatal("a positive explicit miss must be valid and uncontaminated")
	}
}

func TestCompactionIsRecordedWhenPromptTokensShrink(t *testing.T) {
	spec := withdrawalSpec(t)
	spec.WithdrawTool, spec.WithdrawAfter = "", 0
	f := &fakeBackend{turns: []fakeTurn{
		{callTo("lookup_part", `{"part":"A11"}`), ollama.Metrics{PromptTokens: 2000}},
		{callTo("lookup_part", `{"part":"B22"}`), ollama.Metrics{PromptTokens: 7000}},
		{ollama.Message{Role: "assistant", Content: "DONE"}, ollama.Metrics{PromptTokens: 3000}},
	}}
	r, err := RunToolLoop(context.Background(), f, "m", spec, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !r.Compacted {
		t.Fatal("7000 -> 3000 is compaction")
	}
	if !r.CtxCeiling {
		t.Fatal("the peak still crossed 80% of the window")
	}
}

func TestContextCeilingIsRecorded(t *testing.T) {
	spec := withdrawalSpec(t)
	spec.WithdrawTool, spec.WithdrawAfter = "", 0
	f := &fakeBackend{turns: []fakeTurn{
		{callTo("lookup_part", `{"part":"A11"}`), ollama.Metrics{PromptTokens: 2000}},
		{callTo("lookup_part", `{"part":"B22"}`), ollama.Metrics{PromptTokens: 7000}},
		{ollama.Message{Role: "assistant", Content: "DONE"}, ollama.Metrics{PromptTokens: 7100}},
	}}
	r, err := RunToolLoop(context.Background(), f, "m", spec, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if r.MaxPromptTok != 7100 {
		t.Fatalf("MaxPromptTok = %d, want 7100", r.MaxPromptTok)
	}
	// 7100 > 80% of the 8192 context: the ceiling flag is the compaction
	// watchdog's signal - a transcript that grows to the window with no
	// management fails exactly like a looped table: structurally fine until full.
	if !r.CtxCeiling {
		t.Fatal("CtxCeiling must be set above 80% of the context window")
	}
}

func TestColdTTFTIsCapturedFromTheLoadingWarmup(t *testing.T) {
	s, err := LoadSpec()
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeBackend{gens: []ollama.Metrics{
		{TTFTSeconds: 5.2, LoadSeconds: 4.9},              // warm-up: actually loaded
		{TTFTSeconds: 0.9, DecodeTPS: 23, EvalCount: 200}, // decode probe (warm)
		{PrefillTPS: 220, PromptTokens: 2800},             // prefill probe
	}}
	r, err := RunSpeed(context.Background(), f, "m", s, "nonce-1")
	if err != nil {
		t.Fatal(err)
	}
	if r.ColdTTFT != 5.2 {
		t.Fatalf("ColdTTFT = %v, want 5.2 - the loading warm-up IS the cold-start figure", r.ColdTTFT)
	}
	if r.TTFT != 0.9 {
		t.Fatalf("TTFT = %v, want the warm 0.9", r.TTFT)
	}

	// A warm-up that did not load proves nothing about cold start.
	f2 := &fakeBackend{gens: []ollama.Metrics{
		{TTFTSeconds: 0.8, LoadSeconds: 0},
		{TTFTSeconds: 0.9, DecodeTPS: 23},
		{PrefillTPS: 220},
	}}
	r2, _ := RunSpeed(context.Background(), f2, "m", s, "nonce-2")
	if r2.ColdTTFT != 0 {
		t.Fatalf("ColdTTFT = %v, want 0 when the model was already resident", r2.ColdTTFT)
	}
}

func callTurn(m ollama.Message) fakeTurn { return fakeTurn{msg: m} }

func TestUnsafeExecTasksAreSkippedBeforeBackendOrFilesystem(t *testing.T) {
	spec, err := LoadSpec()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir() + "/not-created"
	f := &fakeBackend{}
	r, err := RunExec(context.Background(), f, "m", spec.CodeWrite, dir)
	if err != nil {
		t.Fatal(err)
	}
	if r.Outcome != OutcomeSkipped || f.generateCalls != 0 {
		t.Fatalf("outcome=%q generate calls=%d, want skipped before generation", r.Outcome, f.generateCalls)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("unsafe skip created work directory: %v", err)
	}

	tr, err := RunToolLoop(context.Background(), f, "m", spec.Agentic, dir)
	if err != nil {
		t.Fatal(err)
	}
	if tr.Outcome != OutcomeSkipped || f.chatCalls != 0 {
		t.Fatalf("outcome=%q chat calls=%d, want skipped before chat", tr.Outcome, f.chatCalls)
	}
}

func TestUnsafeExecPreflightFailsBeforeModelGeneration(t *testing.T) {
	spec, err := LoadSpec()
	if err != nil {
		t.Fatal(err)
	}
	spec.CodeWrite.Runner = []string{"shell-not-allowed", "test.py"}
	f := &fakeBackend{}
	r, err := RunExec(WithUnsafeExecution(context.Background()), f, "m", spec.CodeWrite, t.TempDir())
	var typed *Failure
	if !errors.As(err, &typed) || typed.Kind != FailureExecutorPreflight {
		t.Fatalf("preflight error = %#v", err)
	}
	if r.Outcome != OutcomeError || f.generateCalls != 0 {
		t.Fatalf("preflight outcome=%q generate calls=%d", r.Outcome, f.generateCalls)
	}
}

func TestBackendErrorsPropagateWithoutBecomingModelOutcomes(t *testing.T) {
	spec, err := LoadSpec()
	if err != nil {
		t.Fatal(err)
	}
	backendErr := errors.New("connection reset")

	refusalBackend := &fakeBackend{generateErrAt: map[int]error{2: backendErr}}
	refusal, _, err := RunRefusal(context.Background(), refusalBackend, "m", spec.Refusal)
	var failureErr *Failure
	if !errors.As(err, &failureErr) || failureErr.Kind != FailureTransport {
		t.Fatalf("refusal error = %#v, want transport failure", err)
	}
	for key, result := range refusal {
		if result.Outcome != OutcomeError {
			t.Fatalf("refusal %s transport fault became outcome %q", key, result.Outcome)
		}
	}

	checkBackend := &fakeBackend{generateErr: backendErr}
	check, err := RunCheck(context.Background(), checkBackend, "m", spec.Checks[0], 1)
	if !errors.As(err, &failureErr) || failureErr.Kind != FailureTransport || check.Outcome != OutcomeError {
		t.Fatalf("check transport error = %#v outcome=%q", err, check.Outcome)
	}

	plumbingBackend := &fakeBackend{chatErrAt: map[int]error{1: backendErr}}
	pr, err := RunPlumbing(context.Background(), plumbingBackend, "m", spec.Plumbing)
	if !errors.As(err, &failureErr) || failureErr.Kind != FailureTransport {
		t.Fatalf("plumbing error = %#v, want transport failure", err)
	}
	if pr.Outcome != OutcomeError {
		t.Fatalf("transport error became model outcome %q", pr.Outcome)
	}

	loopBackend := &fakeBackend{chatErrAt: map[int]error{1: backendErr}}
	lr, err := RunToolLoop(context.Background(), loopBackend, "m", spec.Withdrawal, t.TempDir())
	if !errors.As(err, &failureErr) || failureErr.Kind != FailureTransport {
		t.Fatalf("tool-loop error = %#v, want transport failure", err)
	}
	if lr.Ended != "transport_error" || lr.Outcome != OutcomeError {
		t.Fatalf("tool-loop ended=%q outcome=%q", lr.Ended, lr.Outcome)
	}
}

func TestPlumbingFaultMatrixNeverReturnsBinaryEvidence(t *testing.T) {
	spec, err := LoadSpec()
	if err != nil {
		t.Fatal(err)
	}
	backendErr := errors.New("injected transport fault")
	validTurns := []fakeTurn{
		callTurn(callTo("get_weather", `{"city":"Oslo"}`)),
		callTurn(ollama.Message{Role: "assistant", Content: "minus 3 degrees"}),
		callTurn(ollama.Message{Role: "assistant", Content: "Paris"}),
	}
	for _, test := range []struct {
		name      string
		showErr   error
		chatErrAt map[int]error
	}{
		{name: "show", showErr: backendErr},
		{name: "emit", chatErrAt: map[int]error{1: backendErr}},
		{name: "roundtrip", chatErrAt: map[int]error{2: backendErr}},
		{name: "irrelevance", chatErrAt: map[int]error{3: backendErr}},
	} {
		t.Run(test.name, func(t *testing.T) {
			backend := &fakeBackend{turns: validTurns, showErr: test.showErr, chatErrAt: test.chatErrAt}
			result, err := RunPlumbing(context.Background(), backend, "m", spec.Plumbing)
			var typed *Failure
			if !errors.As(err, &typed) || typed.Kind != FailureTransport {
				t.Fatalf("error = %#v", err)
			}
			if result.Outcome != OutcomeError {
				t.Fatalf("outcome = %q, want error", result.Outcome)
			}
		})
	}
}

func TestPlumbingRoundTripEchoesToolCallID(t *testing.T) {
	spec, err := LoadSpec()
	if err != nil {
		t.Fatal(err)
	}
	call := callTo("get_weather", `{"city":"Oslo"}`)
	call.ToolCalls[0].ID = "call_weather_1"
	backend := &fakeBackend{turns: []fakeTurn{
		callTurn(call),
		callTurn(ollama.Message{Role: "assistant", Content: "minus 3 degrees"}),
		callTurn(ollama.Message{Role: "assistant", Content: "Paris"}),
	}}
	if _, err := RunPlumbing(context.Background(), backend, "m", spec.Plumbing); err != nil {
		t.Fatal(err)
	}
	if len(backend.messagesSeen) < 2 || len(backend.messagesSeen[1]) != 3 {
		t.Fatalf("round-trip messages = %#v", backend.messagesSeen)
	}
	toolResult := backend.messagesSeen[1][2]
	if toolResult.Role != "tool" || toolResult.ToolCallID != "call_weather_1" {
		t.Fatalf("tool result did not echo tool_call_id: %+v", toolResult)
	}
}

func TestToolFilesystemFailureCannotBecomeModelPassOrFail(t *testing.T) {
	spec := withdrawalSpec(t)
	dir := t.TempDir()
	f := &fakeBackend{
		turns: []fakeTurn{callTurn(callTo("list_files", `{}`))},
		chatHook: func(call int) {
			if call == 1 {
				if err := os.RemoveAll(dir); err != nil {
					t.Fatalf("remove injected workspace: %v", err)
				}
			}
		},
	}
	r, err := RunToolLoop(context.Background(), f, "m", spec, dir)
	var typed *Failure
	if !errors.As(err, &typed) || typed.Kind != FailureFixtureIO {
		t.Fatalf("filesystem failure = %#v", err)
	}
	if r.Outcome != OutcomeError || r.Pass {
		t.Fatalf("filesystem failure became model evidence: %+v", r)
	}
}

func TestToolFilesRejectNonPortableNamesAndOversizedContent(t *testing.T) {
	dir := t.TempDir()
	written := map[string]bool{}
	for _, name := range []string{
		"../outside.txt", `folder\outside.txt`, "NUL", "con.txt", "COM¹", "fixture.txt:stream",
		"trailing.", "trailing ", "question?.txt", strings.Repeat("a", 256),
	} {
		got, err := doTool(context.Background(), dir, "write_file", name, "data", nil,
			ToolLoopSpec{}, written, resolvedTaskRunner{}, nil)
		if err != nil || got != "ERROR: invalid task-local path" {
			t.Errorf("write_file(%q) = %q, %v", name, got, err)
		}
	}

	got, err := doTool(context.Background(), dir, "write_file", "valid-name.txt",
		strings.Repeat("x", maxTaskToolFileBytes+1), nil, ToolLoopSpec{}, written, resolvedTaskRunner{}, nil)
	if err != nil || !strings.Contains(got, "exceeds") {
		t.Fatalf("oversized write = %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "valid-name.txt")); !os.IsNotExist(err) {
		t.Fatalf("oversized write created a file: %v", err)
	}
}
