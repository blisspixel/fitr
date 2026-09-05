package eval

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/blisspixel/fitr/internal/ollama"
)

// This fixture executes the real evaluators and native HTTP client, forcing
// every Chat retry and every withdrawal turn. The transport has no model or
// network dependency. Its request count must exactly exhaust the reservation.
type envelopeWire struct {
	plan      RequestEnvelope
	requests  int64
	tokens    int64
	wire      int64
	chatWire  int
	plumbing  bool
	plumbStep int
}

func (wire *envelopeWire) admit(_ context.Context, request ollama.InferenceRequest) (ollama.InferencePermit, error) {
	wire.requests++
	wire.tokens += int64(request.MaxOutputTokens)
	if wire.requests > wire.plan.MaxRequests || wire.tokens > wire.plan.MaxRequestedOutputTokens {
		return ollama.InferencePermit{}, errors.New("planned reservation exhausted")
	}
	return ollama.InferencePermit{Deadline: time.Now().Add(time.Minute)}, nil
}

func (wire *envelopeWire) RoundTrip(request *http.Request) (*http.Response, error) {
	body, err := wire.responseBody(request)
	if err != nil {
		return nil, err
	}
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Request: request,
		Body: io.NopCloser(strings.NewReader(body))}, nil
}

func (wire *envelopeWire) responseBody(request *http.Request) (string, error) {
	switch request.URL.Path {
	case "/api/tags", "/api/ps":
		return `{"models":[]}`, nil
	case "/api/show":
		return `{"capabilities":["completion","tools"]}`, nil
	case "/api/generate", "/api/chat":
		wire.wire++
		if wire.wire != wire.requests || request.GetBody != nil {
			return "", errors.New("unadmitted or replayable inference request")
		}
	default:
		return "", errors.New("unexpected endpoint")
	}
	if request.URL.Path == "/api/generate" {
		return `{"response":"ok","done":true,"eval_count":1,"eval_duration":1000,"prompt_eval_count":10,"prompt_eval_duration":1000}`, nil
	}
	wire.chatWire++
	if wire.chatWire%ollama.ChatMaxAttempts != 0 {
		return `{"done":false}`, nil
	}
	message := callTo("list_files", `{}`)
	if wire.plumbing {
		message = wire.plumbingMessage()
	}
	data, err := json.Marshal(map[string]any{"done": true, "message": message, "eval_count": 1})
	return string(data), err
}

func (wire *envelopeWire) plumbingMessage() ollama.Message {
	wire.plumbStep++
	switch wire.plumbStep {
	case 1:
		return callTo("get_weather", `{"city":"Oslo"}`)
	case 2:
		return ollama.Message{Role: "assistant", Content: "-3 degrees Celsius"}
	default:
		return ollama.Message{Role: "assistant", Content: "Paris"}
	}
}

func TestRequestEnvelopeBoundsActualOllamaBatteryAndRetries(t *testing.T) {
	spec, options := envelopeSpec(t), fullEnvelopeOptions()
	plan, err := PlanRequestEnvelope(spec, options)
	if err != nil {
		t.Fatal(err)
	}
	wire := &envelopeWire{plan: plan}
	client := &ollama.Client{BaseURL: ollama.DefaultURL, HTTP: &http.Client{Transport: wire}, Admission: wire.admit}
	client.DisableRedirects()
	runEnvelopeStandard(t, client, spec, options)
	runEnvelopeChecks(t, client, spec, options)
	wire.plumbing = true
	plumbing, err := RunPlumbing(t.Context(), client, "candidate", spec.Plumbing)
	wire.plumbing = false
	if err != nil || !plumbing.Healthy {
		t.Fatalf("plumbing = %+v, error = %v", plumbing, err)
	}
	runEnvelopeTools(t, client, spec, options)
	if wire.requests != plan.MaxRequests || wire.wire != plan.MaxRequests || wire.tokens != plan.MaxRequestedOutputTokens {
		t.Fatalf("actual requests=%d wire=%d tokens=%d; planned requests=%d tokens=%d",
			wire.requests, wire.wire, wire.tokens, plan.MaxRequests, plan.MaxRequestedOutputTokens)
	}
}

func runEnvelopeStandard(t *testing.T, client *ollama.Client, spec *Spec, options RequestEnvelopeOptions) {
	t.Helper()
	if _, _, err := client.Generate(t.Context(), "candidate", "Reply with OK.", ollama.Deterministic(ContextProbeOutputTokens, 4096)); err != nil {
		t.Fatal(err)
	}
	for range options.Repeats {
		if _, err := RunSpeed(t.Context(), client, "candidate", spec, "fixture"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := RunMemory(t.Context(), client, "candidate", 4096); err != nil {
		t.Fatal(err)
	}
	for range options.Repeats {
		for _, coding := range []ExecSpec{spec.CodeWrite, spec.CodeFix} {
			result, err := RunExec(t.Context(), client, "candidate", coding, t.TempDir())
			if err != nil || result.Outcome != OutcomeSkipped {
				t.Fatalf("disabled coding = %+v, error = %v", result, err)
			}
		}
	}
}

func runEnvelopeChecks(t *testing.T, client *ollama.Client, spec *Spec, options RequestEnvelopeOptions) {
	t.Helper()
	for round := range options.CheckRepeats {
		for _, check := range spec.Checks {
			if _, err := RunCheck(t.Context(), client, "candidate", check, InstanceSeed("fixture", check.ID, round)); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func runEnvelopeTools(t *testing.T, client *ollama.Client, spec *Spec, options RequestEnvelopeOptions) {
	t.Helper()
	for range options.Repeats {
		result, err := RunToolLoop(t.Context(), client, "candidate", spec.Tools, t.TempDir())
		if err != nil || result.Outcome != OutcomeSkipped {
			t.Fatalf("disabled tool loop = %+v, error = %v", result, err)
		}
	}
	withdrawal, err := RunToolLoop(t.Context(), client, "candidate", spec.Withdrawal, t.TempDir())
	if err != nil || withdrawal.Turns != spec.Withdrawal.MaxTurns {
		t.Fatalf("withdrawal = %+v, error = %v", withdrawal, err)
	}
	if _, _, err := RunRefusal(t.Context(), client, "candidate", spec.Refusal); err != nil {
		t.Fatal(err)
	}
	agentic, err := RunToolLoop(t.Context(), client, "candidate", spec.Agentic, t.TempDir())
	if err != nil || agentic.Outcome != OutcomeSkipped {
		t.Fatalf("disabled agentic loop = %+v, error = %v", agentic, err)
	}
}
