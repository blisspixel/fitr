package ollama

import (
	"errors"
	"net/http"
	"strconv"
	"testing"
)

// The runner's payload is the shape llama.cpp's own server test asserts:
// status 400, type exceed_context_size_error, n_prompt_tokens and a per-slot
// n_ctx. Ollama does not interpret it. It copies the runner's body verbatim
// into its own error envelope, so the JSON arrives nested inside a string.
const runnerOverflowBody = `{"error":{"code":400,` +
	`"message":"the request exceeds the available context size, try increasing it",` +
	`"n_prompt_tokens":5000,"n_ctx":4096,"type":"exceed_context_size_error"}}`

func TestContextOverflowIsReadThroughOllamasErrorEnvelope(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
	}{
		{name: "nested in the envelope", body: `{"error":` + strconv.Quote(runnerOverflowBody) + `}`},
		{name: "runner body unwrapped", body: runnerOverflowBody},
		{
			// statusErrorMessage appends the runner's status line on an
			// out-of-memory condition, after the JSON it already copied.
			name: "with an appended runner status line",
			body: `{"error":` + strconv.Quote(runnerOverflowBody+"\nggml_backend_alloc failed") + `}`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			overflow, ok := parseContextOverflow(http.StatusBadRequest, test.body)
			if !ok {
				t.Fatal("the runtime's own overflow refusal was not recognized")
			}
			if overflow.NCtx != 4096 || overflow.PromptTokens != 5000 {
				t.Fatalf("overflow = %+v, want the reported window and prompt size", overflow)
			}
			if !errors.Is(overflow, ErrContextOverflow) {
				t.Fatal("overflow must match its sentinel so callers can classify it")
			}
		})
	}
}

// Anything that is not exactly this refusal stays an ordinary transport error.
// A changed upstream shape must degrade to the previous behavior rather than
// produce a window fitr never observed.
func TestOnlyTheExactOverflowRefusalIsRecognized(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		body   string
	}{
		{name: "another 400", status: 400, body: `{"error":"model not found"}`},
		{name: "not a 400", status: 500, body: runnerOverflowBody},
		{
			name: "another error type", status: 400,
			body: `{"error":{"type":"server_error","n_ctx":4096,"n_prompt_tokens":5000}}`,
		},
		{
			name: "missing the window", status: 400,
			body: `{"error":{"type":"exceed_context_size_error","n_prompt_tokens":5000}}`,
		},
		{
			name: "missing the prompt size", status: 400,
			body: `{"error":{"type":"exceed_context_size_error","n_ctx":4096}}`,
		},
		{
			name: "unbelievable window", status: 400,
			body: `{"error":{"type":"exceed_context_size_error","n_ctx":0,"n_prompt_tokens":5000}}`,
		},
		{name: "not json", status: 400, body: "the request exceeds the available context size"},
		{name: "empty", status: 400, body: ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, ok := parseContextOverflow(test.status, test.body); ok {
				t.Fatalf("%q was read as a measured overflow refusal", test.body)
			}
		})
	}
}
