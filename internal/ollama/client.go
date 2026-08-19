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
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

const DefaultURL = "http://127.0.0.1:11434"

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
		// Long timeout: a 20-turn agentic run on a slow local model is legitimately
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
	// Truncated means the model hit the token cap. Worth scoring as a failure:
	// roughly 92% of truncations are repetition loops wearing a cap.
	Truncated bool `json:"truncated"`
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
	req, err := http.NewRequestWithContext(ctx, "POST", c.BaseURL+path, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.HTTP.Do(req)
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
	if resp.StatusCode != 200 {
		buf := new(bytes.Buffer)
		buf.ReadFrom(resp.Body)
		return "", Metrics{}, fmt.Errorf("ollama %d: %s", resp.StatusCode,
			strings.TrimSpace(buf.String()))
	}

	var sb strings.Builder
	var ttft float64
	var final genResp
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var g genResp
		if err := json.Unmarshal(line, &g); err != nil {
			continue
		}
		if g.Response != "" && ttft == 0 {
			ttft = time.Since(start).Seconds()
		}
		sb.WriteString(g.Response)
		if g.Done {
			final = g
		}
	}
	if err := sc.Err(); err != nil {
		return sb.String(), Metrics{}, err
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
	Function struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"function"`
}

type Message struct {
	Role      string     `json:"role"`
	Content   string     `json:"content"`
	ToolName  string     `json:"tool_name,omitempty"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
}

type chatResp struct {
	Message Message `json:"message"`
	Done    bool    `json:"done"`
}

func (c *Client) Chat(ctx context.Context, model string, msgs []Message, tools []Tool, s Sampling) (Message, error) {
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
	resp, err := c.post(ctx, "/api/chat", payload)
	if err != nil {
		return Message{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		buf := new(bytes.Buffer)
		buf.ReadFrom(resp.Body)
		return Message{}, fmt.Errorf("ollama %d: %s", resp.StatusCode,
			strings.TrimSpace(buf.String()))
	}
	var r chatResp
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return Message{}, err
	}
	return r.Message, nil
}

// ---------------------------------------------------------------- inspection
type ModelInfo struct {
	Name         string   `json:"name"`
	Size         int64    `json:"size"`
	Capabilities []string `json:"capabilities"`
	Details      struct {
		ParameterSize     string `json:"parameter_size"`
		QuantizationLevel string `json:"quantization_level"`
		Family            string `json:"family"`
	} `json:"details"`
}

func (c *Client) Tags(ctx context.Context) ([]ModelInfo, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", c.BaseURL+"/api/tags", nil)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var r struct {
		Models []ModelInfo `json:"models"`
	}
	return r.Models, json.NewDecoder(resp.Body).Decode(&r)
}

// Show returns capabilities for one model. `capabilities` is authoritative for
// whether a model supports thinking/tools/vision -- far better than matching on
// name prefixes, which mistakes qwen3-coder (instruct) for a thinking model.
func (c *Client) Show(ctx context.Context, model string) (ModelInfo, error) {
	resp, err := c.post(ctx, "/api/show", map[string]string{"model": model})
	if err != nil {
		return ModelInfo{}, err
	}
	defer resp.Body.Close()
	var mi ModelInfo
	err = json.NewDecoder(resp.Body).Decode(&mi)
	mi.Name = model
	return mi, err
}

type RunningModel struct {
	Name      string `json:"name"`
	Size      int64  `json:"size"`
	SizeVRAM  int64  `json:"size_vram"`
	ExpiresAt string `json:"expires_at"`
}

func (c *Client) PS(ctx context.Context) ([]RunningModel, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", c.BaseURL+"/api/ps", nil)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var r struct {
		Models []RunningModel `json:"models"`
	}
	return r.Models, json.NewDecoder(resp.Body).Decode(&r)
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
	for range 8 {
		live, err := c.Resident(ctx)
		if err != nil {
			return nil, err
		}
		if len(live) == 0 {
			return nil, nil
		}
		for _, m := range live {
			resp, err := c.post(ctx, "/api/generate", map[string]any{
				"model": m.Name, "prompt": "", "keep_alive": 0,
			})
			if err == nil {
				resp.Body.Close()
			}
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(1500 * time.Millisecond):
		}
	}
	live, _ := c.Resident(ctx)
	var names []string
	for _, m := range live {
		names = append(names, m.Name)
	}
	return names, nil
}

func (c *Client) Reachable(ctx context.Context) bool {
	cctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(cctx, "GET", c.BaseURL+"/api/tags", nil)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == 200
}

func round(v float64, places int) float64 {
	p := 1.0
	for range places {
		p *= 10
	}
	return float64(int64(v*p+0.5)) / p
}
