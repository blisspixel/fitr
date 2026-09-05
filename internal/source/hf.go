package source

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"time"

	"github.com/blisspixel/fitr/internal/buildinfo"
)

const maxHeaderBytes = 32 << 10

// Resolver has no endpoint, credentials or redirect configuration. An injected
// transport is trusted code for offline tests, not a configurable remote URL.
// The zero value uses the same production policy as NewResolver(nil).
type Resolver struct {
	transport http.RoundTripper
	now       func() time.Time
}

// NewResolver accepts an optional trusted transport for tests. All requests
// still target fixed anonymous HTTPS metadata URLs on huggingface.co.
func NewResolver(transport http.RoundTripper) *Resolver {
	return &Resolver{transport: transport, now: time.Now}
}

func ResolveHF(ctx context.Context, request HFRequest) (Resolution, error) {
	return NewResolver(nil).ResolveHF(ctx, request)
}

// ResolveHF returns a sealed unavailable receipt for remote failures. An error
// means invalid local input or an inability to construct a valid receipt.
func (resolver *Resolver) ResolveHF(ctx context.Context, request HFRequest) (Resolution, error) {
	if err := request.Validate(); err != nil {
		return Resolution{}, err
	}
	if err := ctx.Err(); err != nil {
		return Resolution{}, err
	}
	request.Files = slices.Clone(request.Files)
	slices.Sort(request.Files)
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	client, closeIdle := resolver.client()
	defer closeIdle()
	result := Resolution{Schema: ResolutionSchema, PolicyVersion: PolicyVersion,
		ResolverVersion: buildinfo.Version(), Provider: "huggingface", Scope: Scope,
		Request: request, State: "unavailable", Files: []FileMetadata{}, InventoryPaths: []string{},
		Dependencies: []DependencyFinding{}, Gaps: []string{}, Queries: []QueryObservation{}}
	first, observation := resolver.query(ctx, client, request.RepoID, request.Revision)
	result.Queries = append(result.Queries, observation)
	if observation.Outcome != "complete" {
		return resolver.finish(result, observation.Outcome)
	}
	if first.ID != request.RepoID {
		return resolver.finish(result, "repository_mismatch")
	}
	if commit := requestedCommit(request.Revision); commit != "" && first.SHA != commit {
		return resolver.finish(result, "commit_mismatch")
	}
	second, observation := resolver.query(ctx, client, request.RepoID, first.SHA)
	result.Queries = append(result.Queries, observation)
	if observation.Outcome != "complete" {
		return resolver.finish(result, observation.Outcome)
	}
	if second.ID != first.ID {
		return resolver.finish(result, "repository_mismatch")
	}
	if second.SHA != first.SHA {
		return resolver.finish(result, "commit_mismatch")
	}
	if !consistentMetadata(first, second, request.Files) {
		return resolver.finish(result, "metadata_mismatch")
	}
	dependencies, err := findDependencies(request.Files, second.files)
	if err != nil {
		return resolver.finish(result, "dependency_limit")
	}
	result.ResolvedRepo, result.ResolvedCommit = second.ID, second.SHA
	for path := range second.files {
		result.InventoryPaths = append(result.InventoryPaths, path)
	}
	slices.Sort(result.InventoryPaths)
	result.Files, result.Dependencies = selectFiles(request.Files, second.files), dependencies
	result.State, result.Gaps = selectedState(result.Files)
	return resolver.finish(result, "")
}

func (resolver *Resolver) clock() time.Time {
	if resolver != nil && resolver.now != nil {
		return resolver.now().UTC()
	}
	return time.Now().UTC()
}

func (resolver *Resolver) finish(result Resolution, gap string) (Resolution, error) {
	if gap != "" {
		result.Gaps = []string{gap}
	}
	result.ObservedAt = resolver.clock().Format(time.RFC3339Nano)
	digest, err := result.Digest()
	if err != nil {
		return Resolution{}, err
	}
	result.ResolutionSHA256 = digest
	return result, result.Validate()
}

func (resolver *Resolver) client() (*http.Client, func()) {
	var transport http.RoundTripper
	if resolver != nil {
		transport = resolver.transport
	}
	closeIdle := func() {}
	if transport == nil {
		dialer := &net.Dialer{Timeout: 5 * time.Second}
		native := &http.Transport{DialContext: dialer.DialContext, DisableCompression: true,
			DisableKeepAlives: true, TLSNextProto: map[string]func(string, *tls.Conn) http.RoundTripper{}, TLSHandshakeTimeout: 5 * time.Second,
			ResponseHeaderTimeout: 10 * time.Second, MaxResponseHeaderBytes: maxHeaderBytes}
		transport, closeIdle = native, native.CloseIdleConnections
	}
	return &http.Client{Transport: transport, Timeout: 10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, closeIdle
}

func (resolver *Resolver) query(ctx context.Context, client *http.Client, repo, revision string) (metadata hfMetadata, observation QueryObservation) {
	observation = QueryObservation{Revision: revision, StartedAt: resolver.clock().Format(time.RFC3339Nano)}
	defer func() { observation.CompletedAt = resolver.clock().Format(time.RFC3339Nano) }()
	endpoint := "https://huggingface.co/api/models/" + repo + "/revision/" + url.PathEscape(revision) + "?blobs=true"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		observation.Outcome = "request_invalid"
		return metadata, observation
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Accept-Encoding", "identity")
	request.Header.Set("User-Agent", "fitr-source/"+PolicyVersion)
	response, err := client.Do(request)
	if err != nil {
		observation.Outcome = requestFailure(ctx, err)
		return metadata, observation
	}
	defer response.Body.Close()
	observation.HTTPStatus = response.StatusCode
	if code := checkResponse(response); code != "" {
		observation.Outcome = code
		return metadata, observation
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, MaxMetadataBytes+1))
	if err != nil {
		observation.Outcome = requestFailure(ctx, err)
		return metadata, observation
	}
	if len(data) > MaxMetadataBytes {
		observation.Outcome = "metadata_limit"
		return metadata, observation
	}
	observation.ResponseSHA256 = hashBytes(data)
	metadata, err = parseMetadata(data)
	if err != nil {
		observation.Outcome = "metadata_invalid"
		return metadata, observation
	}
	observation.Outcome = "complete"
	return metadata, observation
}

func checkResponse(response *http.Response) string {
	size := len(response.Status)
	for name, values := range response.Header {
		for _, value := range values {
			size += len(name) + len(value) + 4
		}
	}
	if size > maxHeaderBytes {
		return "header_limit"
	}
	if response.StatusCode != http.StatusOK {
		switch response.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			return "access_denied"
		case http.StatusNotFound:
			return "not_found_or_private"
		case http.StatusTooManyRequests:
			return "rate_limited"
		}
		if response.StatusCode >= 300 && response.StatusCode < 400 {
			return "redirect_refused"
		}
		return "http_error"
	}
	if encoding := response.Header.Get("Content-Encoding"); encoding != "" && encoding != "identity" {
		return "encoding_refused"
	}
	if response.ContentLength > MaxMetadataBytes {
		return "metadata_limit"
	}
	return ""
}

func requestFailure(ctx context.Context, err error) string {
	if errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	var timeout net.Error
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || (errors.As(err, &timeout) && timeout.Timeout()) {
		return "timeout"
	}
	return "transport_error"
}

func hashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

type hfMetadata struct {
	ID       string      `json:"id"`
	SHA      string      `json:"sha"`
	Siblings []hfSibling `json:"siblings"`
	files    map[string]FileMetadata
}

type hfSibling struct {
	Filename string `json:"rfilename"`
	Size     *int64 `json:"size"`
	BlobID   string `json:"blobId"`
	LFS      *hfLFS `json:"lfs"`
}

type hfLFS struct {
	Size        *int64 `json:"size"`
	SHA256      string `json:"sha256"`
	PointerSize *int64 `json:"pointerSize"`
}

// The upstream model_info wire fields are blobId and lfs.sha256/size/pointerSize:
// https://github.com/huggingface/huggingface_hub/blob/main/src/huggingface_hub/hf_api.py
func parseMetadata(data []byte) (hfMetadata, error) {
	var metadata hfMetadata
	if err := decodeJSON(data, &metadata, true); err != nil {
		return metadata, err
	}
	if !validRepo(metadata.ID) || !commitPattern.MatchString(metadata.SHA) ||
		metadata.Siblings == nil || len(metadata.Siblings) > MaxInventory {
		return metadata, errors.New("invalid metadata identity or inventory")
	}
	metadata.files = make(map[string]FileMetadata, len(metadata.Siblings))
	for _, sibling := range metadata.Siblings {
		file, err := sibling.file()
		if err != nil {
			return metadata, err
		}
		if _, duplicate := metadata.files[file.Path]; duplicate {
			return metadata, errors.New("duplicate inventory path")
		}
		metadata.files[file.Path] = file
	}
	return metadata, nil
}

func (sibling hfSibling) file() (FileMetadata, error) {
	file := FileMetadata{Path: sibling.Filename, State: "present", SizeBytes: sibling.Size, GitBlobOID: sibling.BlobID}
	if sibling.LFS != nil {
		lfs := sibling.LFS
		if lfs.PointerSize != nil && (*lfs.PointerSize < 0 || *lfs.PointerSize > MaxMetadataBytes) {
			return file, errors.New("invalid LFS pointer size")
		}
		if sibling.Size != nil && lfs.Size != nil && *sibling.Size != *lfs.Size {
			return file, errors.New("conflicting byte sizes")
		}
		if lfs.Size != nil {
			file.SizeBytes = lfs.Size
		}
		if lfs.SHA256 != "" {
			file.DeclaredSHA256 = "sha256:" + lfs.SHA256
		}
	}
	return file, validateFile(file)
}

func selectFiles(paths []string, inventory map[string]FileMetadata) []FileMetadata {
	files := make([]FileMetadata, 0, len(paths))
	for _, path := range paths {
		file, found := inventory[path]
		if !found {
			file = FileMetadata{Path: path, State: "missing"}
		}
		files = append(files, file)
	}
	return files
}

func consistentMetadata(first, second hfMetadata, paths []string) bool {
	if !reflect.DeepEqual(selectFiles(paths, first.files), selectFiles(paths, second.files)) {
		return false
	}
	if len(first.files) != len(second.files) {
		return false
	}
	for path := range first.files {
		if _, found := second.files[path]; !found {
			return false
		}
	}
	return true
}
