package openaicompat

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/blisspixel/fitr/internal/strictjson"
)

const (
	maxErrorBody       = 64 << 10
	maxSuccessBody     = 16 << 20
	maxSSELine         = 1 << 20
	maxSSEEvent        = 2 << 20
	maxSSEEvents       = 1_000_000
	maxSSETotal        = 64 << 20
	maxGeneratedOutput = 8 << 20
)

// CredentialMode decides whether a client may load bearer credentials from
// the environment. Discovery clients use CredentialsDisabled so probing an
// operator-supplied endpoint can never disclose a configured key.
type CredentialMode uint8

const (
	CredentialsDisabled CredentialMode = iota
	CredentialsFromEnvironment
)

// APIError preserves the parts of an OpenAI error response that determine
// remediation without retaining credentials or an unbounded response body.
type APIError struct {
	StatusCode    int
	Code          string
	Type          string
	Param         string
	Message       string
	RequestID     string
	RetryAfter    string
	BodyTruncated bool
}

func (e *APIError) Error() string {
	if e == nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "openai-compat HTTP %d", e.StatusCode)
	if e.Code != "" {
		fmt.Fprintf(&b, " code=%s", e.Code)
	}
	if e.Type != "" {
		fmt.Fprintf(&b, " type=%s", e.Type)
	}
	if e.Param != "" {
		fmt.Fprintf(&b, " param=%s", e.Param)
	}
	if e.Message != "" {
		fmt.Fprintf(&b, ": %s", e.Message)
	}
	if e.BodyTruncated {
		b.WriteString(" (response body truncated)")
	}
	if e.RequestID != "" {
		fmt.Fprintf(&b, " [request_id=%s]", e.RequestID)
	}
	if e.RetryAfter != "" {
		fmt.Fprintf(&b, " [retry_after=%s]", e.RetryAfter)
	}
	return b.String()
}

type wireError struct {
	Code    string          `json:"code"`
	Type    string          `json:"type"`
	Param   json.RawMessage `json:"param"`
	Message string          `json:"message"`
}

func envAPIKey(baseURL string) string {
	if raw := os.Getenv("FITR_OPENAI_API_KEY"); raw != "" {
		if strings.ContainsAny(raw, "\r\n") {
			return raw
		}
		return strings.TrimSpace(raw)
	}
	if !isOfficialOpenAIEndpoint(baseURL) {
		return ""
	}
	raw := os.Getenv("OPENAI_API_KEY")
	if strings.ContainsAny(raw, "\r\n") {
		return raw
	}
	return strings.TrimSpace(raw)
}

func isOfficialOpenAIEndpoint(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil || !strings.EqualFold(u.Scheme, "https") {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host == "api.openai.com" || strings.HasSuffix(host, ".api.openai.com")
}

func bearerTransportSafe(u *url.URL) bool {
	if strings.EqualFold(u.Scheme, "https") {
		return true
	}
	if !strings.EqualFold(u.Scheme, "http") {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func parseBaseURL(rawURL string) (*url.URL, string, error) {
	rawURL = strings.TrimSpace(rawURL)
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, "", fmt.Errorf("openai-compat endpoint: %w", err)
	}
	if u.Scheme == "" || u.Host == "" {
		return nil, "", errors.New("openai-compat endpoint must be an absolute HTTP URL")
	}
	if !strings.EqualFold(u.Scheme, "http") && !strings.EqualFold(u.Scheme, "https") {
		return nil, "", errors.New("openai-compat endpoint scheme must be HTTP or HTTPS")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, "", errors.New("openai-compat endpoint must not contain credentials, a query, or a fragment")
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = ""
	return u, strings.TrimRight(u.String(), "/"), nil
}

func urlOrigin(u *url.URL) string {
	if u == nil {
		return ""
	}
	port := u.Port()
	if port == "" {
		switch strings.ToLower(u.Scheme) {
		case "http":
			port = "80"
		case "https":
			port = "443"
		}
	}
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Hostname()) + ":" + port
}

func cloneHTTPClient(src *http.Client, origin string, hasCredential bool) *http.Client {
	if src == nil {
		src = &http.Client{Timeout: 60 * time.Minute}
	}
	clone := *src
	switch transport := src.Transport.(type) {
	case nil:
		if base, ok := http.DefaultTransport.(*http.Transport); ok {
			clone.Transport = base.Clone()
		}
	case *http.Transport:
		clone.Transport = transport.Clone()
	}
	prior := src.CheckRedirect
	clone.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if urlOrigin(req.URL) != origin {
			return errors.New("openai-compat refuses a redirect to a different origin")
		}
		if hasCredential && !bearerTransportSafe(req.URL) {
			return errors.New("openai-compat refuses a bearer credential redirect over an unsafe transport")
		}
		if prior != nil {
			return prior(req, via)
		}
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		return nil
	}
	return &clone
}

func (c *Client) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	if c.configErr != nil {
		return nil, c.configErr
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, err
	}
	if c.apiKey != "" {
		if strings.ContainsAny(c.apiKey, "\r\n") {
			return nil, errors.New("openai-compat API key contains a line break")
		}
		if !bearerTransportSafe(req.URL) {
			return nil, errors.New("openai-compat refuses to send a bearer credential over a non-loopback, non-HTTPS endpoint")
		}
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	return req, nil
}

func (c *Client) do(req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, errors.New("openai-compat request is nil")
	}
	if urlOrigin(req.URL) != c.origin {
		return nil, errors.New("openai-compat request origin does not match the configured endpoint")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.Request == nil || urlOrigin(resp.Request.URL) != c.origin {
		resp.Body.Close()
		return nil, errors.New("openai-compat response arrived from a different origin")
	}
	return resp, nil
}

func (c *Client) responseError(resp *http.Response) error {
	body, truncated, readErr := readBounded(resp.Body, maxErrorBody)
	if readErr != nil {
		return fmt.Errorf("openai-compat HTTP %d: read error response: %w", resp.StatusCode, readErr)
	}
	return c.apiError(resp.StatusCode, resp.Header, body, truncated)
}

func readBounded(r io.Reader, limit int64) ([]byte, bool, error) {
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, false, err
	}
	if int64(len(b)) > limit {
		return b[:limit], true, nil
	}
	return b, false, nil
}

func decodeOneJSON(r io.Reader, into any) error {
	body, truncated, err := readBounded(r, maxSuccessBody)
	if err != nil {
		return err
	}
	if truncated {
		return fmt.Errorf("successful JSON response exceeds %d bytes", maxSuccessBody)
	}
	if err := strictjson.Validate(body); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	if err := dec.Decode(into); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("content after the JSON value")
		}
		return err
	}
	return nil
}

// VerifyModelDigest promotes an endpoint-reported digest only when it agrees
// with the independent operator pin captured at client construction.
func (c *Client) VerifyModelDigest(model, reported string) (string, error) {
	if strings.TrimSpace(model) == "" {
		return "", errors.New("openai-compat model identity requires a resolved model name")
	}
	if c.modelSHA256 == "" {
		return "", errors.New("openai-compat model identity requires FITR_OPENAI_MODEL_SHA256")
	}
	if strings.TrimSpace(reported) == "" {
		return "", errors.New("openai-compat endpoint did not report a model digest to match against FITR_OPENAI_MODEL_SHA256")
	}
	digest, err := normalizeModelDigest(reported)
	if err != nil {
		return "", fmt.Errorf("openai-compat reported model digest: %w", err)
	}
	if digest != c.modelSHA256 {
		return "", errors.New("openai-compat reported model digest does not match FITR_OPENAI_MODEL_SHA256")
	}
	return c.modelSHA256, nil
}

func (c *Client) apiError(status int, header http.Header, body []byte, truncated bool) *APIError {
	e := &APIError{
		StatusCode: status, RequestID: header.Get("X-Request-Id"),
		RetryAfter: header.Get("Retry-After"), BodyTruncated: truncated,
	}
	var envelope struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(body, &envelope) == nil && len(envelope.Error) > 0 && string(envelope.Error) != "null" {
		var detail wireError
		if json.Unmarshal(envelope.Error, &detail) == nil {
			e.Code, e.Type, e.Message = detail.Code, detail.Type, detail.Message
			e.Param = rawParam(detail.Param)
		} else {
			_ = json.Unmarshal(envelope.Error, &e.Message)
		}
	}
	if e.Message == "" {
		var detail wireError
		if json.Unmarshal(body, &detail) == nil && detail.Type == "error" {
			e.Code, e.Type, e.Message = detail.Code, detail.Type, detail.Message
			e.Param = rawParam(detail.Param)
		}
	}
	if e.Message == "" {
		e.Message = strings.TrimSpace(string(body))
	}
	e.Code = redact(e.Code, c.apiKey)
	e.Type = redact(e.Type, c.apiKey)
	e.Param = redact(e.Param, c.apiKey)
	e.Message = redact(e.Message, c.apiKey)
	e.RequestID = redact(e.RequestID, c.apiKey)
	e.RetryAfter = redact(e.RetryAfter, c.apiKey)
	return e
}

func (c *Client) payloadError(body []byte, header http.Header) *APIError {
	var envelope struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(body, &envelope) == nil && len(envelope.Error) > 0 && string(envelope.Error) != "null" {
		return c.apiError(http.StatusOK, header, body, false)
	}
	var detail wireError
	if json.Unmarshal(body, &detail) == nil && detail.Type == "error" && detail.Message != "" {
		return c.apiError(http.StatusOK, header, body, false)
	}
	return nil
}

func rawParam(raw json.RawMessage) string {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return strings.TrimSpace(string(raw))
}

func redact(s, secret string) string {
	if secret == "" {
		return s
	}
	return strings.ReplaceAll(s, secret, "[redacted]")
}

func normalizeModelDigest(digest string) (string, error) {
	digest = strings.ToLower(strings.TrimSpace(digest))
	if digest == "" {
		return "", nil
	}
	hexDigest := digest
	if strings.HasPrefix(hexDigest, "sha256:") {
		hexDigest = strings.TrimPrefix(hexDigest, "sha256:")
	} else if strings.Contains(hexDigest, ":") {
		return "", fmt.Errorf("unsupported model digest %q", digest)
	}
	if len(hexDigest) != 64 {
		return "", errors.New("model digest must contain 64 SHA-256 hex characters")
	}
	if _, err := hex.DecodeString(hexDigest); err != nil {
		return "", fmt.Errorf("model digest is not valid SHA-256 hex: %w", err)
	}
	return "sha256:" + hexDigest, nil
}
