package ollama

import (
	"math"
	"strings"
)

// The sentence below is the only prose receipt this package treats as a
// measured overflow. Checked in llama.cpp commit
// 339f3242c761f16c27e5ffcde1d35dcc31933a55, tools/server/server-context.cpp:
// when a splittable slot cannot hold the prompt, the runner formats
// "request (%d tokens) exceeds the available context size (%d tokens), try
// increasing it" from slot.task->n_tokens() and slot.n_ctx, with error type
// ERROR_TYPE_EXCEED_CONTEXT_SIZE.
//
// Ollama v0.40.4 (tag commit 45e6011815c74acde6a346512856b69dd79dd22b) does
// not forward that JSON. extractLlamaServerErrorMessage keeps the message
// string, and the HTTP body is {"error":"<message>"}. The neighboring
// sentence, "input (%d tokens) is larger than the max context size (%d
// tokens). skipping", is a different branch and is not this observation.
const (
	contextOverflowSentenceLead = "request ("
	contextOverflowSentenceMid  = " tokens) exceeds the available context size ("
	contextOverflowSentenceTail = " tokens), try increasing it"
)

// parseContextOverflowSentence reads the two integers only when the whole
// line is that format. A leading zero is a different spelling than the
// integer it would denote, and an extra word is a shape that was not
// observed, so both stay unrecognized.
func parseContextOverflowSentence(line string) (*ContextOverflow, bool) {
	rest, ok := strings.CutPrefix(line, contextOverflowSentenceLead)
	if !ok {
		return nil, false
	}
	prompt, rest, ok := cutPositiveDecimal(rest)
	if !ok {
		return nil, false
	}
	rest, ok = strings.CutPrefix(rest, contextOverflowSentenceMid)
	if !ok {
		return nil, false
	}
	nctx, rest, ok := cutPositiveDecimal(rest)
	if !ok || rest != contextOverflowSentenceTail {
		return nil, false
	}
	return &ContextOverflow{
		NCtx: nctx, PromptTokens: prompt, FromRuntime: "the serving runtime",
	}, true
}

// cutPositiveDecimal reads the decimal llama.cpp prints with %d. The first
// digit must be non-zero, and a value that does not fit in an int is not a
// count fitr can report.
func cutPositiveDecimal(s string) (int, string, bool) {
	if s == "" || s[0] < '1' || s[0] > '9' {
		return 0, "", false
	}
	n := 0
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		digit := int(s[i] - '0')
		if n > (math.MaxInt-digit)/10 {
			return 0, "", false
		}
		n = n*10 + digit
		i++
	}
	return n, s[i:], true
}
