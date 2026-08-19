package eval

// A scripted fake backend: the llm.Backend interface exists so the
// measurement layer does not care where tokens come from, and these are the
// first tests that cash that in - tool-loop behavior verified with no server.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/blisspixel/fitr/internal/llm"
	"github.com/blisspixel/fitr/internal/ollama"
)

type fakeTurn struct {
	msg ollama.Message
	m   ollama.Metrics
}

type fakeBackend struct {
	turns     []fakeTurn
	i         int
	gens      []ollama.Metrics
	gi        int
	toolsSeen []int // len(tools) per Chat call
}

var _ llm.Backend = (*fakeBackend)(nil)

func (f *fakeBackend) Name() string                                          { return "fake" }
func (f *fakeBackend) URL() string                                           { return "fake://" }
func (f *fakeBackend) Version(ctx context.Context) string                    { return "fake 1" }
func (f *fakeBackend) Reachable(ctx context.Context) bool                    { return true }
func (f *fakeBackend) StopAll(ctx context.Context) ([]string, error)         { return nil, nil }
func (f *fakeBackend) Tags(ctx context.Context) ([]ollama.ModelInfo, error)  { return nil, nil }
func (f *fakeBackend) PS(ctx context.Context) ([]ollama.RunningModel, error) { return nil, nil }
func (f *fakeBackend) Show(ctx context.Context, model string) (ollama.ModelInfo, error) {
	return ollama.ModelInfo{Name: model, Capabilities: []string{"completion", "tools"}}, nil
}

func (f *fakeBackend) Generate(ctx context.Context, model, prompt string, s ollama.Sampling) (string, ollama.Metrics, error) {
	if f.gi >= len(f.gens) {
		return "ok", ollama.Metrics{}, nil
	}
	m := f.gens[f.gi]
	f.gi++
	return "ok", m, nil
}

func (f *fakeBackend) Chat(ctx context.Context, model string, msgs []ollama.Message, tools []ollama.Tool, s ollama.Sampling) (ollama.Message, ollama.Metrics, error) {
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

func TestWarmPrefixTTFTUsesTheCacheReceipt(t *testing.T) {
	s, err := LoadSpec()
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeBackend{gens: []ollama.Metrics{
		{TTFTSeconds: 4.0, LoadSeconds: 3.5},
		{TTFTSeconds: 0.9, DecodeTPS: 23, CacheKnown: true, CachedTokens: 0, PromptTokens: 80},
		{TTFTSeconds: 0.12, CacheKnown: true, CachedTokens: 78, PromptTokens: 80},
		{PrefillTPS: 220, PromptTokens: 2800},
	}}
	r, err := RunSpeed(context.Background(), f, "m", s, "nonce-w")
	if err != nil {
		t.Fatal(err)
	}
	if r.TTFT != 0.9 {
		t.Fatalf("TTFT = %v, want the uncached 0.9", r.TTFT)
	}
	if r.WarmTTFT != 0.12 {
		t.Fatalf("WarmTTFT = %v, want the cache-hit 0.12", r.WarmTTFT)
	}
	if r.ColdTTFT != 4.0 {
		t.Fatalf("ColdTTFT = %v, want the loading 4.0", r.ColdTTFT)
	}

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
