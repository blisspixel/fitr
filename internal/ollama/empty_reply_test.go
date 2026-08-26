package ollama

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func chatServer(t *testing.T, bodies ...string) (*Client, *int) {
	t.Helper()
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := calls
		calls++
		if i >= len(bodies) {
			i = len(bodies) - 1
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(bodies[i]))
	}))
	t.Cleanup(srv.Close)
	return &Client{BaseURL: srv.URL, HTTP: srv.Client()}, &calls
}

const emptyNonTerminal = `{"model":"m","message":{"role":"assistant","content":"",` +
	`"tool_calls":[{"function":{"name":"a","arguments":{}}}]},"done":false,` +
	`"eval_count":0,"prompt_eval_count":0}`

const goodReply = `{"model":"m","message":{"role":"assistant","content":"ok"},` +
	`"done":true,"done_reason":"stop","eval_count":5,"eval_duration":1000000,` +
	`"prompt_eval_count":10,"prompt_eval_duration":1000000}`

// Observed in the wild three times, each costing a battery that had already
// run for minutes: a reply that is not finished and evaluated no tokens, yet
// carries tool calls. Zero tokens means nothing was measured, so asking again
// risks nothing.
func TestEmptyNonTerminalReplyIsRetriedOnce(t *testing.T) {
	c, calls := chatServer(t, emptyNonTerminal, goodReply)
	msg, m, err := c.Chat(context.Background(), "m", []Message{{Role: "user", Content: "hi"}}, nil, Deterministic(64, 4096))
	if err != nil {
		t.Fatalf("a retryable empty reply was not retried: %v", err)
	}
	if *calls != 2 {
		t.Fatalf("made %d requests, want exactly 2", *calls)
	}
	if msg.Content != "ok" || m.EvalCount != 5 {
		t.Fatalf("returned the wrong reply: %+v %+v", msg, m)
	}
}

// One retry, not a loop. A server stuck in this state must surface, not spin.
func TestEmptyNonTerminalReplyIsNotRetriedForever(t *testing.T) {
	c, calls := chatServer(t, emptyNonTerminal)
	_, _, err := c.Chat(context.Background(), "m", []Message{{Role: "user", Content: "hi"}}, nil, Deterministic(64, 4096))
	if err == nil {
		t.Fatal("a persistently empty server produced a result")
	}
	if *calls != 2 {
		t.Fatalf("made %d requests, want exactly 2", *calls)
	}
	if !strings.Contains(err.Error(), "eval=0") {
		t.Fatalf("diagnostic does not show the token counts: %v", err)
	}
}

// The retry is for a reply that measured nothing. A partial generation DID
// measure something, and reusing or re-requesting around it would hide a real
// fault, so it must stop the run on the first attempt.
func TestPartialGenerationIsNotRetried(t *testing.T) {
	partial := `{"model":"m","message":{"role":"assistant","content":"half an ans"},` +
		`"done":false,"eval_count":7,"prompt_eval_count":10}`
	c, calls := chatServer(t, partial, goodReply)
	_, _, err := c.Chat(context.Background(), "m", []Message{{Role: "user", Content: "hi"}}, nil, Deterministic(64, 4096))
	if err == nil {
		t.Fatal("a partial generation was accepted")
	}
	if *calls != 1 {
		t.Fatalf("made %d requests; a real fault must not be retried", *calls)
	}
	if !strings.Contains(err.Error(), "missing the terminal receipt") {
		t.Fatalf("unexpected diagnostic: %v", err)
	}
}

// A healthy reply is untouched.
func TestNormalReplyMakesOneRequest(t *testing.T) {
	c, calls := chatServer(t, goodReply)
	if _, _, err := c.Chat(context.Background(), "m", []Message{{Role: "user", Content: "hi"}}, nil, Deterministic(64, 4096)); err != nil {
		t.Fatal(err)
	}
	if *calls != 1 {
		t.Fatalf("made %d requests for a healthy reply", *calls)
	}
	var probe map[string]any
	if json.Unmarshal([]byte(goodReply), &probe) != nil {
		t.Fatal("fixture is not valid JSON")
	}
}
