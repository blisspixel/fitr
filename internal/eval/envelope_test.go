package eval

import (
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"testing"

	"github.com/blisspixel/fitr/internal/ollama"
)

func fullEnvelopeOptions() RequestEnvelopeOptions {
	return RequestEnvelopeOptions{Backend: "ollama", Level: "full", Repeats: 3, CheckRepeats: 3, ContextProbe: true}
}

func envelopeSpec(t *testing.T) *Spec {
	t.Helper()
	spec, err := LoadSpec()
	if err != nil {
		t.Fatal(err)
	}
	return spec
}

func TestRequestEnvelopeCurrentFullBattery(t *testing.T) {
	plan, err := PlanRequestEnvelope(envelopeSpec(t), fullEnvelopeOptions())
	if err != nil {
		t.Fatal(err)
	}
	if plan.MaxRequests != 124 || plan.MaxRequestedOutputTokens != 44941 {
		t.Fatalf("full reservation = %d requests / %d output", plan.MaxRequests, plan.MaxRequestedOutputTokens)
	}
	for _, phase := range plan.Phases {
		if phase.Phase == "tools" || phase.Phase == "agentic" || phase.Phase == "coding.write" || phase.Phase == "coding.fix" {
			t.Fatalf("execution-disabled phase reserves inference: %+v", phase)
		}
	}
	digest, err := plan.Digest()
	if err != nil || len(digest) != 71 {
		t.Fatalf("digest = %q, error = %v", digest, err)
	}
	data, _ := json.Marshal(plan)
	var decoded RequestEnvelope
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if restored, err := decoded.Digest(); err != nil || restored != digest {
		t.Fatalf("restored digest = %q, error = %v", restored, err)
	}
}

func TestRequestEnvelopeUsesSpecsAndActualCheckChannel(t *testing.T) {
	spec := envelopeSpec(t)
	spec.Checks = []CheckSpec{
		{ID: "plain-tool-args", Kind: "check", Need: "structured_output", Family: "tool_args", NumPredict: 17},
		{ID: "actual-tools", Kind: "check", Need: "tool_calling", Family: "tool_call", NumPredict: 19},
	}
	options := fullEnvelopeOptions()
	options.Level, options.ContextProbe, options.CheckRepeats = "checks", false, 2
	plan, err := PlanRequestEnvelope(spec, options)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Phases) != 2 || plan.Phases[0].Kind != ollama.InferenceGenerate || plan.Phases[1].Kind != ollama.InferenceChat {
		t.Fatalf("check channel reservations = %+v", plan.Phases)
	}
	if plan.MaxRequests != 6 || plan.MaxRequestedOutputTokens != 2*17+4*19 {
		t.Fatalf("changed-spec bounds = %+v", plan)
	}
	before, _ := plan.Digest()
	spec.Checks[1].NumPredict++
	changed, _ := PlanRequestEnvelope(spec, options)
	after, _ := changed.Digest()
	if before == after {
		t.Fatal("request cap change did not change envelope identity")
	}
}

func TestRequestEnvelopeLevelsAndExecutionGate(t *testing.T) {
	for _, level := range []string{"quick", "default", "full", "checks"} {
		t.Run(level, func(t *testing.T) {
			options := fullEnvelopeOptions()
			options.Level, options.AllowUnsafeExec = level, true
			plan, err := PlanRequestEnvelope(envelopeSpec(t), options)
			if err != nil {
				t.Fatal(err)
			}
			phases := map[string]bool{}
			for _, phase := range plan.Phases {
				phases[phase.Phase] = true
			}
			if phases["coding.write"] != (level != "checks") || phases["tools"] != (level != "checks") ||
				phases["agentic"] != (level == "full") || phases["withdrawal"] != (level == "default" || level == "full") {
				t.Fatalf("wrong level/execution schedule: %v", phases)
			}
		})
	}
}

func TestRequestEnvelopeRejectsUnboundedOrUnsupportedPlans(t *testing.T) {
	cases := map[string]func(*Spec, *RequestEnvelopeOptions){
		"backend":           func(_ *Spec, o *RequestEnvelopeOptions) { o.Backend = "llama-server" },
		"level":             func(_ *Spec, o *RequestEnvelopeOptions) { o.Level = "unknown" },
		"repeats":           func(_ *Spec, o *RequestEnvelopeOptions) { o.Repeats = 0 },
		"check repeats":     func(_ *Spec, o *RequestEnvelopeOptions) { o.CheckRepeats = -1 },
		"speed cap":         func(s *Spec, _ *RequestEnvelopeOptions) { s.Speed.Decode.NumPredict = 0 },
		"turns":             func(s *Spec, _ *RequestEnvelopeOptions) { s.Withdrawal.MaxTurns = 0 },
		"duplicate check":   func(s *Spec, _ *RequestEnvelopeOptions) { s.Checks = append(s.Checks, s.Checks[0]) },
		"unknown generator": func(s *Spec, _ *RequestEnvelopeOptions) { s.Checks[0].Family = "unknown" },
		"too many checks":   func(s *Spec, _ *RequestEnvelopeOptions) { s.Checks = make([]CheckSpec, maxEnvelopePhases) },
		"output overflow": func(s *Spec, o *RequestEnvelopeOptions) {
			s.Speed.Decode.NumPredict = math.MaxInt
			o.Repeats = math.MaxInt
		},
		"loop overflow": func(s *Spec, o *RequestEnvelopeOptions) {
			o.AllowUnsafeExec, o.Repeats = true, math.MaxInt
			s.Tools.MaxTurns = math.MaxInt
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			spec, options := envelopeSpec(t), fullEnvelopeOptions()
			mutate(spec, &options)
			if _, err := PlanRequestEnvelope(spec, options); err == nil {
				t.Fatal("invalid request plan accepted")
			}
		})
	}
	if _, err := PlanRequestEnvelope(nil, fullEnvelopeOptions()); err == nil {
		t.Fatal("nil spec accepted")
	}
}

func TestRequestEnvelopeValidationDetectsAlteredReceipts(t *testing.T) {
	cases := map[string]func(*RequestEnvelope){
		"schema":      func(p *RequestEnvelope) { p.Schema = "other" },
		"options":     func(p *RequestEnvelope) { p.Options.Backend = "other" },
		"empty phase": func(p *RequestEnvelope) { p.Phases[0].Phase = "" },
		"kind":        func(p *RequestEnvelope) { p.Phases[0].Kind = "other" },
		"attempts":    func(p *RequestEnvelope) { p.Phases[0].AttemptsPerCall++ },
		"calls":       func(p *RequestEnvelope) { p.Phases[0].Calls = 0 },
		"phase total": func(p *RequestEnvelope) { p.Phases[0].MaxRequests++ },
		"aggregate":   func(p *RequestEnvelope) { p.MaxRequestedOutputTokens++ },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			plan, err := PlanRequestEnvelope(envelopeSpec(t), fullEnvelopeOptions())
			if err != nil {
				t.Fatal(err)
			}
			mutate(&plan)
			before := append([]PhaseRequestEnvelope(nil), plan.Phases...)
			if _, err := plan.Digest(); err == nil {
				t.Fatal("tampered envelope was digested")
			}
			if !reflect.DeepEqual(before, plan.Phases) {
				t.Fatal("validation changed caller-owned phases")
			}
		})
	}
}

func TestAdmissionErrorsCannotBecomeToolCapabilitySkips(t *testing.T) {
	err := errors.Join(ollama.ErrInferenceAdmission, errors.New("no tool support in this allowance"))
	if declinesTools(err) {
		t.Fatal("admission error became a capability fact")
	}
	spec := envelopeSpec(t)
	for _, check := range spec.Checks {
		if Generate(check, 0).UsesToolChannel() {
			backend := &fakeBackend{chatErrAt: map[int]error{1: err}}
			_, got := RunCheck(t.Context(), backend, "candidate", check, 0)
			if !errors.Is(got, ollama.ErrInferenceAdmission) {
				t.Fatalf("RunCheck discarded admission failure: %v", got)
			}
			return
		}
	}
	t.Fatal("missing tool-channel fixture")
}
