package ollama

import (
	"net/http"
	"strconv"
	"strings"
	"testing"
)

func FuzzParseContextOverflow(f *testing.F) {
	f.Add(http.StatusBadRequest, `{"error":"`+ollamaOverflowSentence+`"}`)
	f.Add(http.StatusBadRequest, ollamaOverflowSentence)
	f.Add(http.StatusBadRequest, runnerOverflowBody)
	f.Add(http.StatusBadRequest, "the request exceeds the available context size")
	f.Add(http.StatusBadRequest, "input (8023 tokens) is larger than the max context size (2048 tokens). skipping")
	f.Add(http.StatusBadRequest, "request (08023 tokens) exceeds the available context size (2048 tokens), try increasing it")
	f.Add(http.StatusInternalServerError, ollamaOverflowSentence)
	f.Fuzz(func(t *testing.T, status int, body string) {
		overflow, ok := parseContextOverflow(status, body)
		if !ok {
			return
		}
		if status != http.StatusBadRequest || overflow.PromptTokens < 1 || overflow.NCtx < 1 ||
			overflow.FromRuntime != "the serving runtime" {
			t.Fatalf("status %d overflow = %+v", status, overflow)
		}
		// A recognized count has to be a decimal that was actually in the
		// body. Otherwise the parser invented a window.
		if !strings.Contains(body, strconv.Itoa(overflow.PromptTokens)) ||
			!strings.Contains(body, strconv.Itoa(overflow.NCtx)) {
			t.Fatalf("counts were not in the body: %+v body %q", overflow, body)
		}
		if strings.Contains(body, "\n") {
			return
		}
		if _, still := parseContextOverflow(status, body+" x"); still {
			t.Fatalf("trailing text still parsed: %q", body)
		}
	})
}
