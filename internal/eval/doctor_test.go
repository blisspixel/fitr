package eval

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/ollama"
)

func TestDivergence(t *testing.T) {
	if id, d, _ := Divergence([]string{"abc", "abc", "abc"}); !id || d != 1 {
		t.Fatalf("identical runs: got identical=%v distinct=%d", id, d)
	}
	id, d, first := Divergence([]string{"abcdef", "abcXef", "abcdef"})
	if id || d != 2 {
		t.Fatalf("got identical=%v distinct=%d", id, d)
	}
	if first != 3 {
		t.Fatalf("first divergence = %d, want 3", first)
	}
	// Differ only by length: divergence is at the shorter length.
	if _, _, first := Divergence([]string{"abc", "abcdef"}); first != 3 {
		t.Fatalf("length-only divergence = %d, want 3", first)
	}
}

func TestReportDeterminismStates(t *testing.T) {
	var r DoctorResult
	if ok := reportDeterminism(&r, "x", []string{"a", "a"}, "why"); !ok {
		t.Fatal("identical outputs must report deterministic")
	}
	if r.Checks[0].State != "PASS" {
		t.Fatalf("state = %s, want PASS", r.Checks[0].State)
	}
	if ok := reportDeterminism(&r, "y", []string{"a", "b"}, "why"); ok {
		t.Fatal("diverging outputs must not report deterministic")
	}
	// Nondeterminism is a WARN, never a FAIL: repeats-and-intervals survive
	// it. A FAIL would tell people their box is broken when it is merely noisy.
	if r.Checks[1].State != "WARN" {
		t.Fatalf("state = %s, want WARN", r.Checks[1].State)
	}
}

func TestRunDoctorHealthyStack(t *testing.T) {
	backend := &fakeBackend{
		genTexts: []string{
			"OK", "context", "planets", "planets", `{"planets":[]}`, `{"planets":[]}`,
		},
		gens: []ollama.Metrics{
			{EvalCount: 1, TTFTSeconds: 0.1, LoadSeconds: 0.5},
			{PromptTokens: 2800}, {}, {}, {}, {},
		},
	}
	result, err := RunDoctor(context.Background(), backend, "model", 2, DoctorOpts{
		Placement: func(context.Context) string { return "GPU 100%" },
		Config:    map[string]string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Healthy || result.Verdict != "healthy - measurements on this box mean what they say" {
		t.Fatalf("healthy doctor = %+v", result)
	}
	for _, check := range result.Checks {
		if check.State != "PASS" {
			t.Fatalf("healthy check = %+v", check)
		}
	}
}

func TestRunDoctorReportsCaveatsWithoutCallingThemFatal(t *testing.T) {
	backend := &fakeBackend{
		genTexts: []string{"OK", "context", "planets-a", "planets-b"},
		gens: []ollama.Metrics{
			{EvalCount: 1}, {PromptTokens: 100}, {}, {},
		},
		generateErrAt: map[int]error{5: errors.New("json mode unavailable")},
	}
	result, err := RunDoctor(context.Background(), backend, "model", 2, DoctorOpts{
		Placement: func(context.Context) string { return "GPU 62%" },
		Config: map[string]string{
			"OLLAMA_NUM_PARALLEL": "2", "OLLAMA_MAX_LOADED_MODELS": "3",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Healthy || !strings.Contains(result.Verdict, "caveat") {
		t.Fatalf("caveat doctor = %+v", result)
	}
	states := map[string]string{}
	for _, check := range result.Checks {
		states[check.ID] = check.State
	}
	for _, id := range []string{"placement", "served_context", "determinism_text", "config"} {
		if states[id] != "WARN" {
			t.Errorf("%s = %q, want WARN", id, states[id])
		}
	}
	if states["determinism_json"] != "SKIP" {
		t.Errorf("determinism_json = %q, want SKIP", states["determinism_json"])
	}
}

func TestRunDoctorTurnsInitialGenerationFaultsIntoFindings(t *testing.T) {
	for _, tc := range []struct {
		name    string
		backend *fakeBackend
		want    string
	}{
		{"transport", &fakeBackend{generateErr: errors.New("offline")}, "did not generate"},
		{"empty", &fakeBackend{genTexts: []string{""}, gens: []ollama.Metrics{{DoneReason: "stop"}}}, "emits no tokens"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := RunDoctor(context.Background(), tc.backend, "model", 1, DoctorOpts{})
			if err != nil || result.Healthy || len(result.Checks) != 1 ||
				result.Checks[0].State != "FAIL" || !strings.Contains(result.Verdict, tc.want) {
				t.Fatalf("initial fault = %+v, %v", result, err)
			}
		})
	}
}

func TestRunDoctorPropagatesLaterPlainTextTransportFault(t *testing.T) {
	backend := &fakeBackend{
		genTexts:      []string{"OK", "context"},
		gens:          []ollama.Metrics{{EvalCount: 1}, {PromptTokens: 2800}},
		generateErrAt: map[int]error{3: errors.New("connection reset")},
	}
	if _, err := RunDoctor(context.Background(), backend, "model", 2, DoctorOpts{}); err == nil ||
		!strings.Contains(err.Error(), "connection reset") {
		t.Fatalf("later transport error = %v", err)
	}
}
