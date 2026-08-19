// Package llamaserver adapts llama.cpp's llama-server to the llm.Backend
// interface.
//
// Why this backend is worth having at all: llama-server exposes measurement
// surfaces Ollama structurally does not - per-request timings with cached
// token counts (the only honest cold/warm prefill split), template capability
// reporting via /props, and the full OpenAI tool-calling surface. It also
// covers everyone running llama.cpp directly instead of through a wrapper.
//
// Native endpoints are used where they carry more measurement truth
// (/completion for generation timings, /props for capabilities); the OpenAI
// surface (/v1/chat/completions) is used for tool calling, because that is
// the code path every real agent framework exercises against this server.
package llamaserver

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

	"github.com/blisspixel/fitr/internal/llm"
	"github.com/blisspixel/fitr/internal/oai"
	"github.com/blisspixel/fitr/internal/ollama"
)

const DefaultURL = "http://127.0.0.1:8080"

type Client struct {
	BaseURL string
	HTTP    *http.Client
}

var _ llm.Backend = (*Client)(nil)

func New() *Client {
	base := os.Getenv("LLAMA_SERVER_URL")
	if base == "" {
		base = DefaultURL
	}
	return &Client{
		BaseURL: strings.TrimRight(base, "/"),
		// Same reasoning as the Ollama client: a 40-turn agentic run on a slow
		// local model is legitimately slow; cancellation is the caller's job.
		HTTP: &http.Client{Timeout: 60 * time.Minute},
	}
}

func (c *Client) Name() string { return "llama-server" }
func (c *Client) URL() string  { return c.BaseURL }

// Accel returns the raw build/system/device string the server exposes.
// device.Detect maps it onto cuda|metal|vulkan|rocm|...; we do not guess
// from the model's name.
func (c *Client) Accel(ctx context.Context) string {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	p, err := c.props(cctx)
	if err != nil {
		return ""
	}
	var bits []string
	if p.BuildInfo != "" {
		bits = append(bits, p.BuildInfo)
	}
	if p.SystemInfo != "" {
		bits = append(bits, p.SystemInfo)
	}
	for _, d := range p.Devices {
		bits = append(bits, d.Backend, d.Device, d.Name, d.Type)
	}
	return strings.TrimSpace(strings.Join(bits, " "))
}

func (c *Client) Reachable(ctx context.Context) bool {
	cctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(cctx, "GET", c.BaseURL+"/health", nil)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == 200
}

// props is the subset of /props the harness reads. Fields are optional across
// llama.cpp builds; absent ones simply stay zero-valued.
type props struct {
	BuildInfo    string `json:"build_info"`
	ModelPath    string `json:"model_path"`
	ChatTemplate string `json:"chat_template"`
	Modalities   struct {
		Vision bool `json:"vision"`
		Audio  bool `json:"audio"`
	} `json:"modalities"`
	ChatTemplateCaps map[string]bool `json:"chat_template_caps"`
	SystemInfo       string          `json:"system_info"`
	DefaultSettings  struct {
		NCtx int `json:"n_ctx"`
	} `json:"default_generation_settings"`
	Devices []struct {
		Name    string `json:"name"`
		Type    string `json:"type"`
		Backend string `json:"backend"`
		Device  string `json:"device"`
	} `json:"devices"`
}

func (c *Client) props(ctx context.Context) (props, error) {
	var p props
	req, err := http.NewRequestWithContext(ctx, "GET", c.BaseURL+"/props", nil)
	if err != nil {
		return p, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return p, err
	}
	defer resp.Body.Close()
	return p, json.NewDecoder(resp.Body).Decode(&p)
}

// Version reports the llama.cpp build, prefixed with the runtime name so a
// fingerprint reads unambiguously next to Ollama ones.
func (c *Client) Version(ctx context.Context) string {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	p, err := c.props(cctx)
	if err != nil || p.BuildInfo == "" {
		return "llama-server"
	}
	return "llama-server " + p.BuildInfo
}

func modelName(p props) string {
	path := strings.ReplaceAll(p.ModelPath, "\\", "/")
	if i := strings.LastIndex(path, "/"); i >= 0 {
		path = path[i+1:]
	}
	return path
}

// Tags lists the served model. llama-server serves exactly one.
func (c *Client) Tags(ctx context.Context) ([]ollama.ModelInfo, error) {
	p, err := c.props(ctx)
	if err != nil {
		return nil, err
	}
	mi := c.infoFromProps(p)
	return []ollama.ModelInfo{mi}, nil
}

// Show reports capabilities read from the endpoint, never guessed from the
// model's name - the capability probe is the whole point. When the build
// exposes chat_template_caps that answer is authoritative; otherwise the
// template is scanned for tool markers, which is a heuristic and labeled so
// by its position here.
func (c *Client) Show(ctx context.Context, model string) (ollama.ModelInfo, error) {
	p, err := c.props(ctx)
	if err != nil {
		return ollama.ModelInfo{Name: model}, err
	}
	mi := c.infoFromProps(p)
	mi.Name = model
	return mi, nil
}

func (c *Client) infoFromProps(p props) ollama.ModelInfo {
	caps := []string{"completion"}
	if v, ok := p.ChatTemplateCaps["supports_tools"]; ok {
		if v {
			caps = append(caps, "tools")
		}
	} else if strings.Contains(p.ChatTemplate, "tool") {
		caps = append(caps, "tools")
	}
	if p.Modalities.Vision {
		caps = append(caps, "vision")
	}
	return ollama.ModelInfo{Name: modelName(p), Path: p.ModelPath, Capabilities: caps}
}

// PS reports the single resident model. Sizes are zero: llama-server does not
// report resident bytes over HTTP, and a made-up number would be worse than a
// gap - memory-based needs correctly SKIP on this backend.
func (c *Client) PS(ctx context.Context) ([]ollama.RunningModel, error) {
	p, err := c.props(ctx)
	if err != nil {
		return nil, err
	}
	return []ollama.RunningModel{{Name: modelName(p)}}, nil
}

// StopAll is a no-op: a llama-server process serves one model for its whole
// lifetime, so the one-model-resident invariant holds by construction.
func (c *Client) StopAll(ctx context.Context) ([]string, error) { return nil, nil }

// ---------------------------------------------------------------- generate
type completionResp struct {
	Content      string `json:"content"`
	Stop         bool   `json:"stop"`
	StopType     string `json:"stop_type"`
	Truncated    bool   `json:"truncated"`
	TokensCached int    `json:"tokens_cached"`
	Timings      struct {
		PromptN     int     `json:"prompt_n"`
		PromptMS    float64 `json:"prompt_ms"`
		PredictedN  int     `json:"predicted_n"`
		PredictedMS float64 `json:"predicted_ms"`
	} `json:"timings"`
}

// Generate streams the native /completion endpoint. Native rather than /v1
// because the final chunk carries server-side timings including the cached
// prompt token count - the measurement surface this backend exists for.
func (c *Client) Generate(ctx context.Context, model, prompt string, s ollama.Sampling) (string, ollama.Metrics, error) {
	payload := map[string]any{
		"prompt": prompt, "stream": true,
		"n_predict": s.NumPredict, "temperature": s.Temperature,
		"top_k": s.TopK, "seed": s.Seed, "repeat_penalty": s.RepeatPenalty,
	}
	if s.Format == "json" {
		payload["json_schema"] = map[string]any{"type": "object"}
	}
	start := time.Now()
	resp, err := c.post(ctx, "/completion", payload)
	if err != nil {
		return "", ollama.Metrics{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", ollama.Metrics{}, httpError("llama-server", resp)
	}

	var sb strings.Builder
	var ttft float64
	var final completionResp
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		// SSE frames are "data: {json}"; be tolerant of plain NDJSON too.
		line = bytes.TrimPrefix(line, []byte("data: "))
		if len(line) == 0 || bytes.Equal(line, []byte("[DONE]")) {
			continue
		}
		var g completionResp
		if err := json.Unmarshal(line, &g); err != nil {
			continue
		}
		if g.Content != "" && ttft == 0 {
			ttft = time.Since(start).Seconds()
		}
		sb.WriteString(g.Content)
		if g.Stop {
			final = g
		}
	}
	if err := sc.Err(); err != nil {
		return sb.String(), ollama.Metrics{}, err
	}

	m := ollama.Metrics{
		TTFTSeconds:  round(ttft, 3),
		WallSeconds:  round(time.Since(start).Seconds(), 2),
		EvalCount:    final.Timings.PredictedN,
		PromptTokens: final.Timings.PromptN,
		CachedTokens: final.TokensCached,
		CacheKnown:   true,
		DoneReason:   final.StopType,
		Truncated:    final.Truncated || final.StopType == "limit",
	}
	if final.Timings.PredictedMS > 0 {
		m.DecodeTPS = round(float64(final.Timings.PredictedN)/(final.Timings.PredictedMS/1000), 2)
	}
	if final.Timings.PromptMS > 0 {
		m.PrefillTPS = round(float64(final.Timings.PromptN)/(final.Timings.PromptMS/1000), 2)
	}
	return sb.String(), m, nil
}

// ---------------------------------------------------------------- chat
// Chat uses /v1/chat/completions - the code path every real agent framework
// exercises against this server, which makes it the honest one to measure.
// The wire mapping is shared with every OpenAI-shaped backend (internal/oai).
func (c *Client) Chat(ctx context.Context, model string, msgs []ollama.Message, tools []ollama.Tool, s ollama.Sampling) (ollama.Message, ollama.Metrics, error) {
	start := time.Now()
	resp, err := c.post(ctx, "/v1/chat/completions", oai.ChatPayload(model, msgs, tools, s))
	if err != nil {
		return ollama.Message{}, ollama.Metrics{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return ollama.Message{}, ollama.Metrics{}, httpError("llama-server", resp)
	}
	var r struct {
		Choices []struct {
			Message      oai.Message `json:"message"`
			FinishReason string      `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return ollama.Message{}, ollama.Metrics{}, err
	}
	if len(r.Choices) == 0 {
		return ollama.Message{}, ollama.Metrics{}, fmt.Errorf("llama-server: no choices in response")
	}
	m := ollama.Metrics{
		WallSeconds:  round(time.Since(start).Seconds(), 2),
		EvalCount:    r.Usage.CompletionTokens,
		PromptTokens: r.Usage.PromptTokens,
		DoneReason:   r.Choices[0].FinishReason,
		Truncated:    r.Choices[0].FinishReason == "length",
	}
	return oai.ToMessage(r.Choices[0].Message), m, nil
}

// ---------------------------------------------------------------- plumbing
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

func httpError(who string, resp *http.Response) error {
	buf := new(bytes.Buffer)
	buf.ReadFrom(resp.Body)
	return fmt.Errorf("%s %d: %s", who, resp.StatusCode, strings.TrimSpace(buf.String()))
}

func round(v float64, places int) float64 {
	p := 1.0
	for range places {
		p *= 10
	}
	return float64(int64(v*p+0.5)) / p
}
