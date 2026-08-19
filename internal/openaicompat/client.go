// Package openaicompat adapts any OpenAI-compatible server - LM Studio, vLLM,
// SGLang, and the rest of the ~10-of-12 local runtimes that speak this shape -
// to the llm.Backend interface.
//
// What this backend can and cannot measure, honestly:
//
//   - Timings are CLIENT-DERIVED. The OpenAI surface exposes token counts
//     (usage) but no server-side timings, so decode tok/s is computed from
//     wall-clock between first and last token, and prefill tok/s from
//     prompt_tokens over TTFT. On a local, single-user server those are close
//     to the truth; they are still not the server's own numbers, unlike the
//     Ollama and llama-server backends.
//   - Capabilities cannot be read. There is no /props here, so tool support
//     is claimed optimistically and the plumbing diagnostic - which exists
//     for exactly this situation - determines reality before any tools
//     verdict is issued.
//   - Resident memory is not reported; memory-based needs SKIP.
package openaicompat

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

// DefaultURL is LM Studio's default port - the most common OpenAI-compatible
// local server that is neither Ollama nor llama-server.
const DefaultURL = "http://127.0.0.1:1234"

type Client struct {
	BaseURL string
	HTTP    *http.Client
}

var _ llm.Backend = (*Client)(nil)

func New() *Client {
	base := os.Getenv("FITR_OPENAI_URL")
	if base == "" {
		base = DefaultURL
	}
	return &Client{
		BaseURL: strings.TrimRight(base, "/"),
		HTTP:    &http.Client{Timeout: 60 * time.Minute},
	}
}

func (c *Client) Name() string { return "openai" }
func (c *Client) URL() string  { return c.BaseURL }

// Accel is unknown on the generic OpenAI surface; returning empty is the
// honest answer (design rule 6). vLLM's /version does not name the GPU API.
func (c *Client) Accel(ctx context.Context) string { return "" }

func (c *Client) Reachable(ctx context.Context) bool {
	cctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(cctx, "GET", c.BaseURL+"/v1/models", nil)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == 200
}

// Version tries vLLM's /version, then falls back to the generic label - the
// OpenAI surface standardizes no version endpoint.
func (c *Client) Version(ctx context.Context) string {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(cctx, "GET", c.BaseURL+"/version", nil)
	if resp, err := c.HTTP.Do(req); err == nil {
		defer resp.Body.Close()
		var v struct {
			Version string `json:"version"`
		}
		if resp.StatusCode == 200 && json.NewDecoder(resp.Body).Decode(&v) == nil && v.Version != "" {
			return "openai-compat " + v.Version
		}
	}
	return "openai-compat"
}

func (c *Client) Tags(ctx context.Context) ([]ollama.ModelInfo, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", c.BaseURL+"/v1/models", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var r struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, err
	}
	out := make([]ollama.ModelInfo, 0, len(r.Data))
	for _, m := range r.Data {
		out = append(out, ollama.ModelInfo{
			Name: m.ID, Capabilities: []string{"completion", "tools"},
		})
	}
	return out, nil
}

// Show claims tools optimistically: the OpenAI surface has no capability
// endpoint, and the plumbing diagnostic exists to turn an optimistic claim
// into a verified one before any tools verdict is issued. Vision is NOT
// claimed - an unverifiable positive would turn n/a into a false PASS.
func (c *Client) Show(ctx context.Context, model string) (ollama.ModelInfo, error) {
	return ollama.ModelInfo{Name: model, Capabilities: []string{"completion", "tools"}}, nil
}

// PS cannot know what is resident; memory-based needs SKIP on this backend.
func (c *Client) PS(ctx context.Context) ([]ollama.RunningModel, error) { return nil, nil }

// StopAll is a no-op: this surface has no unload verb. The measurement
// caveat that implies is on the user's server config, and doctor's config
// check is the place that says so.
func (c *Client) StopAll(ctx context.Context) ([]string, error) { return nil, nil }

// ---------------------------------------------------------------- generate
type completionsChunk struct {
	Choices []struct {
		Text         string `json:"text"`
		FinishReason string `json:"finish_reason"`
		Delta        struct {
			Content string `json:"content"`
		} `json:"delta"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

// Generate streams /v1/completions (raw prompt, the honest analog of the
// other backends' generate paths), falling back to /v1/chat/completions for
// chat-only servers. stream_options.include_usage asks for token counts in
// the final chunk; both vLLM and LM Studio honor it.
func (c *Client) Generate(ctx context.Context, model, prompt string, s ollama.Sampling) (string, ollama.Metrics, error) {
	payload := map[string]any{
		"model": model, "prompt": prompt, "stream": true,
		"max_tokens": s.NumPredict, "temperature": s.Temperature,
		"top_k": s.TopK, "seed": s.Seed, "repeat_penalty": s.RepeatPenalty,
		"stream_options": map[string]bool{"include_usage": true},
	}
	if s.Format == "json" {
		payload["response_format"] = map[string]string{"type": "json_object"}
	}
	start := time.Now()
	resp, err := c.post(ctx, "/v1/completions", payload)
	if err != nil {
		return "", ollama.Metrics{}, err
	}
	if resp.StatusCode == 404 || resp.StatusCode == 405 {
		resp.Body.Close()
		return c.generateViaChat(ctx, model, prompt, s, start)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", ollama.Metrics{}, httpError(resp)
	}
	return c.consumeStream(resp, start, func(ch completionsChunk) (string, string) {
		if len(ch.Choices) == 0 {
			return "", ""
		}
		return ch.Choices[0].Text, ch.Choices[0].FinishReason
	})
}

func (c *Client) generateViaChat(ctx context.Context, model, prompt string, s ollama.Sampling, start time.Time) (string, ollama.Metrics, error) {
	payload := map[string]any{
		"model": model, "messages": []map[string]string{{"role": "user", "content": prompt}},
		"stream": true, "max_tokens": s.NumPredict, "temperature": s.Temperature,
		"top_k": s.TopK, "seed": s.Seed, "repeat_penalty": s.RepeatPenalty,
		"stream_options": map[string]bool{"include_usage": true},
	}
	if s.Format == "json" {
		payload["response_format"] = map[string]string{"type": "json_object"}
	}
	resp, err := c.post(ctx, "/v1/chat/completions", payload)
	if err != nil {
		return "", ollama.Metrics{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", ollama.Metrics{}, httpError(resp)
	}
	return c.consumeStream(resp, start, func(ch completionsChunk) (string, string) {
		if len(ch.Choices) == 0 {
			return "", ""
		}
		return ch.Choices[0].Delta.Content, ch.Choices[0].FinishReason
	})
}

// consumeStream reads SSE chunks, assembling text and deriving the metrics
// the surface does not provide: decode tok/s from wall-clock between first
// and last token, prefill tok/s from prompt tokens over TTFT.
func (c *Client) consumeStream(resp *http.Response, start time.Time,
	pick func(completionsChunk) (text, finish string)) (string, ollama.Metrics, error) {

	var sb strings.Builder
	var ttft float64
	var lastTok time.Time
	var usagePrompt, usageCompletion int
	finish := ""

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		line = bytes.TrimPrefix(line, []byte("data: "))
		if len(line) == 0 || bytes.Equal(line, []byte("[DONE]")) {
			continue
		}
		var ch completionsChunk
		if err := json.Unmarshal(line, &ch); err != nil {
			continue
		}
		text, fr := pick(ch)
		if text != "" {
			if ttft == 0 {
				ttft = time.Since(start).Seconds()
			}
			lastTok = time.Now()
			sb.WriteString(text)
		}
		if fr != "" {
			finish = fr
		}
		if ch.Usage != nil {
			usagePrompt, usageCompletion = ch.Usage.PromptTokens, ch.Usage.CompletionTokens
		}
	}
	if err := sc.Err(); err != nil {
		return sb.String(), ollama.Metrics{}, err
	}

	m := ollama.Metrics{
		TTFTSeconds:  round(ttft, 3),
		WallSeconds:  round(time.Since(start).Seconds(), 2),
		EvalCount:    usageCompletion,
		PromptTokens: usagePrompt,
		DoneReason:   finish,
		Truncated:    finish == "length",
	}
	if decode := lastTok.Sub(start).Seconds() - ttft; decode > 0 && usageCompletion > 1 {
		m.DecodeTPS = round(float64(usageCompletion-1)/decode, 2)
	}
	if ttft > 0 && usagePrompt > 0 {
		m.PrefillTPS = round(float64(usagePrompt)/ttft, 2)
	}
	return sb.String(), m, nil
}

// ---------------------------------------------------------------- chat
func (c *Client) Chat(ctx context.Context, model string, msgs []ollama.Message, tools []ollama.Tool, s ollama.Sampling) (ollama.Message, ollama.Metrics, error) {
	start := time.Now()
	resp, err := c.post(ctx, "/v1/chat/completions", oai.ChatPayload(model, msgs, tools, s))
	if err != nil {
		return ollama.Message{}, ollama.Metrics{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return ollama.Message{}, ollama.Metrics{}, httpError(resp)
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
		return ollama.Message{}, ollama.Metrics{}, fmt.Errorf("openai-compat: no choices in response")
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

func httpError(resp *http.Response) error {
	buf := new(bytes.Buffer)
	buf.ReadFrom(resp.Body)
	return fmt.Errorf("openai-compat %d: %s", resp.StatusCode, strings.TrimSpace(buf.String()))
}

func round(v float64, places int) float64 {
	p := 1.0
	for range places {
		p *= 10
	}
	return float64(int64(v*p+0.5)) / p
}
