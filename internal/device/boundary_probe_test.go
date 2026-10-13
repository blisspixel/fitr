package device

import (
	"strings"
	"testing"
)

func declaredReceipt(tokens int) ContextVerification {
	return ContextVerification{
		RequestedTokens: tokens,
		EffectiveTokens: &tokens,
		EffectiveSource: ContextSourceRuntimeReport,
	}
}

func TestBoundaryPromptStaysInsideTheCeiling(t *testing.T) {
	text, ok := BoundaryPrompt(128)
	if !ok || strings.Count(text, "tok") != 128+BoundaryProbeMargin {
		t.Fatalf("prompt copies = %d, ok %v", strings.Count(text, "tok"), ok)
	}
	if _, ok := BoundaryPrompt(BoundaryProbeCeiling + 1); ok {
		t.Fatal("a prompt above the ceiling was built")
	}
	if _, ok := BoundaryPrompt(0); ok {
		t.Fatal("a zero window produced a prompt")
	}
}

func TestResolveBoundaryConfirmsAMatchingRefusal(t *testing.T) {
	receipt := declaredReceipt(4096)
	got, note := ResolveBoundary(receipt, BoundaryObservation{Sent: true, Overflow: true, OverflowNCtx: 4096, PromptTokens: 4200})
	if note != "" || got.EffectiveSource != ContextSourceBoundaryProbe || got.EffectiveTokens == nil || *got.EffectiveTokens != 4096 {
		t.Fatalf("confirmed = %+v, note %q", got, note)
	}
}

func TestResolveBoundaryClearsAContradictoryWindow(t *testing.T) {
	receipt := declaredReceipt(8192)
	got, note := ResolveBoundary(receipt, BoundaryObservation{Sent: true, Overflow: true, OverflowNCtx: 4096, PromptTokens: 5000})
	if got.EffectiveTokens != nil || got.EffectiveSource != "" {
		t.Fatalf("contradiction kept an effective window: %+v", got)
	}
	if !strings.Contains(note, "4096") || !strings.Contains(note, "8192") {
		t.Fatalf("note = %q", note)
	}
}

func TestResolveBoundaryDoesNotTreatAcceptanceAsTheDeclaration(t *testing.T) {
	receipt := declaredReceipt(2048)
	got, note := ResolveBoundary(receipt, BoundaryObservation{Sent: true, Accepted: true, PromptTokens: 2200})
	if got.EffectiveTokens != nil {
		t.Fatalf("acceptance above the declaration set effective = %d", *got.EffectiveTokens)
	}
	if !strings.Contains(note, "understated") {
		t.Fatalf("note = %q", note)
	}
	larger := 8192
	receipt.EffectiveTokens = &larger
	got, note = ResolveBoundary(receipt, BoundaryObservation{Sent: true, Accepted: true, PromptTokens: 2200})
	if got.EffectiveTokens == nil || *got.EffectiveTokens != larger || got.EffectiveSource != ContextSourceRuntimeReport {
		t.Fatalf("a larger runtime report was replaced: %+v", got)
	}
	if !strings.Contains(note, "understated") {
		t.Fatalf("note = %q", note)
	}
}

func TestResolveBoundaryKeepsAShortProbeAndOrdinaryErrors(t *testing.T) {
	receipt := declaredReceipt(4096)
	got, note := ResolveBoundary(receipt, BoundaryObservation{Sent: true, Accepted: true, PromptTokens: 1000})
	if got.EffectiveSource != ContextSourceRuntimeReport || got.EffectiveTokens == nil || *got.EffectiveTokens != 4096 {
		t.Fatalf("short probe replaced the runtime report: %+v", got)
	}
	if !strings.Contains(note, "shorter") {
		t.Fatalf("note = %q", note)
	}
	got, note = ResolveBoundary(receipt, BoundaryObservation{Sent: true, Unrecognized: true})
	if note != "" || got.EffectiveSource != ContextSourceRuntimeReport {
		t.Fatalf("ordinary error changed the receipt: %+v %q", got, note)
	}
	high := declaredReceipt(BoundaryProbeCeiling + 1)
	got, note = ResolveBoundary(high, BoundaryObservation{})
	if got.EffectiveSource != ContextSourceRuntimeReport || !strings.Contains(note, "not sent") {
		t.Fatalf("ceiling = %+v %q", got, note)
	}
}
