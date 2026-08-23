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
	"github.com/blisspixel/fitr/internal/strictjson"
)

// DefaultURL is LM Studio's default port - the most common OpenAI-compatible
// local server that is neither Ollama nor llama-server.
const DefaultURL = "http://127.0.0.1:1234"

const controlPlaneTimeout = 15 * time.Second

type Client struct {
	baseURL     string
	origin      string
	http        *http.Client
	apiKey      string
	modelSHA256 string
	configErr   error
}

var _ llm.Backend = (*Client)(nil)
var _ llm.ModelDigestVerifier = (*Client)(nil)

func New() *Client {
	base := os.Getenv("FITR_OPENAI_URL")
	if base == "" {
		base = DefaultURL
	}
	c, err := NewAt(base, CredentialsFromEnvironment)
	if err != nil {
		return &Client{baseURL: strings.TrimRight(base, "/"), configErr: err,
			http: &http.Client{Timeout: 60 * time.Minute}}
	}
	return c
}

// NewAt binds a client to one final origin for its lifetime. Credentials are
// captured once and can be disabled for discovery of an untrusted endpoint.
func NewAt(baseURL string, credentialMode CredentialMode) (*Client, error) {
	return newAtWithHTTP(baseURL, credentialMode, &http.Client{Timeout: 60 * time.Minute})
}

func newAtWithHTTP(baseURL string, credentialMode CredentialMode, httpClient *http.Client) (*Client, error) {
	u, canonical, err := parseBaseURL(baseURL)
	if err != nil {
		return nil, err
	}
	if credentialMode != CredentialsDisabled && credentialMode != CredentialsFromEnvironment {
		return nil, fmt.Errorf("openai-compat credential mode is invalid")
	}
	key := ""
	if credentialMode == CredentialsFromEnvironment {
		key = envAPIKey(canonical)
		if strings.ContainsAny(key, "\r\n") {
			return nil, fmt.Errorf("openai-compat API key contains a line break")
		}
		if key != "" && !bearerTransportSafe(u) {
			return nil, fmt.Errorf("openai-compat refuses to send a bearer credential over a non-loopback, non-HTTPS endpoint")
		}
	}
	pin := strings.TrimSpace(os.Getenv("FITR_OPENAI_MODEL_SHA256"))
	if pin != "" {
		pin, err = normalizeModelDigest(pin)
		if err != nil {
			return nil, fmt.Errorf("FITR_OPENAI_MODEL_SHA256: %w", err)
		}
	}
	origin := urlOrigin(u)
	return &Client{
		baseURL: canonical, origin: origin,
		http:   cloneHTTPClient(httpClient, origin, key != ""),
		apiKey: key, modelSHA256: pin,
	}, nil
}

func (c *Client) Name() string { return "openai" }
func (c *Client) URL() string  { return c.baseURL }

// Accel is unknown on the generic OpenAI surface; returning empty is the
// honest answer (design rule 6). vLLM's /version does not name the GPU API.
func (c *Client) Accel(ctx context.Context) string { return "" }

func (c *Client) Reachable(ctx context.Context) bool {
	cctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	req, err := c.newRequest(cctx, http.MethodGet, "/v1/models", nil)
	if err != nil {
		return false
	}
	resp, err := c.do(req)
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
	req, err := c.newRequest(cctx, http.MethodGet, "/version", nil)
	if err != nil {
		return "openai-compat"
	}
	if resp, err := c.do(req); err == nil {
		defer resp.Body.Close()
		var v struct {
			Version string `json:"version"`
		}
		if resp.StatusCode == 200 && decodeOneJSON(resp.Body, &v) == nil && v.Version != "" {
			return "openai-compat " + v.Version
		}
	}
	return "openai-compat"
}

func (c *Client) Tags(ctx context.Context) ([]ollama.ModelInfo, error) {
	cctx, cancel := context.WithTimeout(ctx, controlPlaneTimeout)
	defer cancel()
	req, err := c.newRequest(cctx, http.MethodGet, "/v1/models", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, c.responseError(resp)
	}
	var r struct {
		Data []struct {
			ID     string `json:"id"`
			Digest string `json:"digest"`
		} `json:"data"`
	}
	if err := decodeOneJSON(resp.Body, &r); err != nil {
		return nil, fmt.Errorf("openai-compat models response: %w", err)
	}
	if r.Data == nil {
		return nil, fmt.Errorf("openai-compat models response is missing the data array")
	}
	out := make([]ollama.ModelInfo, 0, len(r.Data))
	seen := make(map[string]bool, len(r.Data))
	for _, m := range r.Data {
		if m.ID == "" || strings.TrimSpace(m.ID) != m.ID {
			return nil, fmt.Errorf("openai-compat models response contains an invalid model id %q", m.ID)
		}
		if seen[m.ID] {
			return nil, fmt.Errorf("openai-compat models response contains duplicate model id %q", m.ID)
		}
		seen[m.ID] = true
		if m.Digest != strings.TrimSpace(m.Digest) {
			return nil, fmt.Errorf("openai-compat model %q has an invalid digest", m.ID)
		}
		digest, err := normalizeModelDigest(m.Digest)
		if err != nil {
			return nil, fmt.Errorf("openai-compat model %q: %w", m.ID, err)
		}
		out = append(out, ollama.ModelInfo{
			Name: m.ID, ReportedDigest: digest, Capabilities: []string{"completion", "tools"},
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
			Refusal string `json:"refusal"`
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
	start := time.Now()
	if s.Format == "json" {
		return c.generateViaChat(ctx, model, prompt, s, start)
	}
	payload := map[string]any{
		"model": model, "prompt": prompt, "stream": true,
		"max_tokens": s.NumPredict, "temperature": s.Temperature,
		"seed":           s.Seed,
		"stream_options": map[string]bool{"include_usage": true},
	}
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
		return "", ollama.Metrics{}, c.responseError(resp)
	}
	return c.consumeStream(resp, start, func(ch completionsChunk) (string, string) {
		if len(ch.Choices) == 0 {
			return "", ""
		}
		return ch.Choices[0].Text, ch.Choices[0].FinishReason
	})
}

func (c *Client) generateViaChat(ctx context.Context, model, prompt string, s ollama.Sampling, start time.Time) (string, ollama.Metrics, error) {
	payload := oai.StrictChatPayload(model, []ollama.Message{{Role: "user", Content: prompt}}, nil, s)
	payload["stream"] = true
	payload["stream_options"] = map[string]bool{"include_usage": true}
	resp, err := c.post(ctx, "/v1/chat/completions", payload)
	if err != nil {
		return "", ollama.Metrics{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", ollama.Metrics{}, c.responseError(resp)
	}
	return c.consumeStream(resp, start, func(ch completionsChunk) (string, string) {
		if len(ch.Choices) == 0 {
			return "", ""
		}
		return ch.Choices[0].Delta.Content + ch.Choices[0].Delta.Refusal, ch.Choices[0].FinishReason
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
	seenUsage := false
	seenDone := false
	var eventData []string
	var eventBytes, eventCount, totalBytes int

	dispatch := func() error {
		if len(eventData) == 0 {
			return nil
		}
		eventCount++
		if eventCount > maxSSEEvents {
			return fmt.Errorf("openai-compat: SSE event count exceeds %d", maxSSEEvents)
		}
		data := []byte(strings.Join(eventData, "\n"))
		eventData = eventData[:0]
		eventBytes = 0
		if seenDone {
			return fmt.Errorf("openai-compat: SSE stream contains data after [DONE]")
		}
		if bytes.Equal(bytes.TrimSpace(data), []byte("[DONE]")) {
			seenDone = true
			return nil
		}
		if apiErr := c.payloadError(data, resp.Header); apiErr != nil {
			return apiErr
		}
		var ch completionsChunk
		if err := strictjson.Unmarshal(data, &ch); err != nil {
			return fmt.Errorf("openai-compat: decode SSE chunk: %w", err)
		}
		if len(ch.Choices) > 1 {
			return fmt.Errorf("openai-compat: SSE chunk contains %d choices, want at most one", len(ch.Choices))
		}
		text, fr := pick(ch)
		if text != "" {
			if len(text) > maxGeneratedOutput-sb.Len() {
				return fmt.Errorf("openai-compat: generated output exceeds %d bytes", maxGeneratedOutput)
			}
			if ttft == 0 {
				ttft = time.Since(start).Seconds()
			}
			lastTok = time.Now()
			sb.WriteString(text)
		}
		if fr != "" {
			if finish != "" && finish != fr {
				return fmt.Errorf("openai-compat: conflicting finish reasons %q and %q", finish, fr)
			}
			finish = fr
		}
		if ch.Usage != nil {
			if ch.Usage.PromptTokens < 0 || ch.Usage.CompletionTokens < 0 {
				return fmt.Errorf("openai-compat: response contains negative token usage")
			}
			if seenUsage && (usagePrompt != ch.Usage.PromptTokens || usageCompletion != ch.Usage.CompletionTokens) {
				return fmt.Errorf("openai-compat: conflicting usage receipts")
			}
			seenUsage = true
			usagePrompt, usageCompletion = ch.Usage.PromptTokens, ch.Usage.CompletionTokens
		}
		return nil
	}

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), maxSSELine)
	for sc.Scan() {
		line := strings.TrimSuffix(sc.Text(), "\r")
		totalBytes += len(line) + 1
		if totalBytes > maxSSETotal {
			return sb.String(), ollama.Metrics{}, fmt.Errorf("openai-compat: SSE stream exceeds %d bytes", maxSSETotal)
		}
		if line == "" {
			if err := dispatch(); err != nil {
				return sb.String(), ollama.Metrics{}, err
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, ok := strings.Cut(line, ":")
		if !ok || field != "data" {
			continue
		}
		value = strings.TrimPrefix(value, " ")
		nextBytes := eventBytes + len(value)
		if len(eventData) > 0 {
			nextBytes++
		}
		if nextBytes > maxSSEEvent {
			return sb.String(), ollama.Metrics{}, fmt.Errorf("openai-compat: SSE event exceeds %d bytes", maxSSEEvent)
		}
		eventData = append(eventData, value)
		eventBytes = nextBytes
	}
	if err := sc.Err(); err != nil {
		return sb.String(), ollama.Metrics{}, fmt.Errorf("openai-compat: read SSE stream: %w", err)
	}
	if len(eventData) > 0 {
		if err := dispatch(); err != nil {
			return sb.String(), ollama.Metrics{}, err
		}
	}
	if !seenDone {
		return sb.String(), ollama.Metrics{}, fmt.Errorf("openai-compat: stream ended before [DONE]")
	}
	if !seenUsage {
		return sb.String(), ollama.Metrics{}, fmt.Errorf("openai-compat: stream ended without requested usage")
	}
	if finish == "" {
		return sb.String(), ollama.Metrics{}, fmt.Errorf("openai-compat: stream ended without a finish reason")
	}

	m := ollama.Metrics{
		TTFTSeconds:   round(ttft, 3),
		WallSeconds:   round(time.Since(start).Seconds(), 2),
		EvalCount:     usageCompletion,
		PromptTokens:  usagePrompt,
		DoneReason:    finish,
		Truncated:     finish == "length",
		ClientDerived: true,
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
	resp, err := c.post(ctx, "/v1/chat/completions", oai.StrictChatPayload(model, msgs, tools, s))
	if err != nil {
		return ollama.Message{}, ollama.Metrics{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return ollama.Message{}, ollama.Metrics{}, c.responseError(resp)
	}
	var r struct {
		Error   json.RawMessage `json:"error"`
		Choices []struct {
			Message      oai.Message `json:"message"`
			FinishReason string      `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := decodeOneJSON(resp.Body, &r); err != nil {
		return ollama.Message{}, ollama.Metrics{}, err
	}
	if len(r.Error) > 0 && string(r.Error) != "null" {
		body := append([]byte(`{"error":`), r.Error...)
		body = append(body, '}')
		return ollama.Message{}, ollama.Metrics{}, c.apiError(http.StatusOK, resp.Header, body, false)
	}
	if len(r.Choices) != 1 {
		return ollama.Message{}, ollama.Metrics{}, fmt.Errorf(
			"openai-compat: response contains %d choices, want exactly one", len(r.Choices))
	}
	if r.Choices[0].Message.Role != "assistant" {
		return ollama.Message{}, ollama.Metrics{}, fmt.Errorf(
			"openai-compat: first choice has role %q, want assistant", r.Choices[0].Message.Role)
	}
	if r.Usage.PromptTokens < 0 || r.Usage.CompletionTokens < 0 {
		return ollama.Message{}, ollama.Metrics{}, fmt.Errorf(
			"openai-compat: response contains negative token usage")
	}
	m := ollama.Metrics{
		WallSeconds:   round(time.Since(start).Seconds(), 2),
		EvalCount:     r.Usage.CompletionTokens,
		PromptTokens:  r.Usage.PromptTokens,
		DoneReason:    r.Choices[0].FinishReason,
		Truncated:     r.Choices[0].FinishReason == "length",
		ClientDerived: true,
	}
	return oai.ToMessage(r.Choices[0].Message), m, nil
}

func (c *Client) post(ctx context.Context, path string, body any) (*http.Response, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := c.newRequest(ctx, http.MethodPost, path, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.do(req)
}

func round(v float64, places int) float64 {
	p := 1.0
	for range places {
		p *= 10
	}
	return float64(int64(v*p+0.5)) / p
}
