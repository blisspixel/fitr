package source

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestPrefixRetainsCompleteRepresentationSize(t *testing.T) {
	for _, test := range []struct {
		status       int
		contentRange string
		total        int64
	}{
		{http.StatusPartialContent, "bytes 0-2/123", 123},
		{http.StatusOK, "", 3},
	} {
		response := sourceResponse(test.status, "abc")
		response.ContentLength = 3
		if test.contentRange != "" {
			response.Header.Set("Content-Range", test.contentRange)
		}
		_, observation, err := readPrefix(context.Background(), response, PrefixObservation{RequestedBytes: MaxArtifactPrefixBytes})
		if err != nil || observation.TotalBytes == nil || *observation.TotalBytes != test.total {
			t.Fatalf("complete representation size lost: %+v %v", observation, err)
		}
	}
}

func TestPrefixRecordsCompletionOnEveryOutcome(t *testing.T) {
	for _, repo := range []string{"owner/model", ""} {
		t.Run(repo, func(t *testing.T) {
			resolver := NewResolver(sourceTransport(func(request *http.Request) (*http.Response, error) {
				response := sourceResponse(http.StatusOK, "GGUF")
				response.Request = request
				return response, nil
			}))
			start := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
			clock := start
			resolver.now = func() time.Time {
				instant := clock
				clock = clock.Add(time.Millisecond)
				return instant
			}
			_, observation := resolver.FetchArtifactPrefix(context.Background(), repo, sourceCommit, "model.gguf")
			if observation.StartedAt != start.Format(time.RFC3339Nano) ||
				observation.CompletedAt != start.Add(time.Millisecond).Format(time.RFC3339Nano) {
				t.Fatalf("prefix observation lost its acquisition interval: %+v", observation)
			}
		})
	}
}

func TestPrefixRejectsUnpinnedAndInvalidIdentifiersBeforeNetwork(t *testing.T) {
	for _, test := range []struct{ repo, commit, file string }{
		{"owner/model", "main", "model.gguf"},
		{"owner/model", "abcd", "model.gguf"},
		{"owner/../other", sourceCommit, "model.gguf"},
		{"owner/model?token=value", sourceCommit, "model.gguf"},
		{"owner/model", sourceCommit, "../model.gguf"},
		{"owner/model", sourceCommit, "/model.gguf"},
		{"owner/model", sourceCommit, "model.gguf?download=true"},
	} {
		t.Run(test.repo+"/"+test.commit+"/"+test.file, func(t *testing.T) {
			calls := 0
			resolver := NewResolver(sourceTransport(func(request *http.Request) (*http.Response, error) {
				calls++
				response := sourceResponse(http.StatusOK, "GGUF")
				response.Request = request
				return response, nil
			}))
			body, observation := resolver.FetchArtifactPrefix(context.Background(), test.repo, test.commit, test.file)
			if body != nil || observation.Outcome != "request_invalid" || calls != 0 {
				t.Fatalf("invalid pin reached transport %d times: %+v", calls, observation)
			}
		})
	}
}

func TestPrefixRejectsInvalidRangeEvidence(t *testing.T) {
	for _, test := range []struct {
		name, contentRange, body string
		status                   int
	}{
		{"missing", "", "GGUF", http.StatusPartialContent},
		{"wrong_start", "bytes 4-7/100", "GGUF", http.StatusPartialContent},
		{"wrong_unit", "items 0-3/100", "GGUF", http.StatusPartialContent},
		{"outside_total", "bytes 0-3/3", "GGUF", http.StatusPartialContent},
		{"beyond_request", "bytes 0-32768/40000", "GGUF", http.StatusPartialContent},
		{"truncated", "bytes 0-7/100", "GGUF", http.StatusPartialContent},
		{"extra", "bytes 0-2/100", "GGUF", http.StatusPartialContent},
		{"malformed", "bytes 0-3/100 junk", "GGUF", http.StatusPartialContent},
		{"overflow", "bytes 0-3/99999999999999999999999", "GGUF", http.StatusPartialContent},
		{"full_with_range", "bytes 4-7/100", "GGUF", http.StatusOK},
	} {
		t.Run(test.name, func(t *testing.T) {
			resolver := NewResolver(sourceTransport(func(request *http.Request) (*http.Response, error) {
				response := sourceResponse(test.status, test.body)
				response.Request = request
				if test.contentRange != "" {
					response.Header.Set("Content-Range", test.contentRange)
				}
				return response, nil
			}))
			body, observation := resolver.FetchArtifactPrefix(context.Background(), "owner/model", sourceCommit, "model.gguf")
			if body != nil || observation.Outcome != "range_invalid" {
				t.Fatalf("non-prefix range became evidence: %+v, %q", observation, body)
			}
		})
	}
}

func TestPrefixRefusesRedirectCredentialsAndMalformedTargets(t *testing.T) {
	for _, target := range []string{
		"https://username:password@cdn.example/model", "https://username@cdn.example/model",
		"https://cdn.example/model#fragment", "https://:443/model", "https:opaque",
		"//cdn.example/model", "/relative", "http://cdn.example/model", "https://cdn.example:bad/model",
	} {
		t.Run(target, func(t *testing.T) {
			calls := 0
			resolver := NewResolver(sourceTransport(func(request *http.Request) (*http.Response, error) {
				calls++
				response := sourceResponse(http.StatusOK, "GGUF")
				response.Request = request
				if calls == 1 {
					response.StatusCode = http.StatusFound
					response.Header.Set("Location", target)
				}
				return response, nil
			}))
			body, observation := resolver.FetchArtifactPrefix(context.Background(), "owner/model", sourceCommit, "model.gguf")
			want := "redirect_rejected"
			if target == "https://cdn.example:bad/model" {
				// net/http rejects an unparsable Location before CheckRedirect.
				want = "transport_error"
			}
			if body != nil || observation.Outcome != want || calls != 1 {
				t.Fatalf("unsafe redirect reached transport %d times: %+v", calls, observation)
			}
		})
	}
}

func TestPrefixRefusesEncodedBytes(t *testing.T) {
	resolver := NewResolver(sourceTransport(func(request *http.Request) (*http.Response, error) {
		response := sourceResponse(http.StatusOK, "GGUF")
		response.Request = request
		response.Header.Set("Content-Encoding", "gzip")
		return response, nil
	}))
	body, observation := resolver.FetchArtifactPrefix(context.Background(), "owner/model", sourceCommit, "model.gguf")
	if body != nil || observation.Outcome != "encoding_refused" {
		t.Fatalf("encoded representation became artifact bytes: %+v", observation)
	}
}

type prefixReadFailure struct{ err error }

func (reader prefixReadFailure) Read([]byte) (int, error) { return 0, reader.err }
func (prefixReadFailure) Close() error                    { return nil }

func TestPrefixReadPreservesCancellationAndTimeout(t *testing.T) {
	for _, test := range []struct {
		outcome string
		err     error
	}{
		{"cancelled", context.Canceled}, {"timeout", context.DeadlineExceeded}, {"read_failed", errors.New("broken reader")},
	} {
		t.Run(test.outcome, func(t *testing.T) {
			resolver := NewResolver(sourceTransport(func(request *http.Request) (*http.Response, error) {
				response := sourceResponse(http.StatusOK, "")
				response.Request, response.Body = request, prefixReadFailure{err: test.err}
				return response, nil
			}))
			body, observation := resolver.FetchArtifactPrefix(context.Background(), "owner/model", sourceCommit, "model.gguf")
			if body != nil || observation.Outcome != test.outcome {
				t.Fatalf("read disposition collapsed: %+v", observation)
			}
		})
	}
}

func TestPrefixCancelledBeforeNetwork(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	resolver := NewResolver(sourceTransport(func(request *http.Request) (*http.Response, error) {
		calls++
		return nil, request.Context().Err()
	}))
	body, observation := resolver.FetchArtifactPrefix(ctx, "owner/model", sourceCommit, "model.gguf")
	if body != nil || observation.Outcome != "cancelled" || calls != 0 {
		t.Fatalf("cancelled input reached transport %d times: %+v", calls, observation)
	}
}

func TestPrefixAcceptsOnlyBoundedAnonymousOpeningBytes(t *testing.T) {
	for _, test := range []struct {
		status             int
		contentRange, body string
		want               int
	}{
		{http.StatusPartialContent, "bytes 0-3/100", "GGUF", 4},
		{http.StatusPartialContent, "bytes 0-3/*", "GGUF", 4},
		{http.StatusOK, "", strings.Repeat("x", MaxArtifactPrefixBytes+1024), MaxArtifactPrefixBytes},
	} {
		t.Run(test.contentRange, func(t *testing.T) {
			reader := strings.NewReader(test.body)
			resolver := NewResolver(sourceTransport(func(request *http.Request) (*http.Response, error) {
				if request.URL.String() != "https://huggingface.co/owner/model/resolve/"+sourceCommit+"/nested/model.gguf" ||
					request.Header.Get("Range") != "bytes=0-32767" || request.Header.Get("Accept-Encoding") != "identity" {
					t.Fatalf("incorrect prefix request: %+v", request)
				}
				for _, name := range []string{"Authorization", "Proxy-Authorization", "Cookie"} {
					if request.Header.Get(name) != "" {
						t.Fatalf("credential header %s sent", name)
					}
				}
				response := sourceResponse(test.status, "")
				response.Request, response.Body = request, io.NopCloser(reader)
				if test.contentRange != "" {
					response.Header.Set("Content-Range", test.contentRange)
				}
				return response, nil
			}))
			body, observation := resolver.FetchArtifactPrefix(context.Background(), "owner/model", sourceCommit, "nested/model.gguf")
			if observation.Outcome != "resolved" || len(body) != test.want || observation.Bytes != test.want ||
				reader.Len() != len(test.body)-test.want {
				t.Fatalf("prefix transfer exceeded its evidence or read bound: %+v", observation)
			}
		})
	}
}

func TestPrefixRefusesContradictoryResponseHeaders(t *testing.T) {
	for _, test := range []struct {
		name, outcome string
		configure     func(*http.Response)
	}{
		{"duplicate_range", "range_invalid", func(response *http.Response) {
			response.Header.Add("Content-Range", "bytes 0-3/100")
		}},
		{"mismatched_length", "range_invalid", func(response *http.Response) {
			response.ContentLength = 5
		}},
		{"duplicate_encoding", "encoding_refused", func(response *http.Response) {
			response.Header.Add("Content-Encoding", "identity")
			response.Header.Add("Content-Encoding", "gzip")
		}},
		{"implicitly_decoded", "encoding_refused", func(response *http.Response) {
			response.Uncompressed = true
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			resolver := NewResolver(sourceTransport(func(request *http.Request) (*http.Response, error) {
				response := sourceResponse(http.StatusPartialContent, "GGUF")
				response.Request = request
				response.Header.Set("Content-Range", "bytes 0-3/100")
				test.configure(response)
				return response, nil
			}))
			body, observation := resolver.FetchArtifactPrefix(context.Background(), "owner/model", sourceCommit, "model.gguf")
			if body != nil || observation.Outcome != test.outcome {
				t.Fatalf("contradictory headers became prefix evidence: %+v", observation)
			}
		})
	}
}

func TestPrefixRedirectKeepsAnonymousRequestAndBound(t *testing.T) {
	for _, status := range []int{http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			calls := 0
			resolver := NewResolver(sourceTransport(func(request *http.Request) (*http.Response, error) {
				calls++
				if request.Header.Get("Range") != "bytes=0-32767" || request.Header.Get("Accept-Encoding") != "identity" {
					t.Fatalf("redirect lost its bounded representation request: %+v", request)
				}
				for _, name := range []string{"Authorization", "Proxy-Authorization", "Cookie", "Referer"} {
					if request.Header.Get(name) != "" {
						t.Fatalf("redirect sent %s", name)
					}
				}
				response := sourceResponse(http.StatusOK, "GGUF")
				response.Request = request
				if calls == 1 {
					response.StatusCode = status
					response.Header.Set("Set-Cookie", "session=private")
					response.Header.Set("Location", "https://cdn.example/bytes?signature=ephemeral")
				} else if request.URL.String() != "https://cdn.example/bytes?signature=ephemeral" {
					t.Fatalf("redirect changed the signed location: %s", request.URL)
				}
				return response, nil
			}))
			_, observation := resolver.FetchArtifactPrefix(context.Background(), "owner/model", sourceCommit, "model.gguf")
			if calls != 2 || observation.Outcome != "resolved" || observation.Host != "cdn.example" || observation.Redirects != 1 {
				t.Fatalf("redirect provenance lost: %+v (%d requests)", observation, calls)
			}
		})
	}
}

func FuzzPrefixRange(f *testing.F) {
	for _, value := range []string{"bytes 0-32767/20000000000", "bytes 0-3/*", "bytes 4-7/100", "bytes 0-3/3", "bytes 0-3/+10", "", "bytes 0-0/1"} {
		f.Add(value, int64(-1))
	}
	f.Fuzz(func(t *testing.T, value string, contentLength int64) {
		response := &http.Response{StatusCode: http.StatusPartialContent, Header: make(http.Header), ContentLength: contentLength}
		response.Header.Set("Content-Range", value)
		for _, limit := range []int{1, MaxArtifactPrefixBytes, MaxScreeningPrefixBytes} {
			length, _, valid := prefixRangeDetails(response, limit)
			if valid && (length < 1 || length > int64(limit) || (contentLength >= 0 && length != contentLength)) {
				t.Fatalf("accepted prefix escapes its byte or framing bound: %q length %d Content-Length %d bound %d", value, length, contentLength, limit)
			}
		}
	})
}

func TestPrefixRejectsRangeBeyondSelectedBoundBeforeReading(t *testing.T) {
	for _, limit := range []int{1, 8192, MaxScreeningPrefixBytes} {
		reader := strings.NewReader("GGUF")
		resolver := NewResolver(sourceTransport(func(request *http.Request) (*http.Response, error) {
			response := sourceResponse(http.StatusPartialContent, "")
			response.Request, response.Body = request, io.NopCloser(reader)
			response.Header.Set("Content-Range", fmt.Sprintf("bytes 0-%d/%d", limit, limit+10))
			return response, nil
		}))
		body, observation := resolver.FetchArtifactPrefixLimit(context.Background(), "owner/model", sourceCommit, "model.gguf", limit)
		if body != nil || observation.Outcome != "range_invalid" || reader.Len() != 4 {
			t.Errorf("out-of-bound range read body: limit %d, remaining %d, %+v", limit, reader.Len(), observation)
		}
	}
}

func TestPrefixExplicitBoundIsValidatedBeforeNetwork(t *testing.T) {
	for _, limit := range []int{-1, 0, MaxScreeningPrefixBytes + 1} {
		calls := 0
		resolver := NewResolver(sourceTransport(func(request *http.Request) (*http.Response, error) {
			calls++
			response := sourceResponse(http.StatusOK, "GGUF")
			response.Request = request
			return response, nil
		}))
		body, observation := resolver.FetchArtifactPrefixLimit(context.Background(), "owner/model", sourceCommit, "model.gguf", limit)
		if body != nil || observation.Outcome != "request_invalid" || calls != 0 {
			t.Errorf("invalid bound %d reached transport %d times: %+v", limit, calls, observation)
		}
	}
}

func TestPrefixExplicitBoundControlsRangeAndRead(t *testing.T) {
	for _, limit := range []int{1, 8192, MaxArtifactPrefixBytes, 2 * MaxArtifactPrefixBytes, MaxScreeningPrefixBytes} {
		for _, partial := range []bool{false, true} {
			reader := strings.NewReader(strings.Repeat("x", limit+10))
			calls := 0
			resolver := NewResolver(sourceTransport(func(request *http.Request) (*http.Response, error) {
				calls++
				if got := request.Header.Get("Range"); got != fmt.Sprintf("bytes=0-%d", limit-1) {
					t.Errorf("bound %d requested %q", limit, got)
				}
				response := sourceResponse(http.StatusOK, "")
				response.Request, response.Body = request, io.NopCloser(reader)
				if partial {
					response.StatusCode = http.StatusPartialContent
					response.Header.Set("Content-Range", fmt.Sprintf("bytes 0-%d/%d", limit-1, limit+10))
					response.ContentLength = int64(limit)
				}
				return response, nil
			}))
			body, observation := resolver.FetchArtifactPrefixLimit(context.Background(), "owner/model", sourceCommit, "model.gguf", limit)
			if calls != 1 || observation.Outcome != "resolved" || observation.RequestedBytes != limit ||
				len(body) != limit || observation.Bytes != limit || reader.Len() != 10 {
				t.Errorf("bound %d (partial %v) read %d bytes with %d left in %d calls: %+v", limit, partial, len(body), reader.Len(), calls, observation)
			}
		}
	}
}
