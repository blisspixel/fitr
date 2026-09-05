package ollama

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

const observedGenerateReply = `{"done":true,"response":"ok","eval_count":4,"eval_duration":2000000000,"prompt_eval_count":2,"prompt_eval_duration":500000000,"load_duration":250000000}`

const observedChatReply = `{"done":true,"message":{"role":"assistant","content":"ok","thinking":"private reasoning","tool_calls":[{"function":{"name":"private_tool","arguments":{"secret":"private argument"}}}]},"eval_count":4,"eval_duration":2000000000,"prompt_eval_count":2,"prompt_eval_duration":500000000}`

func observationCall(ctx context.Context, c *Client, kind string) (Message, Metrics, error) {
	if kind == InferenceGenerate {
		text, metrics, err := c.Generate(ctx, "candidate", "private prompt", Deterministic(9, 4096))
		return Message{Content: text}, metrics, err
	}
	return c.Chat(ctx, "candidate", []Message{{Role: "user", Content: "private prompt"}}, nil, Deterministic(9, 4096))
}

func observationClient(body string) *Client {
	return &Client{BaseURL: DefaultURL, HTTP: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return inferenceResponse(req, body), nil
	})}}
}

func TestObservationDiscardsUnverifiedRepliesAndPreventsRetry(t *testing.T) {
	for _, tc := range []struct{ name, kind, body string }{
		{"generate", InferenceGenerate, observedGenerateReply},
		{"generate load", InferenceGenerate, `{"done":true,"done_reason":"load"}`},
		{"partial generate", InferenceGenerate, `{"response":"private partial output"}`},
		{"tool calls", InferenceChat, observedChatReply},
		{"empty nonterminal", InferenceChat, `{"done":false}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			cause := errors.Join(errors.New("placement changed"), errEmptyNonTerminal)
			c := observationClient(tc.body)
			c.ObserveInference = func(_ context.Context, attempt InferenceAttempt) error {
				calls++
				if attempt != (InferenceAttempt{Kind: tc.kind, Model: "candidate", NumCtx: 4096, MaxOutputTokens: 9}) {
					t.Fatalf("attempt = %+v", attempt)
				}
				return cause
			}
			message, metrics, err := observationCall(t.Context(), c, tc.kind)
			if !IsLocalityError(err) || !errors.Is(err, cause) || calls != 1 {
				t.Fatalf("calls=%d error=%v", calls, err)
			}
			if !reflect.DeepEqual(message, Message{}) || metrics != (Metrics{}) {
				t.Fatalf("unverified output escaped: message=%+v metrics=%+v", message, metrics)
			}
		})
	}
}

func TestObservationChecksEachEmptyChatRetry(t *testing.T) {
	for _, failSecond := range []bool{false, true} {
		t.Run(map[bool]string{false: "verified", true: "second rejected"}[failSecond], func(t *testing.T) {
			wire, observations := 0, 0
			c := &Client{BaseURL: DefaultURL, HTTP: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				wire++
				if observations != wire-1 || req.GetBody != nil {
					t.Fatal("retry dispatched before observation or with a replayable body")
				}
				body := `{"done":false,"done_reason":"load"}`
				if wire == ChatMaxAttempts {
					body = observedChatReply
				}
				return inferenceResponse(req, body), nil
			})}}
			c.ObserveInference = func(context.Context, InferenceAttempt) error {
				observations++
				if failSecond && observations == ChatMaxAttempts {
					return errors.New("second attempt reloaded on CPU")
				}
				return nil
			}
			message, metrics, err := observationCall(t.Context(), c, InferenceChat)
			if observations != ChatMaxAttempts || wire != ChatMaxAttempts || (failSecond != IsLocalityError(err)) {
				t.Fatalf("wire=%d observations=%d error=%v", wire, observations, err)
			}
			if failSecond && (!reflect.DeepEqual(message, Message{}) || metrics != (Metrics{})) {
				t.Fatal("unverified retry result escaped")
			}
			if !failSecond && (message.Content != "ok" || !metrics.InferenceElapsedKnown) {
				t.Fatalf("verified retry lost its result: %+v %+v", message, metrics)
			}
		})
	}
}

type observationBody struct {
	io.Reader
	closed *bool
}

func (b observationBody) Close() error { *b.closed = true; return nil }

func TestObservationTimingExcludesBothHooks(t *testing.T) {
	for _, kind := range []string{InferenceGenerate, InferenceChat} {
		t.Run(kind, func(t *testing.T) { checkObservationTiming(t, kind) })
	}
}

func checkObservationTiming(t *testing.T, kind string) {
	t.Helper()
	closed := false
	var admissionElapsed, observationElapsed time.Duration
	c := observationClient("")
	c.HTTP.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body := observedGenerateReply
		if kind == InferenceChat {
			body = observedChatReply
		}
		response := inferenceResponse(req, body)
		response.Body = observationBody{Reader: strings.NewReader(body), closed: &closed}
		return response, nil
	})
	c.Admission = func(context.Context, InferenceRequest) (InferencePermit, error) {
		started := time.Now()
		time.Sleep(30 * time.Millisecond)
		admissionElapsed = time.Since(started)
		return futureInferencePermit(), nil
	}
	c.ObserveInference = func(ctx context.Context, _ InferenceAttempt) error {
		if !closed {
			t.Fatal("response body remains open during observation")
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("observation lost admitted deadline")
		}
		started := time.Now()
		time.Sleep(60 * time.Millisecond)
		observationElapsed = time.Since(started)
		return nil
	}
	started := time.Now()
	_, metrics, err := observationCall(t.Context(), c, kind)
	elapsed := time.Since(started)
	if err != nil || !metrics.InferenceElapsedKnown || metrics.InferenceElapsed < 0 {
		t.Fatalf("metrics=%+v error=%v", metrics, err)
	}
	if metrics.InferenceElapsed > elapsed-admissionElapsed-observationElapsed {
		t.Fatalf("inference %v includes hook time in total %v", metrics.InferenceElapsed, elapsed)
	}
	if metrics.DecodeTPS != 2 || metrics.PrefillTPS != 4 || metrics.EvalCount != 4 || metrics.PromptTokens != 2 {
		t.Fatalf("native metrics changed: %+v", metrics)
	}
	if metrics.WallSeconds > metrics.InferenceElapsed.Seconds()+0.01 {
		t.Fatalf("native wall includes observation: %+v", metrics)
	}
}

func TestObservationCancellationDiscardsOutput(t *testing.T) {
	for _, kind := range []string{InferenceGenerate, InferenceChat} {
		for _, duringHook := range []bool{false, true} {
			t.Run(kind+map[bool]string{false: "/before", true: "/during"}[duringHook], func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				calls := 0
				c := observationClient(observedGenerateReply)
				c.HTTP.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
					if !duringHook {
						cancel()
					}
					body := observedGenerateReply
					if kind == InferenceChat {
						body = observedChatReply
					}
					return inferenceResponse(req, body), nil
				})
				c.ObserveInference = func(context.Context, InferenceAttempt) error { calls++; cancel(); return nil }
				message, metrics, err := observationCall(ctx, c, kind)
				if !IsLocalityError(err) || !errors.Is(err, context.Canceled) || metrics != (Metrics{}) || !reflect.DeepEqual(message, Message{}) {
					t.Fatalf("message=%+v metrics=%+v error=%v", message, metrics, err)
				}
				if calls != map[bool]int{false: 0, true: 1}[duringHook] {
					t.Fatalf("observation calls = %d", calls)
				}
			})
		}
	}
}

func TestObservationKeepsOrdinaryErrorsAndNilHookMetrics(t *testing.T) {
	for _, kind := range []string{InferenceGenerate, InferenceChat} {
		c := observationClient("malformed")
		calls := 0
		c.ObserveInference = func(context.Context, InferenceAttempt) error { calls++; return nil }
		_, metrics, err := observationCall(t.Context(), c, kind)
		if err == nil || IsLocalityError(err) || calls != 1 || metrics != (Metrics{}) {
			t.Fatalf("kind=%s calls=%d metrics=%+v error=%v", kind, calls, metrics, err)
		}
	}
	c := observationClient(observedGenerateReply)
	_, metrics, err := observationCall(t.Context(), c, InferenceGenerate)
	if err != nil || metrics.InferenceElapsedKnown || metrics.InferenceElapsed != 0 {
		t.Fatalf("ordinary client metrics=%+v error=%v", metrics, err)
	}
	metrics.InferenceElapsedKnown, metrics.InferenceElapsed = true, time.Second
	data, err := json.Marshal(metrics)
	if err != nil || strings.Contains(string(data), "Inference") || strings.Contains(string(data), "inference_") {
		t.Fatalf("transient client timing leaked to JSON: %s error=%v", data, err)
	}
}

func TestObservationControlPlaneAndAdmissionRejection(t *testing.T) {
	c := observationClient(`{"models":[]}`)
	c.ObserveInference = func(context.Context, InferenceAttempt) error { t.Fatal("control plane invoked observer"); return nil }
	if _, err := c.StopAll(t.Context()); err != nil {
		t.Fatal(err)
	}
	response, err := c.post(t.Context(), "/api/generate", map[string]any{"keep_alive": 0})
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	c.Admission = func(context.Context, InferenceRequest) (InferencePermit, error) {
		return InferencePermit{}, errors.New("budget exhausted")
	}
	for _, kind := range []string{InferenceGenerate, InferenceChat} {
		if _, _, err := observationCall(t.Context(), c, kind); !errors.Is(err, ErrInferenceAdmission) {
			t.Fatalf("error = %v", err)
		}
	}
}

func TestPSPreservesOptionalResidentDigest(t *testing.T) {
	c := observationClient(`{"models":[{"name":"candidate","digest":"sha256:exact-manifest","size":100,"size_vram":100}]}`)
	models, err := c.PS(t.Context())
	if err != nil || len(models) != 1 || models[0].Digest != "sha256:exact-manifest" {
		t.Fatalf("models=%+v error=%v", models, err)
	}
}
