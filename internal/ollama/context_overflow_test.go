package ollama

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
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

// Anything that is not one of the two exact receipts stays an ordinary
// transport error. A changed upstream shape must degrade to the previous
// behavior rather than produce a window fitr never observed.
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

// Ollama 0.40.4 returned this sentence, and only this sentence, for an
// oversized generate with truncate and shift disabled. The integers are the
// runner's prompt-token count and per-slot window. Prose that lacks them is
// covered by TestOnlyTheExactOverflowRefusalIsRecognized and must stay closed.
const ollamaOverflowSentence = "request (8023 tokens) exceeds the available context size (2048 tokens), try increasing it"

func TestOllamaSentenceOverflowCarriesBothCounts(t *testing.T) {
	for _, test := range []struct {
		name         string
		body         string
		prompt, nctx int
	}{
		{
			name: "ollama envelope", prompt: 8023, nctx: 2048,
			body: `{"error":` + strconv.Quote(ollamaOverflowSentence) + `}`,
		},
		{name: "bare sentence", body: ollamaOverflowSentence, prompt: 8023, nctx: 2048},
		{
			name: "envelope plus a runner status line", prompt: 8023, nctx: 2048,
			body: `{"error":` + strconv.Quote(ollamaOverflowSentence+"\nggml_backend_alloc failed") + `}`,
		},
		{
			name: "bare sentence plus a runner status line", prompt: 8023, nctx: 2048,
			body: ollamaOverflowSentence + "\nggml_backend_alloc failed",
		},
		{
			name: "both integers are read", prompt: 5000, nctx: 4096,
			body: "request (5000 tokens) exceeds the available context size (4096 tokens), try increasing it",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			overflow, ok := parseContextOverflow(http.StatusBadRequest, test.body)
			if !ok {
				t.Fatal("the numbered context refusal was not recognized")
			}
			if overflow.PromptTokens != test.prompt || overflow.NCtx != test.nctx {
				t.Fatalf("overflow = %+v, want prompt %d and window %d", overflow, test.prompt, test.nctx)
			}
			if !errors.Is(overflow, ErrContextOverflow) || overflow.FromRuntime != "the serving runtime" {
				t.Fatalf("overflow = %+v", overflow)
			}
		})
	}
}

func TestNativeHTTPErrorReadsTheOllamaSentenceAsOverflow(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusBadRequest,
		Body:       io.NopCloser(strings.NewReader(`{"error":` + strconv.Quote(ollamaOverflowSentence) + `}`)),
	}
	err := nativeHTTPError(resp)
	overflow, ok := AsContextOverflow(err)
	if !ok {
		t.Fatalf("native HTTP error = %v", err)
	}
	if overflow.PromptTokens != 8023 || overflow.NCtx != 2048 || !errors.Is(err, ErrContextOverflow) {
		t.Fatalf("overflow = %+v", overflow)
	}
}

func TestOllamaSentenceOverflowRejectsADifferentShape(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		body   string
	}{
		{
			name: "leading zero", status: 400,
			body: "request (08023 tokens) exceeds the available context size (2048 tokens), try increasing it",
		},
		{
			name: "leading zero on the window", status: 400,
			body: "request (8023 tokens) exceeds the available context size (02048 tokens), try increasing it",
		},
		{
			name: "zero prompt", status: 400,
			body: "request (0 tokens) exceeds the available context size (2048 tokens), try increasing it",
		},
		{
			name: "zero window", status: 400,
			body: "request (8023 tokens) exceeds the available context size (0 tokens), try increasing it",
		},
		{
			name: "extra words", status: 400,
			body: "request (8023 tokens) exceeds the available context size (2048 tokens), try increasing it now",
		},
		{
			name: "other llama.cpp sentence", status: 400,
			body: "input (8023 tokens) is larger than the max context size (2048 tokens). skipping",
		},
		{
			name: "not a 400", status: 500,
			body: ollamaOverflowSentence,
		},
		{
			name: "count overflows", status: 400,
			body: "request (99999999999999999999 tokens) exceeds the available context size (2048 tokens), try increasing it",
		},
		{
			name: "decoded document of another type", status: 400,
			body: `{"error":{"type":"server_error","message":"` + ollamaOverflowSentence + `","n_ctx":2048,"n_prompt_tokens":8023}}`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, ok := parseContextOverflow(test.status, test.body); ok {
				t.Fatalf("%q was read as a measured overflow refusal", test.body)
			}
		})
	}
}
