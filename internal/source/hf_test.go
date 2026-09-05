package source

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

const sourceCommit = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type sourceTransport func(*http.Request) (*http.Response, error)

func (transport sourceTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func sourceRequest() HFRequest {
	return HFRequest{RepoID: "owner/model", Revision: "main", Files: []string{"model.gguf"}}
}

func sourceSibling(path string) hfSibling {
	size, pointer := int64(1234), int64(130)
	return hfSibling{Filename: path, Size: &size, BlobID: strings.Repeat("b", 40),
		LFS: &hfLFS{Size: &size, SHA256: strings.Repeat("c", 64), PointerSize: &pointer}}
}

func sourceBody(t *testing.T, siblings ...hfSibling) string {
	t.Helper()
	data, err := json.Marshal(hfMetadata{ID: "owner/model", SHA: sourceCommit, Siblings: siblings})
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func sourceResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), ContentLength: -1}
}

func sourceResolver(t *testing.T, bodies ...string) (*Resolver, *int) {
	t.Helper()
	calls := 0
	resolver := NewResolver(sourceTransport(func(request *http.Request) (*http.Response, error) {
		if calls >= len(bodies) {
			t.Fatal("unexpected extra metadata request")
		}
		body := bodies[calls]
		calls++
		if request.Method != http.MethodGet || request.URL.Host != "huggingface.co" || request.URL.Scheme != "https" ||
			request.URL.Query().Get("blobs") != "true" || !strings.HasPrefix(request.URL.Path, "/api/models/") {
			t.Fatalf("unexpected metadata request: %s %s", request.Method, request.URL)
		}
		for _, header := range []string{"Authorization", "Proxy-Authorization", "Cookie"} {
			if request.Header.Get(header) != "" {
				t.Fatalf("credential header %s leaked", header)
			}
		}
		return sourceResponse(http.StatusOK, body), nil
	}))
	instant := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	resolver.now = func() time.Time { instant = instant.Add(time.Millisecond); return instant }
	return resolver, &calls
}

func sourceFixture(t *testing.T) Resolution {
	t.Helper()
	body := sourceBody(t, sourceSibling("model.gguf"))
	resolver, _ := sourceResolver(t, body, body)
	result, err := resolver.ResolveHF(t.Context(), sourceRequest())
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestResolveHFPinsCommitAndKeepsObservations(t *testing.T) {
	t.Setenv("HF_TOKEN", "secret-token")
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	body := sourceBody(t, sourceSibling("model.gguf"))
	resolver, calls := sourceResolver(t, body, body)
	request := sourceRequest()
	request.Revision = "refs/pr/3"
	result, err := resolver.ResolveHF(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if *calls != 2 || result.State != "resolved" || result.ResolvedCommit != sourceCommit ||
		result.Queries[0].Revision != "refs/pr/3" || result.Queries[1].Revision != sourceCommit {
		t.Fatalf("unexpected resolution: %+v", result)
	}
	if result.Queries[0].StartedAt >= result.Queries[0].CompletedAt || result.Queries[0].CompletedAt >= result.Queries[1].StartedAt ||
		result.Queries[1].CompletedAt >= result.ObservedAt {
		t.Fatal("query timestamps were flattened")
	}
	request.Files[0] = "mutated.gguf"
	if result.Request.Files[0] != "model.gguf" {
		t.Fatal("request slice was retained")
	}
	if result.Files[0].GitBlobOID == result.Files[0].DeclaredSHA256 || !strings.HasPrefix(result.Files[0].DeclaredSHA256, "sha256:") {
		t.Fatal("Git and content hash identities were conflated")
	}
}

func TestResolveHFURLAndBranchMovement(t *testing.T) {
	body := sourceBody(t, sourceSibling("model.gguf"))
	paths := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		paths = append(paths, request.RequestURI)
		fmt.Fprint(writer, body)
	}))
	defer server.Close()
	target, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	resolver := NewResolver(sourceTransport(func(request *http.Request) (*http.Response, error) {
		copyRequest := request.Clone(request.Context())
		copyRequest.URL.Scheme, copyRequest.URL.Host = target.Scheme, target.Host
		return server.Client().Transport.RoundTrip(copyRequest)
	}))
	request := sourceRequest()
	request.Revision = "branch/x"
	result, err := resolver.ResolveHF(t.Context(), request)
	if err != nil || result.State != "resolved" {
		t.Fatalf("resolution = %+v, %v", result, err)
	}
	want := []string{"/api/models/owner/model/revision/branch%2Fx?blobs=true", "/api/models/owner/model/revision/" + sourceCommit + "?blobs=true"}
	if !slices.Equal(paths, want) {
		t.Fatalf("requests = %v", paths)
	}
}

func TestResolveHFRejectsInconsistentMetadata(t *testing.T) {
	body := sourceBody(t, sourceSibling("model.gguf"))
	cases := []struct {
		name, first, second, gap string
		calls                    int
	}{
		{"rename_first", strings.Replace(body, "owner/model", "owner/renamed", 1), "", "repository_mismatch", 1},
		{"rename_second", body, strings.Replace(body, "owner/model", "owner/renamed", 1), "repository_mismatch", 2},
		{"commit", body, strings.Replace(body, sourceCommit, strings.Repeat("e", 40), 1), "commit_mismatch", 2},
		{"size", body, strings.ReplaceAll(body, "1234", "1235"), "metadata_mismatch", 2},
		{"hash", body, strings.ReplaceAll(body, strings.Repeat("c", 64), strings.Repeat("d", 64)), "metadata_mismatch", 2},
		{"inventory", body, sourceBody(t, sourceSibling("model.gguf"), sourceSibling("other.gguf")), "metadata_mismatch", 2},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			resolver, calls := sourceResolver(t, test.first, test.second)
			result, err := resolver.ResolveHF(t.Context(), sourceRequest())
			if err != nil || result.State != "unavailable" || !slices.Equal(result.Gaps, []string{test.gap}) || *calls != test.calls {
				t.Fatalf("resolution=%+v err=%v calls=%d", result, err, *calls)
			}
		})
	}
}

func TestResolveHFIncompleteMetadata(t *testing.T) {
	cases := []struct {
		name     string
		siblings []hfSibling
		gap      string
	}{
		{"missing", []hfSibling{}, "selected_file_missing"},
		{"size", []hfSibling{{Filename: "model.gguf"}}, "selected_size_missing"},
		{"hash", []hfSibling{{Filename: "model.gguf", Size: sourceSibling("x").Size, BlobID: sourceCommit}}, "content_sha256_unavailable"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			body := sourceBody(t, test.siblings...)
			resolver, _ := sourceResolver(t, body, body)
			result, err := resolver.ResolveHF(t.Context(), sourceRequest())
			if err != nil || result.State != "incomplete" || !slices.Contains(result.Gaps, test.gap) {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}

func TestResolveHFRejectsMalformedMetadata(t *testing.T) {
	body := sourceBody(t, sourceSibling("model.gguf"))
	cases := map[string]string{
		"duplicate": strings.Replace(body, `"sha":`, `"id":"owner/model","sha":`, 1),
		"trailing":  body + ` {}`,
		"case":      strings.Replace(body, `"sha":`, `"SHA":`, 1),
		"null":      "null", "missing_inventory": `{"id":"owner/model","sha":"` + sourceCommit + `"}`,
		"null_inventory":    `{"id":"owner/model","sha":"` + sourceCommit + `","siblings":null}`,
		"duplicate_file":    sourceBody(t, sourceSibling("model.gguf"), sourceSibling("model.gguf")),
		"traversal":         strings.Replace(body, "model.gguf", "../model.gguf", 1),
		"negative":          strings.Replace(body, "1234", "-1", 1),
		"fraction":          strings.Replace(body, "1234", "1.25", 1),
		"conflicting_sizes": strings.Replace(body, "1234", "9999", 1),
		"bad_hash":          strings.Replace(body, strings.Repeat("c", 64), "etag", 1),
		"bad_git":           strings.Replace(body, strings.Repeat("b", 40), strings.Repeat("b", 64), 1),
		"huge":              strings.ReplaceAll(body, "1234", "9223372036854775808"),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			resolver, calls := sourceResolver(t, body)
			result, err := resolver.ResolveHF(t.Context(), sourceRequest())
			if err != nil || result.State != "unavailable" || result.Gaps[0] != "metadata_invalid" || *calls != 1 {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}

func TestResolveHFBoundsAndRemoteFailures(t *testing.T) {
	body := sourceBody(t, sourceSibling("model.gguf"))
	cases := []struct {
		name      string
		status    int
		headers   http.Header
		body, gap string
	}{
		{"gated", 403, nil, "private diagnostic", "access_denied"},
		{"private", 404, nil, "private diagnostic", "not_found_or_private"},
		{"throttled", 429, nil, "", "rate_limited"},
		{"server", 500, nil, "", "http_error"},
		{"partial", 206, nil, body, "http_error"},
		{"redirect", 302, http.Header{"Location": []string{"http://127.0.0.1/secret"}}, "", "redirect_refused"},
		{"encoding", 200, http.Header{"Content-Encoding": []string{"gzip"}}, body, "encoding_refused"},
		{"headers", 200, http.Header{"X-Huge": []string{strings.Repeat("x", maxHeaderBytes)}}, body, "header_limit"},
		{"body", 200, nil, strings.Repeat("x", MaxMetadataBytes+1), "metadata_limit"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			resolver := NewResolver(sourceTransport(func(*http.Request) (*http.Response, error) {
				calls++
				response := sourceResponse(test.status, test.body)
				response.Header = test.headers
				return response, nil
			}))
			result, err := resolver.ResolveHF(t.Context(), sourceRequest())
			if err != nil || result.State != "unavailable" || result.Gaps[0] != test.gap || calls != 1 {
				t.Fatalf("result=%+v err=%v calls=%d", result, err, calls)
			}
			if result.Queries[0].ResponseSHA256 != "" {
				t.Fatal("failure body was retained")
			}
		})
	}
}

func TestResolveHFTransportFailureAndCancellation(t *testing.T) {
	resolver := NewResolver(sourceTransport(func(request *http.Request) (*http.Response, error) {
		return nil, errors.New("secret transport diagnostic")
	}))
	result, err := resolver.ResolveHF(t.Context(), sourceRequest())
	if err != nil || result.Gaps[0] != "transport_error" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := ResolveHF(ctx, sourceRequest()); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel err=%v", err)
	}
	resolver = NewResolver(sourceTransport(func(request *http.Request) (*http.Response, error) { return nil, context.DeadlineExceeded }))
	result, err = resolver.ResolveHF(t.Context(), sourceRequest())
	if err != nil || result.Gaps[0] != "timeout" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestProductionTransportPolicy(t *testing.T) {
	resolver := NewResolver(nil)
	client, closeIdle := resolver.client()
	defer closeIdle()
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatal("expected isolated native transport")
	}
	if transport.Proxy != nil || !transport.DisableKeepAlives || !transport.DisableCompression ||
		transport.ForceAttemptHTTP2 || transport.TLSNextProto == nil || client.Jar != nil || client.Timeout != 10*time.Second ||
		transport.MaxResponseHeaderBytes != maxHeaderBytes {
		t.Fatalf("unsafe transport=%+v", transport)
	}
	if !errors.Is(client.CheckRedirect(nil, nil), http.ErrUseLastResponse) {
		t.Fatal("redirects enabled")
	}
	var zero Resolver
	if zero.clock().IsZero() {
		t.Fatal("zero resolver clock invalid")
	}
	if _, err := ResolveHF(t.Context(), HFRequest{}); err == nil {
		t.Fatal("invalid request accepted")
	}
}

func TestResolveHFAcceptsAdditiveMetadata(t *testing.T) {
	body := sourceBody(t, sourceSibling("model.gguf"))
	other := strings.Replace(body, `"siblings":`, `"downloads":99,"cardData":{"arbitrary":"inert"},"siblings":`, 1)
	resolver, _ := sourceResolver(t, body, other)
	result, err := resolver.ResolveHF(t.Context(), sourceRequest())
	if err != nil || result.State != "resolved" || result.Queries[0].ResponseSHA256 == result.Queries[1].ResponseSHA256 {
		t.Fatalf("dynamic metadata invalidated pin: %+v %v", result, err)
	}
	if reflect.DeepEqual(result.Queries[0], result.Queries[1]) {
		t.Fatal("observations were flattened")
	}
}

func TestResolveHFExplicitCommitCase(t *testing.T) {
	body := sourceBody(t, sourceSibling("model.gguf"))
	for _, revision := range []string{sourceCommit, strings.ToUpper(sourceCommit), strings.Repeat("e", 40), strings.Repeat("E", 40)} {
		t.Run(revision, func(t *testing.T) {
			resolver, calls := sourceResolver(t, body, body)
			request := sourceRequest()
			request.Revision = revision
			result, err := resolver.ResolveHF(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			if strings.EqualFold(revision, sourceCommit) {
				if result.State != "resolved" || *calls != 2 {
					t.Fatalf("equivalent commit rejected: %+v", result)
				}
			} else if result.State != "unavailable" || result.Gaps[0] != "commit_mismatch" || *calls != 1 {
				t.Fatalf("explicit commit was reinterpreted as a branch: %+v", result)
			}
		})
	}
}
