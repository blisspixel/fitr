// Package ollama is a minimal client for the Ollama HTTP API.
//
// Deliberately not the official SDK: we need precise control over sampling
// parameters, streaming timing (time-to-first-token must be wall-clock from
// request start), and the raw done_reason -- and we want zero dependencies.
package ollama

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/blisspixel/fitr/internal/boundedio"
	"github.com/blisspixel/fitr/internal/diskspace"
	"github.com/blisspixel/fitr/internal/strictjson"
)

const DefaultURL = "http://127.0.0.1:11434"

const (
	maxNativeBody       = 16 << 20
	maxNativeError      = 64 << 10
	maxNativeLine       = 1 << 20
	maxNativeStream     = 64 << 20
	maxNativeFrames     = 1_000_000
	maxGeneratedOutput  = 8 << 20
	maxServerLogTail    = 1 << 20
	controlPlaneTimeout = 15 * time.Second
)

var serverLibraryPattern = regexp.MustCompile(`library=([A-Za-z0-9_]+)`)

type Client struct {
	BaseURL string
	HTTP    *http.Client
}

func New() *Client {
	base := os.Getenv("OLLAMA_BASE_URL")
	if base == "" {
		base = DefaultURL
	}
	return &Client{
		BaseURL: strings.TrimRight(base, "/"),
		// Long timeout: a 40-turn agentic run on a slow local model is legitimately
		// slow. Cancellation is the caller's job, via context.
		HTTP: &http.Client{Timeout: 60 * time.Minute},
	}
}

// Sampling holds the knobs that decide reproducibility.
//
// Temperature 0 alone is NOT enough (float non-associativity, batch-variant
// kernels, prompt-cache reuse), but temp 0 + top_k 1 + a fixed seed removes
// every source we control. Repeats handle the rest.
//
// RepeatPenalty is pinned deliberately, and pinned to 1.0 (off). Ollama
// changed its own default from 1.1 to 1.0 in v0.32.10, so leaving it unset
// makes degeneracy results depend on the server version. Worse, a repetition
// penalty is precisely the mechanism that *hides* looping -- llama.cpp ships
// DRY, XTC and repeat-penalty samplers that suppress loops but never report
// them. fitr measures whether this model, at this quant, on this hardware
// degenerates; masking that with a sampler would measure the sampler.
type Sampling struct {
	Temperature   float64 `json:"temperature"`
	TopK          int     `json:"top_k"`
	Seed          int     `json:"seed"`
	NumCtx        int     `json:"num_ctx"`
	NumPredict    int     `json:"num_predict"`
	RepeatPenalty float64 `json:"repeat_penalty"`

	// Format, when "json", asks the server for grammar-constrained output.
	// Kept OFF for every measurement task: the constrained path has its own
	// failure modes (it breaks seed reproducibility on some stacks), which is
	// exactly why `doctor` probes it separately.
	Format string `json:"-"`
}

func Deterministic(numPredict, numCtx int) Sampling {
	return Sampling{
		Temperature: 0, TopK: 1, Seed: 42,
		NumCtx: numCtx, NumPredict: numPredict,
		RepeatPenalty: 1.0,
	}
}

// Metrics are the timing facts for one generation.
type Metrics struct {
	TTFTSeconds  float64 `json:"ttft_s"`
	WallSeconds  float64 `json:"wall_s"`
	EvalCount    int     `json:"eval_count"`
	DecodeTPS    float64 `json:"decode_tps"`
	PromptTokens int     `json:"prompt_eval_count"`
	PrefillTPS   float64 `json:"prefill_tps"`
	LoadSeconds  float64 `json:"load_s"`
	DoneReason   string  `json:"done_reason"`
	// CachedTokens is how much of the prompt was served from the prefix cache
	// rather than evaluated - the receipt that separates a warm TTFT from a
	// cold one, which differ by 70-200x. Ollama does not report it (always 0);
	// llama-server does. CacheKnown is whether the field is a real receipt:
	// a zero with CacheKnown=false is "not measured", not "cache miss".
	CachedTokens int  `json:"cached_tokens,omitempty"`
	CacheKnown   bool `json:"cache_known,omitempty"`
	// Truncated means the model hit the token cap. Worth scoring as a failure:
	// roughly 92% of truncations are repetition loops wearing a cap.
	Truncated bool `json:"truncated"`
	// ClientDerived is true when tok/s and TTFT were computed from wall-clock
	// on this side of the socket. The OpenAI-compatible surface has no server
	// timings; Ollama and llama-server leave this false.
	ClientDerived bool `json:"client_derived,omitempty"`
}

type genResp struct {
	Response           string `json:"response"`
	Done               bool   `json:"done"`
	DoneReason         string `json:"done_reason"`
	EvalCount          int    `json:"eval_count"`
	EvalDuration       int64  `json:"eval_duration"`
	PromptEvalCount    int    `json:"prompt_eval_count"`
	PromptEvalDuration int64  `json:"prompt_eval_duration"`
	LoadDuration       int64  `json:"load_duration"`
}

func (c *Client) post(ctx context.Context, path string, body any) (*http.Response, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.HTTP.Do(req)
}

func readBounded(r io.Reader, limit int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("response exceeds %d bytes", limit)
	}
	return b, nil
}

func decodeBoundedJSON(r io.Reader, into any) error {
	b, err := readBounded(r, maxNativeBody)
	if err != nil {
		return err
	}
	return decodeJSONFrame(b, into)
}

func decodeJSONFrame(frame []byte, into any) error {
	dec := json.NewDecoder(bytes.NewReader(frame))
	if err := dec.Decode(into); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("content after JSON frame")
		}
		return err
	}
	return strictjson.Validate(frame)
}

func nativeHTTPError(resp *http.Response) error {
	b, err := readBounded(resp.Body, maxNativeError)
	if err != nil {
		return fmt.Errorf("ollama %d: %w", resp.StatusCode, err)
	}
	return fmt.Errorf("ollama %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
}

func validateGenFrame(g genResp) error {
	if g.EvalCount < 0 || g.EvalDuration < 0 || g.PromptEvalCount < 0 ||
		g.PromptEvalDuration < 0 || g.LoadDuration < 0 {
		return errors.New("ollama generate frame contains a negative metric")
	}
	return nil
}

// Generate streams /api/generate and returns the text plus timing.
//
// TTFT is measured as wall-clock from request start to the first non-empty
// chunk -- not derived from the server's own counters, because what the user
// experiences is the wall clock.
func (c *Client) Generate(ctx context.Context, model, prompt string, s Sampling) (string, Metrics, error) {
	payload := map[string]any{
		"model": model, "prompt": prompt, "stream": true,
		"options": map[string]any{
			"temperature": s.Temperature, "top_k": s.TopK, "seed": s.Seed,
			"num_ctx": s.NumCtx, "num_predict": s.NumPredict,
			"repeat_penalty": s.RepeatPenalty,
		},
		"keep_alive": "10m",
		"think":      false,
	}
	if s.Format != "" {
		payload["format"] = s.Format
	}
	start := time.Now()
	resp, err := c.post(ctx, "/api/generate", payload)
	if err != nil {
		return "", Metrics{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", Metrics{}, nativeHTTPError(resp)
	}

	var sb strings.Builder
	var ttft float64
	var final genResp
	var totalBytes, frames int
	terminal := false
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), maxNativeLine)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		totalBytes += len(line) + 1
		frames++
		if totalBytes > maxNativeStream || frames > maxNativeFrames {
			return sb.String(), Metrics{}, errors.New("ollama generate stream exceeds protocol limits")
		}
		if terminal {
			return sb.String(), Metrics{}, errors.New("ollama generate stream contains data after the terminal frame")
		}
		var g genResp
		if err := decodeJSONFrame(line, &g); err != nil {
			return sb.String(), Metrics{}, fmt.Errorf("ollama generate frame: %w", err)
		}
		if err := validateGenFrame(g); err != nil {
			return sb.String(), Metrics{}, err
		}
		if g.Response != "" && ttft == 0 {
			ttft = time.Since(start).Seconds()
		}
		if len(g.Response) > maxGeneratedOutput-sb.Len() {
			return sb.String(), Metrics{}, fmt.Errorf("ollama generated output exceeds %d bytes", maxGeneratedOutput)
		}
		sb.WriteString(g.Response)
		if g.Done {
			final = g
			terminal = true
		}
	}
	if err := sc.Err(); err != nil {
		return sb.String(), Metrics{}, fmt.Errorf("ollama generate stream: %w", err)
	}
	if !terminal {
		return sb.String(), Metrics{}, errors.New("ollama generate stream ended before a terminal frame")
	}

	m := Metrics{
		TTFTSeconds:  round(ttft, 3),
		WallSeconds:  round(time.Since(start).Seconds(), 2),
		EvalCount:    final.EvalCount,
		PromptTokens: final.PromptEvalCount,
		LoadSeconds:  round(float64(final.LoadDuration)/1e9, 2),
		DoneReason:   final.DoneReason,
		Truncated:    final.DoneReason == "length",
	}
	if final.EvalDuration > 0 {
		m.DecodeTPS = round(float64(final.EvalCount)/(float64(final.EvalDuration)/1e9), 2)
	}
	if final.PromptEvalDuration > 0 {
		m.PrefillTPS = round(float64(final.PromptEvalCount)/(float64(final.PromptEvalDuration)/1e9), 2)
	}
	return sb.String(), m, nil
}

// ---------------------------------------------------------------- chat/tools
type Tool struct {
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

type ToolFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

type ToolCall struct {
	// ID is empty on Ollama, which has no call ids; OpenAI-shaped backends
	// set it and require it echoed back on the tool-result message.
	ID       string `json:"id,omitempty"`
	Function struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"function"`
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	// Thinking carries reasoning_content. It MUST round-trip across agentic
	// turns: models trained to see their prior reasoning degrade measurably
	// when the harness silently drops it, and that loss would be recorded as
	// the model's failure. Appending the assistant message unmodified
	// preserves it; this field exists so serialization does too.
	Thinking   string     `json:"thinking,omitempty"`
	ToolName   string     `json:"tool_name,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
}

type chatResp struct {
	Message            Message `json:"message"`
	Done               bool    `json:"done"`
	DoneReason         string  `json:"done_reason"`
	EvalCount          int     `json:"eval_count"`
	EvalDuration       int64   `json:"eval_duration"`
	PromptEvalCount    int     `json:"prompt_eval_count"`
	PromptEvalDuration int64   `json:"prompt_eval_duration"`
}

// Chat returns the message plus per-turn metrics. The prompt token count is
// what makes an agentic loop measurable: it is the size of the transcript the
// model just re-processed, which is both the prefill bill and the input to
// the compaction watchdog.
// Chat sends one turn. A non-terminal reply that evaluated no tokens is
// retried once: see chatOnce for why that specific case, and only that case.
func (c *Client) Chat(ctx context.Context, model string, msgs []Message, tools []Tool, s Sampling) (Message, Metrics, error) {
	msg, m, err := c.chatOnce(ctx, model, msgs, tools, s)
	if err == nil || !errors.Is(err, errEmptyNonTerminal) {
		return msg, m, err
	}
	// The server produced no generation at all, so there is nothing measured
	// to protect and nothing to contaminate. Ask again rather than throw away
	// a battery that has already run for minutes.
	return c.chatOnce(ctx, model, msgs, tools, s)
}

// errEmptyNonTerminal marks a reply that is neither finished nor a generation:
// no terminal receipt, and zero tokens evaluated on either side.
//
// This is worth singling out because fail-closed is otherwise right. A run
// that stops is better than a run that quietly retries around a real fault,
// and a partial generation must never be reused. But a reply that evaluated
// ZERO tokens measured nothing: it cannot be a truncated answer, a refusal, or
// a slow model, because none of those are free. It has been observed carrying
// tool calls with no content and no timings, which is a shape the model cannot
// produce. Retrying it risks nothing and has already cost three completed
// batteries today, each several minutes in.
//
// A real fault still stops the run: anything with eval_count above zero, any
// HTTP error, and a second failure here all return unretried.
var errEmptyNonTerminal = errors.New("ollama chat returned no generation and no terminal receipt")

func (c *Client) chatOnce(ctx context.Context, model string, msgs []Message, tools []Tool, s Sampling) (Message, Metrics, error) {
	payload := map[string]any{
		"model": model, "messages": msgs, "stream": false,
		"options": map[string]any{
			"temperature": s.Temperature, "top_k": s.TopK, "seed": s.Seed,
			"num_ctx": s.NumCtx, "num_predict": s.NumPredict,
			"repeat_penalty": s.RepeatPenalty,
		},
		"keep_alive": "10m",
		"think":      false,
	}
	if len(tools) > 0 {
		payload["tools"] = tools
	}
	start := time.Now()
	resp, err := c.post(ctx, "/api/chat", payload)
	if err != nil {
		return Message{}, Metrics{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Message{}, Metrics{}, nativeHTTPError(resp)
	}
	var r chatResp
	if err := decodeBoundedJSON(resp.Body, &r); err != nil {
		return Message{}, Metrics{}, err
	}
	if !r.Done {
		// The diagnostic carries the fields that identify the cause rather
		// than just naming the symptom: done_reason separates a load reply
		// from a truncated generation, and the token counts separate "the
		// model produced something incomplete" from "the model never ran".
		detail := fmt.Sprintf(
			"(done_reason=%q role=%q content=%d bytes tool_calls=%d eval=%d prompt_eval=%d)",
			r.DoneReason, r.Message.Role, len(r.Message.Content),
			len(r.Message.ToolCalls), r.EvalCount, r.PromptEvalCount)
		if r.EvalCount == 0 && r.PromptEvalCount == 0 {
			return Message{}, Metrics{}, fmt.Errorf("%w %s", errEmptyNonTerminal, detail)
		}
		return Message{}, Metrics{}, fmt.Errorf(
			"ollama chat response is missing the terminal receipt %s", detail)
	}
	if r.Message.Role != "assistant" {
		return Message{}, Metrics{}, fmt.Errorf(
			"ollama chat response has role %q, want assistant", r.Message.Role)
	}
	if r.EvalCount < 0 || r.EvalDuration < 0 || r.PromptEvalCount < 0 || r.PromptEvalDuration < 0 {
		return Message{}, Metrics{}, errors.New("ollama chat response contains a negative metric")
	}
	m := Metrics{
		WallSeconds:  round(time.Since(start).Seconds(), 2),
		EvalCount:    r.EvalCount,
		PromptTokens: r.PromptEvalCount,
		DoneReason:   r.DoneReason,
		Truncated:    r.DoneReason == "length",
	}
	if r.EvalDuration > 0 {
		m.DecodeTPS = round(float64(r.EvalCount)/(float64(r.EvalDuration)/1e9), 2)
	}
	if r.PromptEvalDuration > 0 {
		m.PrefillTPS = round(float64(r.PromptEvalCount)/(float64(r.PromptEvalDuration)/1e9), 2)
	}
	return r.Message, m, nil
}

// ---------------------------------------------------------------- inspection
type ModelInfo struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	Digest string `json:"digest,omitempty"`
	// ReportedDigest is an untrusted server assertion that requires an
	// independent backend-specific verifier before it can become Digest.
	ReportedDigest string   `json:"reported_digest,omitempty"`
	Capabilities   []string `json:"capabilities"`
	// Path is a local GGUF path when the runtime exposes one (llama-server
	// /props model_path). Empty on Ollama tags.
	Path string `json:"path,omitempty"`
	// Info is GGUF metadata from Ollama /api/show (architecture, expert
	// counts, context length). Absent fields stay missing; callers must not
	// invent them.
	Info    map[string]any `json:"model_info,omitempty"`
	Details struct {
		ParameterSize     string `json:"parameter_size"`
		QuantizationLevel string `json:"quantization_level"`
		Family            string `json:"family"`
	} `json:"details"`
}

func (c *Client) Tags(ctx context.Context) ([]ModelInfo, error) {
	cctx, cancel := context.WithTimeout(ctx, controlPlaneTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, c.BaseURL+"/api/tags", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, nativeHTTPError(resp)
	}
	var r struct {
		Models []ModelInfo `json:"models"`
	}
	if err := decodeBoundedJSON(resp.Body, &r); err != nil {
		return nil, err
	}
	if r.Models == nil {
		return nil, errors.New("ollama tags response is missing the models array")
	}
	seen := make(map[string]bool, len(r.Models))
	for _, model := range r.Models {
		if model.Name == "" || strings.TrimSpace(model.Name) != model.Name {
			return nil, fmt.Errorf("ollama tags response contains an invalid model name %q", model.Name)
		}
		if seen[model.Name] {
			return nil, fmt.Errorf("ollama tags response contains duplicate model name %q", model.Name)
		}
		seen[model.Name] = true
		if model.Size < 0 {
			return nil, fmt.Errorf("ollama model %q has a negative size", model.Name)
		}
		if model.Digest != strings.TrimSpace(model.Digest) {
			return nil, fmt.Errorf("ollama model %q has an invalid digest", model.Name)
		}
	}
	return r.Models, nil
}

// Show returns capabilities for one model. `capabilities` is authoritative for
// whether a model supports thinking/tools/vision -- far better than matching on
// name prefixes, which mistakes qwen3-coder (instruct) for a thinking model.
func (c *Client) Show(ctx context.Context, model string) (ModelInfo, error) {
	cctx, cancel := context.WithTimeout(ctx, controlPlaneTimeout)
	defer cancel()
	resp, err := c.post(cctx, "/api/show", map[string]string{"model": model})
	if err != nil {
		return ModelInfo{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ModelInfo{}, nativeHTTPError(resp)
	}
	var mi ModelInfo
	err = decodeBoundedJSON(resp.Body, &mi)
	if err == nil && mi.Size < 0 {
		err = fmt.Errorf("ollama model %q has a negative size", model)
	}
	mi.Name = model
	return mi, err
}

type RunningModel struct {
	Name          string `json:"name"`
	Size          int64  `json:"size"`
	SizeVRAM      int64  `json:"size_vram"`
	ContextLength int    `json:"context_length,omitempty"`
	ExpiresAt     string `json:"expires_at"`
}

func (c *Client) PS(ctx context.Context) ([]RunningModel, error) {
	cctx, cancel := context.WithTimeout(ctx, controlPlaneTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, c.BaseURL+"/api/ps", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, nativeHTTPError(resp)
	}
	var r struct {
		Models []RunningModel `json:"models"`
	}
	if err := decodeBoundedJSON(resp.Body, &r); err != nil {
		return nil, err
	}
	if r.Models == nil {
		return nil, errors.New("ollama ps response is missing the models array")
	}
	seen := make(map[string]bool, len(r.Models))
	for _, model := range r.Models {
		if model.Name == "" || strings.TrimSpace(model.Name) != model.Name {
			return nil, fmt.Errorf("ollama ps response contains an invalid model name %q", model.Name)
		}
		if seen[model.Name] {
			return nil, fmt.Errorf("ollama ps response contains duplicate, ambiguous model name %q", model.Name)
		}
		seen[model.Name] = true
		if model.Size < 0 || model.SizeVRAM < 0 || model.ContextLength < 0 {
			return nil, fmt.Errorf("ollama resident model %q contains a negative size or context", model.Name)
		}
		if model.SizeVRAM > model.Size {
			return nil, fmt.Errorf("ollama resident model %q reports GPU bytes larger than total bytes", model.Name)
		}
	}
	return r.Models, nil
}

// EffectiveContext reads the context allocation reported for the exact model
// currently loaded by Ollama. Older servers omit context_length; that is an
// unavailable observation, not zero context and not the requested value.
func (c *Client) EffectiveContext(ctx context.Context, model string) (int, bool, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		return 0, false, errors.New("effective context requires a resolved model name")
	}
	running, err := c.Resident(ctx)
	if err != nil {
		return 0, false, err
	}
	var match *RunningModel
	for i := range running {
		if running[i].Name != model {
			continue
		}
		if match != nil {
			return 0, false, fmt.Errorf("effective context is ambiguous for model %q", model)
		}
		match = &running[i]
	}
	if match == nil || match.ContextLength == 0 {
		return 0, false, nil
	}
	if match.ContextLength < 0 {
		return 0, false, fmt.Errorf("ollama reported a negative context length for model %q", model)
	}
	return match.ContextLength, true, nil
}

// Resident reports models that are actually still loaded.
//
// A model whose expires_at is in the past has been unloaded but may still
// appear in /api/ps for a moment. Treating that as "still resident" makes
// StopAll spin until it times out on a model that is already gone.
func (c *Client) Resident(ctx context.Context) ([]RunningModel, error) {
	all, err := c.PS(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	var live []RunningModel
	for _, m := range all {
		if m.ExpiresAt != "" {
			if t, err := time.Parse(time.RFC3339, m.ExpiresAt); err == nil && t.Before(now) {
				continue // expired, just not yet reaped from the listing
			}
		}
		live = append(live, m)
	}
	return live, nil
}

// StopAll unloads every resident model and returns any that would not go.
//
// One model resident at a time is non-negotiable between phases: concurrent
// models contaminated timings badly enough to invalidate an entire eval run.
// Callers should WARN and record leftovers rather than abort -- data marked as
// possibly contaminated beats data silently trusted, and beats no data at all.
func (c *Client) StopAll(ctx context.Context) ([]string, error) {
	cctx, cancel := context.WithTimeout(ctx, controlPlaneTimeout)
	defer cancel()
	for range 8 {
		live, err := c.Resident(cctx)
		if err != nil {
			return nil, err
		}
		if len(live) == 0 {
			return nil, nil
		}
		for _, m := range live {
			resp, err := c.post(cctx, "/api/generate", map[string]any{
				"model": m.Name, "prompt": "", "keep_alive": 0,
			})
			if err == nil {
				resp.Body.Close()
			}
		}
		select {
		case <-cctx.Done():
			return nil, cctx.Err()
		case <-time.After(1500 * time.Millisecond):
		}
	}
	live, err := c.Resident(cctx)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, m := range live {
		names = append(names, m.Name)
	}
	return names, nil
}

func (c *Client) Name() string { return "ollama" }
func (c *Client) URL() string  { return c.BaseURL }

// Accel reads the last `library=` from Ollama's server log. There is no HTTP
// field for it; the log is how the runtime names CUDA vs Vulkan vs Metal.
func (c *Client) Accel(ctx context.Context) string {
	if err := ctx.Err(); err != nil {
		return ""
	}
	b, err := boundedio.ReadTail(serverLogPath(), maxServerLogTail)
	if err != nil {
		return ""
	}
	all := serverLibraryPattern.FindAllStringSubmatch(string(b), -1)
	if len(all) == 0 {
		return ""
	}
	return all[len(all)-1][1]
}

func serverLogPath() string {
	if runtime.GOOS == "windows" {
		base := os.Getenv("LOCALAPPDATA")
		if base == "" {
			return ""
		}
		return filepath.Join(base, "Ollama", "server.log")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".ollama", "logs", "server.log")
}

// Pull downloads a model, streaming progress lines. Ollama pulls GGUFs
// straight from Hugging Face when the name is hf.co/{user}/{repo}[:quant],
// so "point fitr at an HF link" is this plus name normalization.
func (c *Client) Pull(ctx context.Context, model string, progress func(status string, pct int)) error {
	resp, err := c.post(ctx, "/api/pull", map[string]any{"model": model, "stream": true})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nativeHTTPError(resp)
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), maxNativeLine)
	var totalBytes, frames int
	terminal, sizeChecked := false, false
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		totalBytes += len(line) + 1
		frames++
		if totalBytes > maxNativeStream || frames > maxNativeFrames {
			return errors.New("ollama pull stream exceeds protocol limits")
		}
		if terminal {
			return errors.New("ollama pull stream contains data after the terminal frame")
		}
		var p struct {
			Status    string `json:"status"`
			Error     string `json:"error"`
			Total     int64  `json:"total"`
			Completed int64  `json:"completed"`
		}
		if err := decodeJSONFrame(line, &p); err != nil {
			return fmt.Errorf("ollama pull frame: %w", err)
		}
		if p.Error != "" {
			return fmt.Errorf("pull: %s", p.Error)
		}
		if p.Total < 0 || p.Completed < 0 || (p.Total > 0 && p.Completed > p.Total) {
			return errors.New("ollama pull frame contains invalid progress")
		}
		if p.Status == "success" {
			terminal = true
		}
		// The first frame carrying a size is the first moment the download's
		// cost is knowable. Check it then, and abandon before writing tens of
		// gigabytes rather than after filling the volume.
		//
		// Ollama reports Total per layer, not for the whole model, so this is a
		// floor on the requirement and not the requirement. That is the right
		// direction to be wrong in: it catches the case that matters, a pull
		// far larger than the room left, and never invents a total it was not
		// given.
		if !sizeChecked && p.Total > 0 {
			sizeChecked = true
			if err := c.checkPullRoom(p.Total); err != nil {
				return err
			}
		}
		if progress != nil && p.Status != "" {
			pct := -1
			if p.Total > 0 {
				pct = int(100 * (float64(p.Completed) / float64(p.Total)))
			}
			progress(p.Status, pct)
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("ollama pull stream: %w", err)
	}
	if !terminal {
		return errors.New("ollama pull stream ended before a success receipt")
	}
	return nil
}

// modelStoreDir is where the runtime writes weights, which is the volume that
// matters -- not fitr's own results directory, and not the working directory.
func modelStoreDir() string {
	if d := os.Getenv("OLLAMA_MODELS"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".ollama", "models")
}

// checkPullRoom refuses a download that would not leave the volume its
// headroom.
//
// fitr had no disk awareness at all: `fitr run <model> --pull` streamed
// gigabytes with no idea how much room was left, and a pasted Hugging Face
// reference pulls without even asking for --pull. A measurement tool that
// bricks the machine it was measuring has not made an honest trade.
//
// An unreadable free-space figure does not block the pull. fitr does not
// invent numbers and must not act on one it failed to read.
func (c *Client) checkPullRoom(want int64) error {
	dir := modelStoreDir()
	if dir == "" || want <= 0 {
		return nil
	}
	// The leaf may not exist yet on a first pull; the volume still answers.
	probe := dir
	for range 4 {
		if _, err := os.Stat(probe); err == nil {
			break
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			return nil
		}
		probe = parent
	}
	ok, free, known := diskspace.Fits(probe, uint64(want))
	if !known || ok {
		return nil
	}
	return fmt.Errorf(
		"pull needs at least %.1f GB but %s has %.1f GB free; "+
			"free space or set OLLAMA_MODELS to a larger volume",
		float64(want)/(1<<30), probe, float64(free)/(1<<30))
}

// Version asks the server, falling back to the CLI. The server's answer wins:
// the fingerprint must describe the process that served the tokens, and that
// frequently differs from whatever binary happens to be first on PATH.
func (c *Client) Version(ctx context.Context) string {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, reqErr := http.NewRequestWithContext(cctx, http.MethodGet, c.BaseURL+"/api/version", nil)
	if reqErr == nil {
		if resp, err := c.HTTP.Do(req); err == nil {
			defer resp.Body.Close()
			var v struct {
				Version string `json:"version"`
			}
			if resp.StatusCode == http.StatusOK && decodeBoundedJSON(resp.Body, &v) == nil && v.Version != "" {
				return v.Version
			}
		}
	}
	out, err := exec.CommandContext(cctx, "ollama", "--version").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(strings.ReplaceAll(string(out), "ollama version is ", ""))
}

func (c *Client) Reachable(ctx context.Context) bool {
	cctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, c.BaseURL+"/api/tags", nil)
	if err != nil {
		return false
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func round(v float64, places int) float64 {
	p := 1.0
	for range places {
		p *= 10
	}
	return float64(int64(v*p+0.5)) / p
}
