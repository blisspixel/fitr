package source

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// MaxArtifactPrefixBytes is how much of an artifact a prefix read may take.
//
// The keys the fit arithmetic needs sit ahead of the tokenizer vocabulary, so
// a small prefix is enough, but 4 KiB is not: some current architectures carry
// their keys past 10 KB. This is the bound on bytes transferred, not a promise
// that the header fits inside it, and a truncated decode stays truncated
// rather than being retried larger.
const MaxArtifactPrefixBytes = 32 << 10

// MaxScreeningPrefixBytes is the largest explicitly authorized header read.
// Tokenizer metadata can exceed the default prefix; a larger read requires a
// new operator-selected bound rather than an automatic retry.
const MaxScreeningPrefixBytes = 8 << 20

// PrefixObservation records a bounded read of an artifact's opening bytes.
//
// Host is the host that actually served the bytes, which is not the host the
// request was addressed to. A provider hands out signed, expiring URLs on a
// content network it chooses, so the only honest record is where the response
// came from. A projection built from these bytes has a different provenance
// than one built from a file on disk, and this is what says so.
type PrefixObservation struct {
	RequestedHost  string `json:"requested_host"`
	RequestedBytes int    `json:"requested_bytes,omitempty"`
	Host           string `json:"host,omitempty"`
	Redirects      int    `json:"redirects"`
	HTTPStatus     int    `json:"http_status,omitempty"`
	Bytes          int    `json:"bytes,omitempty"`
	TotalBytes     *int64 `json:"total_bytes,omitempty"`
	Outcome        string `json:"outcome"`
	StartedAt      string `json:"started_at,omitempty"`
	CompletedAt    string `json:"completed_at,omitempty"`
}

// maxPrefixRedirects is one.
//
// A provider that stores large files behind a content network answers the
// canonical URL with a redirect to a signed location, so refusing every
// redirect refuses the bytes. One hop reaches them. More than one is a chain
// fitr has no reason to follow and would make the host that served the response
// progressively less predictable.
const maxPrefixRedirects = 1

// FetchArtifactPrefix reads the opening bytes of one resolved file.
//
// It is separate from metadata resolution and is never performed as part of
// it: resolution stays on its fixed host, and this deliberately does not. The
// caller asks for it explicitly, and the observation returned records where the
// bytes came from so a receipt can carry that rather than imply the canonical
// host served them.
//
// The request is anonymous, carries no credentials, asks for a byte range, and
// stops reading at the bound whether or not the server honored the range.
func (resolver *Resolver) FetchArtifactPrefix(ctx context.Context, repo, commit, file string) ([]byte, PrefixObservation) {
	return resolver.FetchArtifactPrefixLimit(ctx, repo, commit, file, MaxArtifactPrefixBytes)
}

// FetchArtifactPrefixLimit applies one explicit per-file transfer bound. It
// never retries with a larger range, even when metadata is incomplete.
func (resolver *Resolver) FetchArtifactPrefixLimit(ctx context.Context, repo, commit, file string, maxBytes int) (body []byte, observation PrefixObservation) {
	observation = PrefixObservation{
		RequestedHost: "huggingface.co", RequestedBytes: maxBytes, StartedAt: resolver.clock().Format(time.RFC3339Nano),
	}
	defer func() { observation.CompletedAt = resolver.clock().Format(time.RFC3339Nano) }()
	if maxBytes <= 0 || maxBytes > MaxScreeningPrefixBytes ||
		!validRepo(repo) || !commitPattern.MatchString(commit) || !validPath(file, 512) {
		observation.Outcome = "request_invalid"
		return nil, observation
	}
	if err := ctx.Err(); err != nil {
		observation.Outcome = requestFailure(ctx, err)
		return nil, observation
	}
	endpoint := "https://huggingface.co/" + repo + "/resolve/" + url.PathEscape(commit) + "/" + prefixEscapePath(file)
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	client, closeIdle := resolver.client()
	defer closeIdle()

	var err error
	body, observation, err = followPrefix(ctx, client, endpoint, observation)
	if err != nil {
		return nil, observation
	}
	return body, observation
}

func followPrefix(ctx context.Context, client *http.Client, endpoint string,
	observation PrefixObservation,
) ([]byte, PrefixObservation, error) {
	for hop := 0; hop <= maxPrefixRedirects; hop++ {
		if err := ctx.Err(); err != nil {
			observation.Outcome = requestFailure(ctx, err)
			return nil, observation, err
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			observation.Outcome = "request_invalid"
			return nil, observation, err
		}
		request.Header.Set("Range", fmt.Sprintf("bytes=0-%d", observation.RequestedBytes-1))
		request.Header.Set("Accept-Encoding", "identity")
		request.Header.Set("User-Agent", "fitr-source/"+PolicyVersion)
		response, err := client.Do(request)
		if err != nil {
			observation.Outcome = requestFailure(ctx, err)
			return nil, observation, err
		}
		observation.HTTPStatus = response.StatusCode
		observation.Host = response.Request.URL.Host
		if location := redirectTarget(response); location != "" {
			response.Body.Close()
			if hop == maxPrefixRedirects {
				observation.Outcome = "redirect_limit"
				return nil, observation, errors.New("too many redirects")
			}
			next, err := url.Parse(location)
			if err != nil || next.Scheme != "https" || next.Hostname() == "" ||
				next.User != nil || next.Fragment != "" || next.Opaque != "" {
				observation.Outcome = "redirect_rejected"
				return nil, observation, errors.New("redirect is not an absolute https location")
			}
			endpoint = next.String()
			observation.Redirects++
			continue
		}
		if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusPartialContent {
			response.Body.Close()
			observation.Outcome = "http_status"
			return nil, observation, fmt.Errorf("prefix read returned %d", response.StatusCode)
		}
		return readPrefix(ctx, response, observation)
	}
	observation.Outcome = "redirect_limit"
	return nil, observation, errors.New("too many redirects")
}

func readPrefix(ctx context.Context, response *http.Response, observation PrefixObservation) ([]byte, PrefixObservation, error) {
	defer response.Body.Close()
	if values := response.Header.Values("Content-Encoding"); len(values) > 1 ||
		(len(values) == 1 && values[0] != "" && values[0] != "identity") || response.Uncompressed {
		observation.Outcome = "encoding_refused"
		return nil, observation, errors.New("artifact prefix representation is encoded")
	}
	expected, totalBytes, valid := prefixRangeDetails(response, observation.RequestedBytes)
	if !valid {
		observation.Outcome = "range_invalid"
		return nil, observation, errors.New("response does not identify an opening byte range")
	}
	observation.TotalBytes = totalBytes
	body, err := io.ReadAll(io.LimitReader(response.Body, int64(observation.RequestedBytes)))
	if err != nil {
		observation.Outcome = requestFailure(ctx, err)
		if observation.Outcome == "transport_error" {
			observation.Outcome = "read_failed"
		}
		return nil, observation, err
	}
	if expected >= 0 && int64(len(body)) != expected {
		observation.Outcome = "range_invalid"
		return nil, observation, errors.New("prefix byte count disagrees with its range")
	}
	observation.Bytes = len(body)
	observation.Outcome = "resolved"
	return body, observation, nil
}

// RFC 9110 (June 2022), sections 14.4 and 15.3.7, defines Content-Range and
// requires it on a single-part 206 response. Bytes from any other offset are
// not an artifact header, even if a decoder happens to recognize them.
func prefixRangeDetails(response *http.Response, maxBytes int) (int64, *int64, bool) {
	values := response.Header.Values("Content-Range")
	if response.StatusCode == http.StatusOK {
		var total *int64
		if response.ContentLength >= 0 {
			total = &response.ContentLength
		}
		return -1, total, len(values) == 0
	}
	if len(values) != 1 {
		return 0, nil, false
	}
	value, found := strings.CutPrefix(values[0], "bytes ")
	if !found {
		return 0, nil, false
	}
	span, total, found := strings.Cut(value, "/")
	if !found {
		return 0, nil, false
	}
	first, last, found := strings.Cut(span, "-")
	if !found || first != "0" {
		return 0, nil, false
	}
	end, valid := prefixRangeNumber(last)
	if !valid || end >= int64(maxBytes) {
		return 0, nil, false
	}
	var totalBytes *int64
	if total != "*" {
		length, valid := prefixRangeNumber(total)
		if !valid || length <= end {
			return 0, nil, false
		}
		totalBytes = &length
	}
	length := end + 1
	return length, totalBytes, response.ContentLength < 0 || response.ContentLength == length
}

func prefixRangeNumber(value string) (int64, bool) {
	for _, digit := range value {
		if digit < '0' || digit > '9' {
			return 0, false
		}
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	return parsed, err == nil
}

func redirectTarget(response *http.Response) string {
	switch response.StatusCode {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return response.Header.Get("Location")
	}
	return ""
}

// prefixEscapePath escapes each path segment while keeping the separators, so
// a nested filename stays a path rather than becoming one escaped segment.
func prefixEscapePath(file string) string {
	parts := strings.Split(file, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}
