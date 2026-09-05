package ollama

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func contextPolicyCall(ctx context.Context, c *Client, kind string, s Sampling) (string, Metrics, error) {
	if kind == InferenceGenerate {
		return c.Generate(ctx, "candidate", "document", s)
	}
	m, metrics, err := c.Chat(ctx, "candidate", []Message{{Role: "user", Content: "document"}}, nil, s)
	return m.Content, metrics, err
}

func contextReply(fields string) string {
	return `{"done":true,"response":"ok","message":{"role":"assistant","content":"ok"}` + fields + `}`
}

func TestContextPolicyIsExplicitAndLegacyMetricsStayUnchanged(t *testing.T) {
	for _, kind := range []string{InferenceGenerate, InferenceChat} {
		for _, policy := range []ContextRequestPolicy{"", PreserveContextV1} {
			t.Run(kind+"/"+string(policy), func(t *testing.T) {
				checkContextLegacyMetrics(t, kind, policy)
			})
		}
	}
}

func checkContextLegacyMetrics(t *testing.T, kind string, policy ContextRequestPolicy) {
	t.Helper()
	c := &Client{BaseURL: DefaultURL, ContextPolicy: policy, HTTP: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		checkContextRequestPolicy(t, req, policy)
		return inferenceResponse(req, contextReply(`,"prompt_eval_count":100,"prompt_eval_cached_count":40,"eval_count":3`)), nil
	})}}
	output, metrics, err := contextPolicyCall(t.Context(), c, kind, Deterministic(128, 8192))
	if err != nil || output != "ok" || metrics.PromptTokens != 100 || metrics.EvalCount != 3 || metrics.CacheKnown || metrics.CachedTokens != 0 {
		t.Fatalf("output=%q metrics=%+v error=%v", output, metrics, err)
	}
	if policy == "" {
		if metrics.ContextAccounting != nil {
			t.Fatal("legacy client acquired context receipt")
		}
		return
	}
	if err := metrics.ContextAccounting.CheckReserve(8192, 128); err != nil {
		t.Fatal(err)
	}
	if n, err := metrics.ContextAccounting.NewlyEvaluatedPromptTokens(128); err != nil || n != 60 {
		t.Fatalf("new prompt count = %d, %v", n, err)
	}
	withReceipt, err := json.Marshal(metrics)
	if err != nil {
		t.Fatal(err)
	}
	metrics.ContextAccounting = nil
	withoutReceipt, err := json.Marshal(metrics)
	if err != nil || string(withReceipt) != string(withoutReceipt) {
		t.Fatalf("transient accounting changed persisted metrics: %s / %s (%v)", withReceipt, withoutReceipt, err)
	}
}

func checkContextRequestPolicy(t *testing.T, req *http.Request, policy ContextRequestPolicy) {
	t.Helper()
	b, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(b, &payload); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"truncate", "shift"} {
		v, exists := payload[key]
		if exists != (policy != "") || (exists && v != false) {
			t.Fatalf("%s control = %#v, present=%v", key, v, exists)
		}
	}
	if policy != "" && req.GetBody != nil {
		t.Fatal("context request permits hidden HTTP replay")
	}
}

func TestInvalidContextPolicyStopsBeforeAdmission(t *testing.T) {
	for _, kind := range []string{InferenceGenerate, InferenceChat} {
		for _, tc := range []struct {
			policy      ContextRequestPolicy
			window, cap int
		}{
			{"unsupported", 8192, 128}, {PreserveContextV1, 0, 128},
			{PreserveContextV1, 8192, 0}, {PreserveContextV1, 128, 128},
			{PreserveContextV1, -1, 128}, {PreserveContextV1, 128, 129},
		} {
			c := &Client{ContextPolicy: tc.policy, Admission: func(context.Context, InferenceRequest) (InferencePermit, error) {
				t.Fatal("invalid policy consumed a reservation")
				return InferencePermit{}, nil
			}}
			_, _, err := contextPolicyCall(t.Context(), c, kind, Deterministic(tc.cap, tc.window))
			if !errors.Is(err, ErrContextRequestPolicy) {
				t.Fatalf("kind=%s policy=%+v error=%v", kind, tc, err)
			}
		}
	}
}

func TestContextAccountingUnknownIsNeverAZeroReceipt(t *testing.T) {
	for _, kind := range []string{InferenceGenerate, InferenceChat} {
		for _, fields := range []string{
			``, `,"prompt_eval_count":null,"prompt_eval_cached_count":null,"eval_count":null`,
			`,"prompt_eval_count":20,"eval_count":1`,
			`,"prompt_eval_cached_count":0,"eval_count":1`,
			`,"prompt_eval_count":20,"prompt_eval_cached_count":0`,
		} {
			c := observationClient(contextReply(fields))
			c.ContextPolicy = PreserveContextV1
			_, m, err := contextPolicyCall(t.Context(), c, kind, Deterministic(128, 8192))
			if err != nil || m.ContextAccounting == nil {
				t.Fatalf("missing accounting was not preserved: %+v, %v", m, err)
			}
			if err := m.ContextAccounting.CheckReserve(8192, 128); !errors.Is(err, ErrContextAccounting) {
				t.Fatalf("unknown accounting qualified: %v", err)
			}
		}
	}
}

func TestContextAccountingRejectsImpossibleCountsBeforeOutput(t *testing.T) {
	for _, kind := range []string{InferenceGenerate, InferenceChat} {
		for _, fields := range []string{
			`,"prompt_eval_count":20,"prompt_eval_cached_count":21,"eval_count":1`,
			`,"prompt_eval_count":20,"prompt_eval_cached_count":-1,"eval_count":1`,
			`,"prompt_eval_count":20,"prompt_eval_cached_count":"0","eval_count":1`,
			`,"prompt_eval_count":20,"prompt_eval_cached_count":{},"eval_count":1`,
			`,"prompt_eval_count":20,"prompt_eval_cached_count":0,"eval_count":129`,
		} {
			c := observationClient(contextReply(fields))
			c.ContextPolicy = PreserveContextV1
			observed := 0
			c.ObserveInference = func(context.Context, InferenceAttempt) error { observed++; return nil }
			output, m, err := contextPolicyCall(t.Context(), c, kind, Deterministic(128, 8192))
			if !errors.Is(err, ErrContextAccounting) || output != "" || m != (Metrics{}) || observed != 1 {
				t.Fatalf("kind=%s output=%q metrics=%+v observed=%d error=%v", kind, output, m, observed, err)
			}
		}
	}
}

func TestContextReserveUsesFullReserveAndAvoidsOverflow(t *testing.T) {
	for _, tc := range []struct {
		prompt, cached, output int
		want                   error
	}{
		{8064, 0, 1, nil}, {8065, 100, 1, ErrContextReserve},
		{math.MaxInt, 0, 1, ErrContextReserve}, {0, 0, 0, nil},
		{-1, 0, 1, ErrContextAccounting}, {10, 11, 1, ErrContextAccounting},
		{10, 0, -1, ErrContextAccounting}, {10, 0, 129, ErrContextAccounting},
	} {
		a := &ContextTokenAccounting{&tc.prompt, &tc.cached, &tc.output}
		if err := a.CheckReserve(8192, 128); !errors.Is(err, tc.want) {
			t.Fatalf("receipt=%+v error=%v want=%v", tc, err, tc.want)
		}
	}
	var absent *ContextTokenAccounting
	if err := absent.CheckReserve(8192, 128); !errors.Is(err, ErrContextAccounting) {
		t.Fatal(err)
	}
	if err := absent.CheckReserve(128, 128); !errors.Is(err, ErrContextRequestPolicy) {
		t.Fatal(err)
	}
	if _, err := absent.NewlyEvaluatedPromptTokens(128); !errors.Is(err, ErrContextAccounting) {
		t.Fatal(err)
	}
}

func TestContextChatRetryNeedsExplicitZeroAccounting(t *testing.T) {
	for _, first := range []string{
		`{"done":false}`, `{"done":false,"eval_count":null,"prompt_eval_count":null}`,
		`{"done":false,"eval_count":0,"prompt_eval_count":0,"prompt_eval_cached_count":-1}`,
		`{"done":false,"eval_count":0,"prompt_eval_count":0,"prompt_eval_cached_count":1}`,
		`{"done":false,"eval_count":0,"prompt_eval_count":0,"prompt_eval_cached_count":0}`,
	} {
		calls, reservations, observations := 0, 0, 0
		c := &Client{BaseURL: DefaultURL, ContextPolicy: PreserveContextV1,
			Admission: func(context.Context, InferenceRequest) (InferencePermit, error) {
				reservations++
				return futureInferencePermit(), nil
			},
			ObserveInference: func(context.Context, InferenceAttempt) error { observations++; return nil },
			HTTP: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				b, err := io.ReadAll(req.Body)
				if err != nil || !strings.Contains(string(b), `"shift":false`) || !strings.Contains(string(b), `"truncate":false`) {
					t.Fatalf("retry lost policy: %s (%v)", b, err)
				}
				if calls == 1 {
					return inferenceResponse(req, first), nil
				}
				return inferenceResponse(req, contextReply(`,"eval_count":1,"prompt_eval_count":20,"prompt_eval_cached_count":0`)), nil
			})}}
		_, _, err := contextPolicyCall(t.Context(), c, InferenceChat, Deterministic(128, 8192))
		wantCalls := 1
		if strings.HasSuffix(first, `"prompt_eval_cached_count":0}`) {
			wantCalls = 2
		}
		if calls != wantCalls || reservations != calls || observations != calls || (err == nil) != (wantCalls == 2) {
			t.Fatalf("wire=%d reserved=%d observed=%d error=%v", calls, reservations, observations, err)
		}
	}
}

func TestContextStreamRejectsInvalidIntermediateAccounting(t *testing.T) {
	c := observationClient("{\"response\":\"partial\"}\n{\"prompt_eval_cached_count\":-1}\n" + contextReply(`,"eval_count":1,"prompt_eval_count":20,"prompt_eval_cached_count":0`))
	c.ContextPolicy = PreserveContextV1
	output, m, err := contextPolicyCall(t.Context(), c, InferenceGenerate, Deterministic(128, 8192))
	if !errors.Is(err, ErrContextAccounting) || output != "" || m != (Metrics{}) {
		t.Fatalf("invalid frame escaped: output=%q metrics=%+v error=%v", output, m, err)
	}
}

func TestContextReceiptDoesNotEscapeFailedObservation(t *testing.T) {
	for _, kind := range []string{InferenceGenerate, InferenceChat} {
		ctx, cancel := context.WithCancel(t.Context())
		c := observationClient(contextReply(`,"eval_count":1,"prompt_eval_count":20,"prompt_eval_cached_count":0`))
		c.ContextPolicy = PreserveContextV1
		c.ObserveInference = func(context.Context, InferenceAttempt) error { cancel(); return nil }
		output, m, err := contextPolicyCall(ctx, c, kind, Deterministic(128, 8192))
		if !IsLocalityError(err) || !errors.Is(err, context.Canceled) || output != "" || !reflect.DeepEqual(m, Metrics{}) {
			t.Fatalf("cancelled receipt escaped: output=%q metrics=%+v error=%v", output, m, err)
		}
	}
}

func TestContextMetricAliasesCannotReplaceReceiptFields(t *testing.T) {
	for _, kind := range []string{InferenceGenerate, InferenceChat} {
		for _, fields := range []string{
			`,"prompt_eval_count":999999,"PROMPT_EVAL_COUNT":1,"prompt_eval_cached_count":0,"eval_count":1`,
			`,"prompt_eval_count":20,"prompt_eval_cached_count":-1,"PROMPT_EVAL_CACHED_COUNT":0,"eval_count":1`,
			`,"prompt_eval_count":20,"prompt_eval_cached_count":0,"eval_count":999,"EVAL_COUNT":1`,
			`,"prompt_eval_count":20,"prompt_eval_cached_count":0,"eval_count":1,"DONE":true`,
			`,"prompt_eval_count":20,"prompt_eval_cached_count":0,"eval_count":1,"Done_Reason":"stop"`,
			`,"prompt_eval_count":20,"prompt_eval_cached_count":0,"eval_count":1,"\u0050ROMPT_EVAL_COUNT":1`,
		} {
			c := observationClient(contextReply(fields))
			c.ContextPolicy = PreserveContextV1
			output, m, err := contextPolicyCall(t.Context(), c, kind, Deterministic(128, 8192))
			if !errors.Is(err, ErrContextAccounting) || output != "" || m != (Metrics{}) {
				t.Fatalf("alias accepted: kind=%s output=%q metrics=%+v error=%v", kind, output, m, err)
			}
		}
	}
}

type contextCancelBody struct {
	io.Reader
	cancel context.CancelFunc
}

func (b contextCancelBody) Close() error {
	b.cancel()
	return nil
}

func TestContextPolicyAloneChecksCancellationAfterClose(t *testing.T) {
	for _, kind := range []string{InferenceGenerate, InferenceChat} {
		for _, admission := range []bool{false, true} {
			ctx, cancel := context.WithCancel(t.Context())
			c := &Client{BaseURL: DefaultURL, ContextPolicy: PreserveContextV1, HTTP: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				resp := inferenceResponse(req, "")
				resp.Body = contextCancelBody{strings.NewReader(contextReply(`,"eval_count":1,"prompt_eval_count":20,"prompt_eval_cached_count":0`)), cancel}
				return resp, nil
			})}}
			if admission {
				c.Admission = func(context.Context, InferenceRequest) (InferencePermit, error) { return futureInferencePermit(), nil }
			}
			output, m, err := contextPolicyCall(ctx, c, kind, Deterministic(128, 8192))
			if !errors.Is(err, context.Canceled) || output != "" || m != (Metrics{}) {
				t.Fatalf("late cancellation accepted: kind=%s output=%q metrics=%+v error=%v", kind, output, m, err)
			}
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		c := &Client{ContextPolicy: PreserveContextV1}
		if _, _, err := contextPolicyCall(ctx, c, kind, Deterministic(128, 8192)); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled context reached transport: %v", err)
		}
	}
}

func TestContextStreamCannotReturnPartialOutputAfterInvalidReceipt(t *testing.T) {
	for _, tail := range []string{
		`{"done":true,"eval_count":-1}`, `{"done":true,"prompt_eval_count":-1}`,
		`{"done":true,"eval_count":1,"eval_count":2}`, `{"done":true,"eval_count":"1"}`, "",
	} {
		c := observationClient("{\"response\":\"partial\"}\n" + tail)
		c.ContextPolicy = PreserveContextV1
		output, m, err := contextPolicyCall(t.Context(), c, InferenceGenerate, Deterministic(128, 8192))
		if err == nil || output != "" || m != (Metrics{}) {
			t.Fatalf("invalid partial receipt accepted: output=%q metrics=%+v error=%v", output, m, err)
		}
	}
}
