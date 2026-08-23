package eval

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/blisspixel/fitr/internal/boundedio"
	"github.com/blisspixel/fitr/internal/llm"
	"github.com/blisspixel/fitr/internal/ollama"
	"github.com/blisspixel/fitr/internal/strictjson"
)

const maxTaskToolFileBytes = 1 << 20

// ---------------------------------------------------------------- speed
type SpeedResult struct {
	DecodeTPS float64 `json:"decode_tps"`
	// Three TTFTs, because blending them is the single biggest measurement
	// error available (they differ 70-200x on real stacks):
	//
	//   TTFT      - model loaded, prompt uncached. This is a new question
	//               on a running model, and it is what the gate judges.
	//   ColdTTFT  - first question of the day: load + prefill + first token.
	//   WarmTTFT  - same prompt sent again; prefix cache hit. Only filled
	//               when the backend returns a real cache receipt.
	TTFT       float64 `json:"ttft_s"`
	ColdTTFT   float64 `json:"cold_ttft_s,omitempty"`
	WarmTTFT   float64 `json:"warm_ttft_s,omitempty"`
	PrefillTPS float64 `json:"prefill_tps"`
	PromptTok  int     `json:"prompt_tokens"`
	// CachedPromptTok is how much of the PREFILL probe's prompt was served
	// from cache. The nonce exists to make this zero; a nonzero value on a
	// backend that reports it means the prefill figure is partly fiction, and
	// the run says so instead of quietly publishing it.
	CachedPromptTok int `json:"cached_prompt_tokens,omitempty"`
	// GatedCachedTok is how much of the GATED TTFT prompt was cached. If this
	// is a large fraction of GatedPromptTok, the gated number is a warm-prefix
	// figure wearing a cold-prompt badge. Never compared to the prefill
	// prompt: those lengths differ by 10-30x and the check would never fire.
	GatedCachedTok int  `json:"gated_cached_tokens,omitempty"`
	GatedPromptTok int  `json:"gated_prompt_tokens,omitempty"`
	Truncated      bool `json:"truncated"`
	// ClientDerived is copied from the backend: OpenAI-compat timings are
	// wall-clock estimates, not server counters.
	ClientDerived bool `json:"client_derived,omitempty"`
}

// GatedTTFTContaminated is true when most of the gated TTFT prompt was a
// cache hit. The threshold is 20% uncached (cached*5 >= prompt): a real
// new-question prefill is not 80%+ cached.
func (s SpeedResult) GatedTTFTContaminated() bool {
	return s.GatedPromptTok > 0 && s.GatedCachedTok*5 >= s.GatedPromptTok
}

// RunSpeed measures decode and prefill.
//
// nonce MUST vary per repeat. Ollama caches prompt prefixes, so repeating an
// identical long prompt means runs 2..K never actually prefill and the reported
// figure becomes fiction -- observed at 19444 tok/s on a device that genuinely
// does ~140.
func RunSpeed(ctx context.Context, c llm.Backend, model string, s *Spec, nonce string) (SpeedResult, error) {
	var out SpeedResult
	// Warm the model first. TTFT must measure time-to-first-token for a LOADED
	// model; including a cold load reported 4.33s where the warm figure is
	// 0.97s. When this warm-up call actually had to load (the phase starts
	// with residents cleared, so on the first repeat it does), its wall-clock
	// first token IS the honest cold-start figure - record it instead of
	// discarding it.
	warm := ollama.Deterministic(8, numCtx(ctx))
	_, m0, err := c.Generate(ctx, model, "Say OK.", warm)
	if err != nil {
		return out, err
	}
	if m0.LoadSeconds > 0.1 {
		out.ColdTTFT = m0.TTFTSeconds
	}
	samp := ollama.Deterministic(s.Speed.Decode.NumPredict, numCtx(ctx))
	tag := ""
	if nonce != "" {
		tag = "  <!-- run " + nonce + " -->"
	}
	decodePrompt := s.Speed.Decode.Prompt + tag
	_, m1, err := c.Generate(ctx, model, decodePrompt, samp)
	if err != nil {
		return out, err
	}
	out.DecodeTPS, out.TTFT = m1.DecodeTPS, m1.TTFTSeconds
	out.ClientDerived = m1.ClientDerived
	out.GatedPromptTok = m1.PromptTokens
	if m1.CacheKnown {
		out.GatedCachedTok = m1.CachedTokens
	}
	// Deliberately NOT recording m1.Truncated: the speed probe caps output at
	// num_predict, so it always stops on "length". Truncation is only a
	// degeneracy signal for tasks that were free to finish.

	// Same prompt again: if the backend reports a cache receipt, this IS the
	// warm-prefix number. Skipping when CacheKnown is false is the honesty
	// rule - a second generate on Ollama would just be another uncached TTFT.
	if m1.CacheKnown {
		_, m1w, err := c.Generate(ctx, model, decodePrompt, samp)
		if err != nil {
			return out, err
		}
		if m1w.CachedTokens > 0 {
			out.WarmTTFT = m1w.TTFTSeconds
		}
	}

	samp2 := ollama.Deterministic(s.Speed.Prefill.NumPredict, numCtx(ctx))
	_, m2, err := c.Generate(ctx, model, buildLongPrompt(nonce), samp2)
	if err != nil {
		return out, err
	}
	out.PrefillTPS, out.PromptTok = m2.PrefillTPS, m2.PromptTokens
	out.CachedPromptTok = m2.CachedTokens
	return out, nil
}

const codeChunk = `
class LRUCache:
    """Least-recently-used cache with O(1) get and put."""

    def __init__(self, capacity):
        if capacity <= 0:
            raise ValueError("capacity must be positive")
        self.capacity = capacity
        self.map = {}
        self.head = _Node(None, None)
        self.tail = _Node(None, None)
        self.head.next = self.tail
        self.tail.prev = self.head

    def _unlink(self, node):
        node.prev.next = node.next
        node.next.prev = node.prev

    def get(self, key):
        node = self.map.get(key)
        if node is None:
            return None
        self._unlink(node)
        self._push_front(node)
        return node.value
`

// buildLongPrompt makes a ~2.8K-token prompt whose PREFIX is unique per nonce,
// defeating the prompt-prefix cache so prefill is always measured cold.
func buildLongPrompt(nonce string) string {
	var sb strings.Builder
	sb.WriteString("Below is a Python source listing.\n\n")
	if nonce != "" {
		sb.WriteString("# run-id " + nonce + "\n")
	}
	for i := range 16 {
		fmt.Fprintf(&sb, "# ---- module %d ----\n%s\n\n", i, codeChunk)
	}
	sb.WriteString("\nIn one short sentence: what data structure pairing makes LRUCache.get O(1)?")
	return sb.String()
}

// ---------------------------------------------------------------- prompts
var filePlaceholderRe = regexp.MustCompile(`\{\{file:([^}]+)\}\}`)

// RenderPrompt substitutes {{file:NAME}} from the task's own files map.
//
// This exists because an earlier spec shipped the raw template with unfilled
// placeholders. Models were asked to fix code they were never shown, invented
// something plausible, and were scored as FAILING -- a harness bug reported as
// a model weakness. An unresolved placeholder is now a hard error, never a
// silently degraded prompt.
func RenderPrompt(tmpl string, files map[string]string) (string, error) {
	out := filePlaceholderRe.ReplaceAllStringFunc(tmpl, func(m string) string {
		name := filePlaceholderRe.FindStringSubmatch(m)[1]
		if body, ok := files[name]; ok {
			return body
		}
		return m // leave it so the check below can catch it
	})
	if leftover := filePlaceholderRe.FindString(out); leftover != "" {
		return "", fmt.Errorf("prompt has unresolved placeholder %s "+
			"(task declares files: %v)", leftover, keysOf(files))
	}
	return out, nil
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ---------------------------------------------------------------- exec tasks
type ExecResult struct {
	Pass     bool                 `json:"pass"`
	Outcome  Outcome              `json:"outcome,omitempty"`
	Verified bool                 `json:"verified"`
	Verifier *VerificationReceipt `json:"verifier,omitempty"`
	Failure  *Failure             `json:"failure,omitempty"`
	Detail   string               `json:"detail"`
	Raw      string               `json:"raw"`
	File     string               `json:"file_edited,omitempty"`
}

var (
	fenceRe     = regexp.MustCompile("(?s)```(?:[a-zA-Z0-9_+-]*)\\n(.*?)```")
	fenceNameRe = regexp.MustCompile("(?s)```[a-zA-Z]*[ \\t]*([A-Za-z0-9_./-]+\\.py)?[ \\t]*\\n(.*?)```")
)

func extractCode(text, prefer string) string {
	all := fenceRe.FindAllStringSubmatch(text, -1)
	if len(all) == 0 {
		if strings.Contains(text, "def ") || strings.Contains(text, "return") {
			return text
		}
		return ""
	}
	if prefer != "" {
		for _, m := range all {
			if strings.Contains(m[1], prefer) {
				return m[1]
			}
		}
	}
	best := ""
	for _, m := range all {
		if len(m[1]) > len(best) {
			best = m[1]
		}
	}
	return best
}

// RunExec writes fixtures, asks the model, applies its code, and EXECUTES the
// tests. Pass/fail is execution, never a model's opinion about its own work.
func RunExec(ctx context.Context, c llm.Backend, model string, spec ExecSpec, dir string) (ExecResult, error) {
	r := ExecResult{Outcome: OutcomeSkipped}
	if !unsafeExecutionEnabled(ctx) {
		r.Detail = "disabled: generated code execution requires --allow-unsafe-exec and remains unverified"
		return r, nil
	}
	runner, err := resolveTaskRunner(ctx, spec.Runner, spec.Files)
	if err != nil {
		f := failure(FailureExecutorPreflight, spec.ID, err)
		r.Outcome, r.Failure = OutcomeError, f
		return r, f
	}
	r.Outcome = OutcomeInconclusive
	prompt, err := RenderPrompt(spec.Prompt, spec.Files)
	if err != nil {
		f := failure(FailureInvalidSpec, spec.ID+".prompt", err)
		r.Outcome, r.Failure = OutcomeError, f
		return r, f
	}
	samp := ollama.Deterministic(spec.NumPredict, numCtx(ctx))
	text, _, err := c.Generate(ctx, model, prompt, samp)
	if err != nil {
		f := failure(FailureTransport, spec.ID+".generate", err)
		r.Outcome, r.Failure = OutcomeError, f
		return r, f
	}
	r.Raw = text

	if err := os.MkdirAll(dir, 0o755); err != nil {
		f := failure(FailureFixtureIO, "mkdir", err)
		r.Outcome, r.Failure = OutcomeError, f
		return r, f
	}
	for name, body := range spec.Files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			f := failure(FailureFixtureIO, "write_fixture", err)
			r.Outcome, r.Failure = OutcomeError, f
			return r, f
		}
	}

	switch spec.Extract.Strategy {
	case "fenced_code_block_with_filename":
		target, body := spec.Extract.DefaultFile, ""
		if m := fenceNameRe.FindStringSubmatch(text); m != nil {
			if m[1] != "" {
				target = filepath.Base(m[1])
			}
			body = m[2]
		}
		if body == "" {
			body = extractCode(text, "")
		}
		if strings.TrimSpace(body) == "" {
			r.Detail = "no executable code was extracted"
			r.Outcome = OutcomeInconclusive
			return r, nil
		}
		// Only allow edits to files the task declares editable; a model naming
		// some other path must not be able to write outside the fixture.
		if !allowed(target, spec.Editable) {
			target = spec.Extract.DefaultFile
		}
		r.File = target
		if body != "" {
			if err := os.WriteFile(filepath.Join(dir, target), []byte(body), 0o644); err != nil {
				f := failure(FailureFixtureIO, "write_model_file", err)
				r.Outcome, r.Failure = OutcomeError, f
				return r, f
			}
		}
	default:
		code := extractCode(text, spec.Extract.PreferContaining)
		r.File = spec.Entry
		if strings.TrimSpace(code) == "" {
			r.Detail = "no executable code was extracted"
			r.Outcome = OutcomeInconclusive
			return r, nil
		}
		if err := os.WriteFile(filepath.Join(dir, spec.Entry), []byte(code), 0o644); err != nil {
			f := failure(FailureFixtureIO, "write_model_file", err)
			r.Outcome, r.Failure = OutcomeError, f
			return r, f
		}
	}

	receipt := verifyIn(ctx, dir, runner, spec.PassIfStdoutContains)
	r.Detail = tail(receipt.Output, 400)
	r.Pass = receipt.SuccessfulExit && receipt.ExactFinalMarker
	r.Verifier = &receipt
	r.Failure = receipt.Failure
	if receipt.Failure != nil {
		r.Detail = strings.TrimSpace(r.Detail + "; " + receipt.Failure.Error())
	}
	// Executable observations stay inconclusive until an isolated worker keeps
	// generated code away from the verifier and the user's machine.
	r.Outcome = OutcomeInconclusive
	return r, nil
}

func allowed(name string, list []string) bool {
	if len(list) == 0 {
		return true
	}
	return slices.Contains(list, name)
}

func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// ---------------------------------------------------------------- tool loops
type ToolLoopResult struct {
	Pass                 bool                  `json:"pass"`
	Outcome              Outcome               `json:"outcome,omitempty"`
	Verified             bool                  `json:"verified"`
	Verifier             *VerificationReceipt  `json:"verifier,omitempty"`
	VerifierObservations []VerificationReceipt `json:"verifier_observations,omitempty"`
	Failure              *Failure              `json:"failure,omitempty"`
	Turns                int                   `json:"turns"`
	Calls                int                   `json:"tool_calls"`
	Malformed            int                   `json:"malformed_calls"`
	Repeats              int                   `json:"repeated_identical_calls"`
	Looped               bool                  `json:"looped"`
	Ended                string                `json:"ended"`
	Sequence             string                `json:"call_sequence"`
	// MaxPromptTok is the largest transcript the model re-processed in one
	// turn; CtxCeiling is set when it crossed 80% of the context window.
	// A model that lets its transcript grow to the ceiling without managing
	// it will fail in exactly the way a looped table fails: everything looks
	// structurally fine until the window is full.
	MaxPromptTok int  `json:"max_prompt_tokens,omitempty"`
	CtxCeiling   bool `json:"context_ceiling,omitempty"`
	// Compacted is true if prompt tokens ever shrank between turns: the
	// model managed its context. Ceiling without this is the watchdog FAIL.
	Compacted bool `json:"compacted,omitempty"`
	// DeadCalls counts calls to a tool AFTER it was withdrawn mid-loop. The
	// tools parameter names what exists each turn; calling a tool that is no
	// longer listed is a hallucinated capability.
	DeadCalls  int      `json:"withdrawn_tool_calls,omitempty"`
	FilesWrote []string `json:"files_written"`
	Detail     string   `json:"detail"`
}

var seqCode = map[string]string{
	"list_files": "L", "read_file": "R", "write_file": "W", "run_tests": "T",
	"lookup_part": "K",
}

// RunToolLoop drives a real tool loop and verifies the RESULT, not the chatter.
// Scored on what actually breaks unattended runs: malformed calls, looping on
// an identical action, and failing to terminate.
func RunToolLoop(ctx context.Context, c llm.Backend, model string, spec ToolLoopSpec, dir string) (ToolLoopResult, error) {
	r := ToolLoopResult{Ended: "turn_cap"}
	requiresExec := ToolLoopRequiresExecution(spec)
	var runner resolvedTaskRunner
	if requiresExec && !unsafeExecutionEnabled(ctx) {
		r.Outcome = OutcomeSkipped
		r.Ended = "unsafe_execution_disabled"
		r.Detail = "disabled: generated code execution requires --allow-unsafe-exec and remains unverified"
		return r, nil
	}
	if requiresExec {
		var err error
		runner, err = resolveTaskRunner(ctx, spec.Verify.Runner, spec.Files)
		if err != nil {
			f := failure(FailureExecutorPreflight, spec.ID, err)
			r.Outcome, r.Failure = OutcomeError, f
			return r, f
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		f := failure(FailureFixtureIO, "mkdir", err)
		r.Outcome, r.Failure = OutcomeError, f
		return r, f
	}
	for name, body := range spec.Files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			f := failure(FailureFixtureIO, "write_fixture", err)
			r.Outcome, r.Failure = OutcomeError, f
			return r, f
		}
	}

	written := map[string]bool{}
	sigCount := map[string]int{}
	msgs := []ollama.Message{{Role: "user", Content: spec.Prompt}}
	lastPrompt := 0
	samp := ollama.Deterministic(spec.NumPredict, numCtx(ctx))
	deadline := time.Now().Add(time.Duration(spec.Budget) * time.Second)
	var seq strings.Builder

	for turn := 0; turn < spec.MaxTurns; turn++ {
		r.Turns = turn + 1
		if time.Now().After(deadline) {
			r.Ended = "time_budget"
			break
		}
		// A withdrawn tool disappears from the tools parameter: the model is
		// TOLD what exists every turn, so continuing to call it is on the model.
		activeTools := spec.Tools
		withdrawn := spec.WithdrawTool != "" && turn >= spec.WithdrawAfter
		if withdrawn {
			activeTools = nil
			for _, t := range spec.Tools {
				if t.Function.Name != spec.WithdrawTool {
					activeTools = append(activeTools, t)
				}
			}
		}
		msg, tm, err := c.Chat(ctx, model, msgs, activeTools, samp)
		if err != nil {
			f := failure(FailureTransport, "tool_loop.chat", err)
			r.Outcome, r.Failure, r.Ended = OutcomeError, f, "transport_error"
			return r, f
		}
		if tm.PromptTokens > r.MaxPromptTok {
			r.MaxPromptTok = tm.PromptTokens
		}
		if lastPrompt > 0 && tm.PromptTokens > 0 && tm.PromptTokens < lastPrompt {
			r.Compacted = true
		}
		if tm.PromptTokens > lastPrompt {
			lastPrompt = tm.PromptTokens
		}
		if len(msg.ToolCalls) == 0 {
			if exactDone(msg.Content) {
				r.Ended = "clean_stop"
			} else {
				r.Ended = "stopped_without_done"
			}
			break
		}
		msgs = append(msgs, msg)

		for _, tc := range msg.ToolCalls {
			r.Calls++
			name := tc.Function.Name
			args := map[string]any{}
			if len(tc.Function.Arguments) > 0 {
				if err := strictjson.Unmarshal(tc.Function.Arguments, &args); err != nil {
					// Some models emit arguments as a JSON *string*; try once more.
					var asStr string
					if strictjson.Unmarshal(tc.Function.Arguments, &asStr) == nil {
						if strictjson.Unmarshal([]byte(asStr), &args) != nil {
							r.Malformed++
						}
					} else {
						r.Malformed++
					}
				}
			}
			if _, ok := seqCode[name]; !ok {
				r.Malformed++
			}
			seq.WriteString(seqCode[name])

			p, _ := args["path"].(string)
			content, _ := args["content"].(string)
			sig := name + "|" + p + "|" + shortHash(content)
			sigCount[sig]++

			var result string
			if withdrawn && name == spec.WithdrawTool {
				r.DeadCalls++
				result = "ERROR: tool " + name + " is no longer available; it has been removed"
			} else {
				var toolErr error
				result, toolErr = doTool(ctx, dir, name, p, content, args, spec, written, runner, &r.VerifierObservations)
				if toolErr != nil {
					var typed *Failure
					if !errors.As(toolErr, &typed) {
						typed = failure(FailureFixtureIO, "tool_loop."+name, toolErr)
					}
					r.Outcome, r.Failure, r.Ended = OutcomeError, typed, "tool_error"
					return r, typed
				}
			}
			msgs = append(msgs, ollama.Message{
				Role: "tool", ToolName: name, ToolCallID: tc.ID,
				Content: truncate(result, 4000),
			})
		}
	}

	for _, v := range sigCount {
		if v > 1 {
			r.Repeats += v - 1
		}
	}
	r.Looped = r.Repeats >= 3
	r.Sequence = seq.String()
	r.CtxCeiling = r.MaxPromptTok > samp.NumCtx*8/10
	for f := range written {
		r.FilesWrote = append(r.FilesWrote, f)
	}
	sort.Strings(r.FilesWrote)

	// Behavioral tasks (withdrawal) have no verify runner: passing means the
	// loop terminated cleanly; the behavioral counters are judged by the scorer.
	if len(spec.Verify.Runner) == 0 {
		r.Pass = r.Ended == "clean_stop"
		r.Outcome = outcomeFor(r.Pass)
		r.Verified = true
		return r, nil
	}
	receipt := verifyIn(ctx, dir, runner, spec.Verify.PassIfStdoutContains)
	r.VerifierObservations = append(r.VerifierObservations, receipt)
	r.Detail = tail(receipt.Output, 300)
	r.Pass = receipt.SuccessfulExit && receipt.ExactFinalMarker &&
		r.Ended == "clean_stop" && r.Malformed == 0 && !r.Looped
	r.Verifier = &receipt
	r.Failure = receipt.Failure
	if receipt.Failure != nil {
		r.Detail = strings.TrimSpace(r.Detail + "; " + receipt.Failure.Error())
	}
	r.Outcome = OutcomeInconclusive
	return r, nil
}

func doTool(ctx context.Context, dir, name, p, content string, args map[string]any, spec ToolLoopSpec,
	written map[string]bool, runner resolvedTaskRunner, observations *[]VerificationReceipt) (string, error) {
	switch name {
	case "list_files":
		entries, err := os.ReadDir(dir)
		if err != nil {
			return "", failure(FailureFixtureIO, "list_files", err)
		}
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		sort.Strings(names)
		return strings.Join(names, "\n"), nil
	case "read_file":
		if !safeTaskFileName(p) {
			return "ERROR: invalid task-local path", nil
		}
		b, err := boundedio.ReadFile(filepath.Join(dir, p), maxTaskToolFileBytes)
		if err != nil {
			if _, fixture := spec.Files[p]; fixture || written[p] {
				return "", failure(FailureFixtureIO, "read_file", err)
			}
			return "ERROR: " + err.Error(), nil
		}
		return string(b), nil
	case "write_file":
		if !safeTaskFileName(p) {
			return "ERROR: invalid task-local path", nil
		}
		if len(content) > maxTaskToolFileBytes {
			return fmt.Sprintf("ERROR: content exceeds %d bytes", maxTaskToolFileBytes), nil
		}
		if err := os.WriteFile(filepath.Join(dir, p), []byte(content), 0o644); err != nil {
			return "", failure(FailureFixtureIO, "write_file", err)
		}
		written[p] = true
		return fmt.Sprintf("wrote %d bytes to %s", len(content), p), nil
	case "run_tests":
		receipt := verifyIn(ctx, dir, runner, spec.Verify.PassIfStdoutContains)
		*observations = append(*observations, receipt)
		if receipt.SuccessfulExit && receipt.ExactFinalMarker {
			return "PASS\n" + receipt.Output, nil
		}
		if receipt.Failure != nil {
			switch receipt.Failure.Kind {
			case FailureExecutorPreflight, FailureExecutorLaunch, FailureExecutorTimeout:
				return "", receipt.Failure
			}
			return "INCONCLUSIVE: " + receipt.Failure.Error() + "\n" + receipt.Output, nil
		}
		return "FAIL\n" + receipt.Output, nil
	case "lookup_part":
		// Prices ship in the task's own parts.txt (NAME=PRICE per line), so
		// the data stays in the spec, not in Go.
		part, _ := args["part"].(string)
		b, err := boundedio.ReadFile(filepath.Join(dir, "parts.txt"), maxTaskToolFileBytes)
		if err != nil {
			return "", failure(FailureFixtureIO, "lookup_part", err)
		}
		for _, line := range strings.Split(string(b), "\n") {
			if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok && k == part {
				return v, nil
			}
		}
		return "ERROR: unknown part " + part, nil
	}
	return "ERROR: unknown tool " + name, nil
}

// safeTaskFileName recognizes one ordinary portable filename. The tool loop
// must behave the same on every release target, including Windows where names
// such as NUL and CON are devices and a colon opens an NTFS alternate stream.
func safeTaskFileName(name string) bool {
	if name == "" || name == "." || name == ".." || len(name) > 255 ||
		filepath.Base(name) != name || strings.TrimSpace(name) != name ||
		strings.ContainsAny(name, `/\:<>"|?*`) || strings.HasSuffix(name, ".") {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return false
		}
	}
	stem := strings.ToUpper(strings.SplitN(name, ".", 2)[0])
	switch stem {
	case "CON", "PRN", "AUX", "NUL", "CONIN$", "CONOUT$",
		"COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9",
		"COM¹", "COM²", "COM³", "LPT¹", "LPT²", "LPT³":
		return false
	}
	return true
}

func exactDone(content string) bool {
	return strings.EqualFold(strings.TrimSpace(content), "DONE")
}

func shortHash(s string) string {
	if len(s) > 200 {
		s = s[:200]
	}
	var h uint32 = 2166136261
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= 16777619
	}
	return fmt.Sprintf("%08x", h)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
