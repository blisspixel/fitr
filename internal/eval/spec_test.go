package eval

import (
	"strings"
	"testing"
)

func TestSpecLoadsAndIsComplete(t *testing.T) {
	s, err := LoadSpec()
	if err != nil {
		t.Fatal(err)
	}
	if s.Speed.Decode.Prompt == "" || s.Speed.Decode.NumPredict == 0 {
		t.Fatal("speed decode spec incomplete")
	}
	if !s.Speed.Prefill.NonceRequired {
		t.Fatal("prefill MUST require a nonce; identical prompts hit the prompt cache")
	}
	if s.CodeWrite.PassIfStdoutContains == "" || len(s.CodeWrite.Files) == 0 {
		t.Fatal("code_write spec incomplete")
	}
	if len(s.CodeFix.Files) != 3 {
		t.Fatalf("code_fix should ship 3 files, got %d", len(s.CodeFix.Files))
	}
	if len(s.Tools.Tools) != 3 {
		t.Fatalf("tools spec should define 3 tools, got %d", len(s.Tools.Tools))
	}
	if s.Agentic.MaxTurns < 20 {
		t.Fatalf("agentic max_turns = %d, want >=20", s.Agentic.MaxTurns)
	}
	if len(s.Agentic.Tools) != 4 {
		t.Fatalf("agentic needs list/read/write/run_tests, got %d", len(s.Agentic.Tools))
	}
	if len(s.Refusal.Prompts) != 3 {
		t.Fatalf("refusal battery = %d prompts, want 3", len(s.Refusal.Prompts))
	}
	if len(s.Refusal.RefusalMarkers) < 10 {
		t.Fatal("refusal markers look truncated")
	}
	if len(s.Plumbing.Rungs) != 5 {
		t.Fatalf("plumbing = %d rungs, want 5", len(s.Plumbing.Rungs))
	}
	// The irrelevance rung is the one most local models fail and needs no
	// ground truth -- make sure it survived the spec extraction.
	last := s.Plumbing.Rungs[len(s.Plumbing.Rungs)-1]
	if last.ID != "5_irrelevance" || last.Prompt == "" {
		t.Fatalf("irrelevance rung missing or empty: %+v", last)
	}
	if s.Version.ResultSchemaVersion == 0 {
		t.Fatal("result schema version not recorded")
	}
}

func TestSamplingIsDeterministic(t *testing.T) {
	s, _ := LoadSpec()
	if s.Speed.Sampling.Temperature != 0 || s.Speed.Sampling.TopK != 1 {
		t.Fatalf("speed sampling must be greedy, got %+v", s.Speed.Sampling)
	}
}

func TestNoPromptShipsUnresolvedPlaceholders(t *testing.T) {
	// A prompt full of unfilled placeholders asks the model to fix code it
	// cannot see, then scores it as failing. That happened; this stops it.
	s, err := LoadSpec()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		tmpl  string
		files map[string]string
	}{
		{"code_write", s.CodeWrite.Prompt, s.CodeWrite.Files},
		{"code_fix", s.CodeFix.Prompt, s.CodeFix.Files},
	} {
		got, err := RenderPrompt(tc.tmpl, tc.files)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		for _, bad := range []string{"{d}", "{c}", "{t}", "{{file:"} {
			if strings.Contains(got, bad) {
				t.Fatalf("%s prompt still contains %q after rendering", tc.name, bad)
			}
		}
	}
}

func TestCodeFixPromptActuallyContainsTheSource(t *testing.T) {
	s, _ := LoadSpec()
	got, err := RenderPrompt(s.CodeFix.Prompt, s.CodeFix.Files)
	if err != nil {
		t.Fatal(err)
	}
	// The whole task is finding a bug in percent_off; if the function body is
	// not in the prompt, the model is guessing.
	for _, want := range []string{"def percent_off", "def total", "CART_OK"} {
		if !strings.Contains(got, want) {
			t.Fatalf("rendered prompt is missing %q -- the model cannot see the code", want)
		}
	}
}

func TestUnresolvedPlaceholderIsFatal(t *testing.T) {
	if _, err := RenderPrompt("see {{file:missing.py}}", map[string]string{"other.py": "x"}); err == nil {
		t.Fatal("an unresolved placeholder must be an error, not a degraded prompt")
	}
}
