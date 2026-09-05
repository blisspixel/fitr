package ollama

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func futureInferencePermit() InferencePermit {
	return InferencePermit{Deadline: time.Now().Add(time.Minute)}
}

func inferenceResponse(request *http.Request, body string) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header),
		Body: io.NopCloser(strings.NewReader(body)), Request: request}
}

func TestInferenceAdmissionRejectsBeforeDispatch(t *testing.T) {
	for _, kind := range []string{InferenceGenerate, InferenceChat} {
		for _, scenario := range []string{"zero cap", "negative cap", "cancelled", "denied", "no deadline", "expired"} {
			t.Run(kind+"/"+scenario, func(t *testing.T) { checkAdmissionRejection(t, kind, scenario) })
		}
	}
}

func checkAdmissionRejection(t *testing.T, kind, scenario string) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	output, hooks := 8, 0
	switch scenario {
	case "zero cap":
		output = 0
	case "negative cap":
		output = -1
	case "cancelled":
		cancel()
	}
	c := &Client{BaseURL: DefaultURL, HTTP: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("rejected inference reached HTTP transport")
		return nil, errors.New("unexpected dispatch")
	})}, Admission: func(context.Context, InferenceRequest) (InferencePermit, error) {
		hooks++
		switch scenario {
		case "denied":
			return InferencePermit{}, errors.New("budget exhausted")
		case "no deadline":
			return InferencePermit{}, nil
		case "expired":
			return InferencePermit{Deadline: time.Now().Add(-time.Second)}, nil
		}
		return futureInferencePermit(), nil
	}}
	err := callAdmissionKind(ctx, c, kind, output)
	if !errors.Is(err, ErrInferenceAdmission) {
		t.Fatalf("error = %v, want admission sentinel", err)
	}
	wantHooks := 1
	if output <= 0 || scenario == "cancelled" {
		wantHooks = 0
	}
	if hooks != wantHooks {
		t.Fatalf("durable reservations = %d, want %d", hooks, wantHooks)
	}
}

func callAdmissionKind(ctx context.Context, client *Client, kind string, output int) error {
	if kind == InferenceGenerate {
		_, _, err := client.Generate(ctx, "candidate", "private prompt", Deterministic(output, 4096))
		return err
	}
	_, _, err := client.Chat(ctx, "candidate", nil, nil, Deterministic(output, 4096))
	return err
}

func TestChatRetryNeedsAnotherReservation(t *testing.T) {
	for _, denyRetry := range []bool{false, true} {
		t.Run(map[bool]string{false: "admitted", true: "denied"}[denyRetry], func(t *testing.T) {
			checkChatRetryAdmission(t, denyRetry)
		})
	}
}

func checkChatRetryAdmission(t *testing.T, denyRetry bool) {
	t.Helper()
	hooks, wire := 0, 0
	c := &Client{BaseURL: DefaultURL, HTTP: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		wire++
		if hooks != wire || req.GetBody != nil {
			t.Fatalf("wire attempt %d lacks a unique non-replayable reservation (%d)", wire, hooks)
		}
		body := `{"done":false}`
		if wire == ChatMaxAttempts {
			body = `{"done":true,"message":{"role":"assistant","content":"ok"}}`
		}
		return inferenceResponse(req, body), nil
	})}, Admission: func(_ context.Context, request InferenceRequest) (InferencePermit, error) {
		hooks++
		if request != (InferenceRequest{Kind: InferenceChat, Model: "candidate", MaxOutputTokens: 9}) {
			t.Fatalf("admission facts = %+v", request)
		}
		if denyRetry && hooks == ChatMaxAttempts {
			return InferencePermit{}, errors.New("retry budget exhausted")
		}
		return futureInferencePermit(), nil
	}}
	err := callAdmissionKind(t.Context(), c, InferenceChat, 9)
	wantWire := ChatMaxAttempts
	if denyRetry {
		wantWire--
	}
	if hooks != ChatMaxAttempts || wire != wantWire || (denyRetry && !errors.Is(err, ErrInferenceAdmission)) || (!denyRetry && err != nil) {
		t.Fatalf("hooks=%d wire=%d error=%v", hooks, wire, err)
	}
}

func TestAdmissionPreservesCallerDeadlineAndCancellation(t *testing.T) {
	for _, cancelInHook := range []bool{false, true} {
		t.Run(map[bool]string{false: "earlier caller deadline", true: "cancelled after reservation"}[cancelInHook], func(t *testing.T) {
			deadline := time.Now().Add(30 * time.Second)
			ctx, cancel := context.WithDeadline(t.Context(), deadline)
			defer cancel()
			wire := 0
			c := &Client{BaseURL: DefaultURL, HTTP: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				wire++
				if actual, ok := req.Context().Deadline(); !ok || !actual.Equal(deadline) {
					t.Errorf("caller deadline was extended: %v", actual)
				}
				return inferenceResponse(req, `{"done":true,"response":"ok"}`), nil
			})}, Admission: func(context.Context, InferenceRequest) (InferencePermit, error) {
				if cancelInHook {
					cancel()
				}
				return futureInferencePermit(), nil
			}}
			err := callAdmissionKind(ctx, c, InferenceGenerate, 8)
			if cancelInHook {
				if wire != 0 || !errors.Is(err, context.Canceled) || !errors.Is(err, ErrInferenceAdmission) {
					t.Fatalf("cancelled reservation wire=%d error=%v", wire, err)
				}
			} else if wire != 1 || err != nil {
				t.Fatalf("admitted request wire=%d error=%v", wire, err)
			}
		})
	}
}

func TestAdmissionDeadlineCapsTransportContext(t *testing.T) {
	deadline := time.Now().Add(time.Minute)
	c := &Client{BaseURL: DefaultURL, HTTP: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if actual, ok := req.Context().Deadline(); !ok || !actual.Equal(deadline) {
			t.Fatalf("permit deadline = %v, want %v", actual, deadline)
		}
		if req.GetBody != nil {
			t.Fatal("admitted inference remains replayable")
		}
		return inferenceResponse(req, `{"done":true,"response":"ok"}`), nil
	})}, Admission: func(context.Context, InferenceRequest) (InferencePermit, error) {
		return InferencePermit{Deadline: deadline}, nil
	}}
	if err := callAdmissionKind(t.Context(), c, InferenceGenerate, 8); err != nil {
		t.Fatal(err)
	}
}

func TestNoAdmissionPreservesExistingSamplingBehavior(t *testing.T) {
	c := &Client{BaseURL: DefaultURL, HTTP: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.GetBody == nil {
			t.Fatal("ordinary client replay policy changed")
		}
		return inferenceResponse(req, `{"done":true,"response":"ok"}`), nil
	})}}
	if err := callAdmissionKind(t.Context(), c, InferenceGenerate, 0); err != nil {
		t.Fatal(err)
	}
}

func TestDisableRedirectsDoesNotDispatchOrMutateSharedClient(t *testing.T) {
	targetCalls := 0
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { targetCalls++ }))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer origin.Close()
	shared := origin.Client()
	c := &Client{BaseURL: origin.URL, HTTP: shared}
	c.DisableRedirects()
	if c.HTTP == shared || shared.CheckRedirect != nil {
		t.Fatal("redirect policy mutated the shared HTTP client")
	}
	if err := callAdmissionKind(t.Context(), c, InferenceGenerate, 8); err == nil || targetCalls != 0 {
		t.Fatalf("redirect target calls=%d error=%v", targetCalls, err)
	}
}
