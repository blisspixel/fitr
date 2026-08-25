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
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/blisspixel/fitr/internal/llm"
	"github.com/blisspixel/fitr/internal/oai"
	"github.com/blisspixel/fitr/internal/ollama"
	"github.com/blisspixel/fitr/internal/strictjson"
)

const DefaultURL = "http://127.0.0.1:8080"

const (
	maxNativeBody       = 16 << 20
	maxNativeError      = 64 << 10
	maxNativeLine       = 1 << 20
	maxNativeStream     = 64 << 20
	maxNativeFrames     = 1_000_000
	maxGeneratedOutput  = 8 << 20
	controlPlaneTimeout = 15 * time.Second
)

type Client struct {
	BaseURL string
	HTTP    *http.Client
}

var _ llm.Backend = (*Client)(nil)
var _ llm.EffectiveContextObserver = (*Client)(nil)

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
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, c.BaseURL+"/health", nil)
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
	cctx, cancel := context.WithTimeout(ctx, controlPlaneTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, c.BaseURL+"/props", nil)
	if err != nil {
		return p, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return p, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return p, httpError("llama-server", resp)
	}
	// Sequenced deliberately: `return p, decode(&p)` leaves the order of the
	// value read and the call unspecified, so a compiler is free to return the
	// struct as it was before decoding filled it.
	if err := decodeBoundedJSON(resp.Body, &p); err != nil {
		return p, err
	}
	return p, nil
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
	if strings.TrimSpace(mi.Name) == "" {
		return nil, errors.New("llama-server props response is missing model_path")
	}
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
	name := modelName(p)
	if strings.TrimSpace(name) == "" {
		return nil, errors.New("llama-server props response is missing model_path")
	}
	if p.DefaultSettings.NCtx < 0 {
		return nil, errors.New("llama-server reported a negative effective context")
	}
	return []ollama.RunningModel{{Name: name, ContextLength: p.DefaultSettings.NCtx}}, nil
}

// EffectiveContext reads the per-slot n_ctx resolved by llama-server. Builds
// that omit the field leave it unavailable. The server is single-model, so
// the value applies to the one model loaded at process start.
func (c *Client) EffectiveContext(ctx context.Context, _ string) (int, bool, error) {
	p, err := c.props(ctx)
	if err != nil {
		return 0, false, err
	}
	if p.DefaultSettings.NCtx == 0 {
		return 0, false, nil
	}
	if p.DefaultSettings.NCtx < 0 {
		return 0, false, errors.New("llama-server reported a negative effective context")
	}
	return p.DefaultSettings.NCtx, true, nil
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
	if s.NumCtx > 0 {
		// Sent so a --ctx run is not a silent no-op. llama-server allocates
		// KV at launch; n_ctx cannot grow past --ctx-size. Extra fields are
		// ignored on builds that do not honor it.
		payload["n_ctx"] = s.NumCtx
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
	if resp.StatusCode != http.StatusOK {
		return "", ollama.Metrics{}, httpError("llama-server", resp)
	}

	var sb strings.Builder
	var ttft float64
	var final completionResp
	var totalBytes, frames int
	terminal := false
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), maxNativeLine)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 || bytes.HasPrefix(line, []byte(":")) {
			continue
		}
		totalBytes += len(line) + 1
		frames++
		if totalBytes > maxNativeStream || frames > maxNativeFrames {
			return sb.String(), ollama.Metrics{}, errors.New("llama-server completion stream exceeds protocol limits")
		}
		if terminal {
			if bytes.Equal(bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:"))), []byte("[DONE]")) {
				continue
			}
			return sb.String(), ollama.Metrics{}, errors.New("llama-server completion stream contains data after the terminal frame")
		}
		if bytes.HasPrefix(line, []byte("data:")) {
			line = bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
		} else if !bytes.HasPrefix(line, []byte("{")) {
			return sb.String(), ollama.Metrics{}, errors.New("llama-server completion stream contains an invalid frame prefix")
		}
		if bytes.Equal(line, []byte("[DONE]")) {
			return sb.String(), ollama.Metrics{}, errors.New("llama-server completion stream ended before a terminal receipt")
		}
		var g completionResp
		if err := decodeJSONFrame(line, &g); err != nil {
			return sb.String(), ollama.Metrics{}, fmt.Errorf("llama-server completion frame: %w", err)
		}
		if g.TokensCached < 0 || g.Timings.PromptN < 0 || g.Timings.PromptMS < 0 ||
			g.Timings.PredictedN < 0 || g.Timings.PredictedMS < 0 {
			return sb.String(), ollama.Metrics{}, errors.New("llama-server completion frame contains a negative metric")
		}
		if g.Content != "" && ttft == 0 {
			ttft = time.Since(start).Seconds()
		}
		if len(g.Content) > maxGeneratedOutput-sb.Len() {
			return sb.String(), ollama.Metrics{}, fmt.Errorf("llama-server generated output exceeds %d bytes", maxGeneratedOutput)
		}
		sb.WriteString(g.Content)
		if g.Stop {
			final = g
			terminal = true
		}
	}
	if err := sc.Err(); err != nil {
		return sb.String(), ollama.Metrics{}, fmt.Errorf("llama-server completion stream: %w", err)
	}
	if !terminal {
		return sb.String(), ollama.Metrics{}, errors.New("llama-server completion stream ended before a terminal receipt")
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
	if resp.StatusCode != http.StatusOK {
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
	if err := decodeBoundedJSON(resp.Body, &r); err != nil {
		return ollama.Message{}, ollama.Metrics{}, err
	}
	if len(r.Choices) != 1 {
		return ollama.Message{}, ollama.Metrics{}, fmt.Errorf(
			"llama-server: response contains %d choices, want exactly one", len(r.Choices))
	}
	if r.Choices[0].Message.Role != "assistant" {
		return ollama.Message{}, ollama.Metrics{}, fmt.Errorf(
			"llama-server: first choice has role %q, want assistant", r.Choices[0].Message.Role)
	}
	if r.Usage.PromptTokens < 0 || r.Usage.CompletionTokens < 0 {
		return ollama.Message{}, ollama.Metrics{}, errors.New("llama-server: response contains negative token usage")
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
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.HTTP.Do(req)
}

func httpError(who string, resp *http.Response) error {
	b, err := readBounded(resp.Body, maxNativeError)
	if err != nil {
		return fmt.Errorf("%s %d: %w", who, resp.StatusCode, err)
	}
	return fmt.Errorf("%s %d: %s", who, resp.StatusCode, strings.TrimSpace(string(b)))
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

func round(v float64, places int) float64 {
	p := 1.0
	for range places {
		p *= 10
	}
	return float64(int64(v*p+0.5)) / p
}
