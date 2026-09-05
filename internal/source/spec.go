// Package source resolves explicit remote file metadata. Its receipts are
// observations, not verified local artifacts, dependency closures or fit claims.
package source

import (
	"encoding/hex"
	"errors"
	"regexp"
	"slices"
	"strings"
)

const (
	ResolutionSchema       = "fitr.source.resolution.v1"
	PolicyVersion          = "fitr.source.hf.metadata.v1"
	Scope                  = "explicit_file_metadata"
	MaxFiles               = 32
	MaxMetadataBytes       = 4 << 20
	MaxReceiptBytes        = 1 << 20
	MaxInventory           = 4096
	MaxDependencies        = 256
	maxFileBytes     int64 = 1 << 60
)

var (
	repoPartPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,95}$`)
	pathPattern     = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)
	commitPattern   = regexp.MustCompile(`^[0-9a-f]{40}$`)
	shaPattern      = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	versionPattern  = regexp.MustCompile(`^[A-Za-z0-9._+-]{1,128}$`)
)

// HFRequest deliberately accepts identifiers, not URLs, implicit revisions or
// quantization aliases. Files are copied and sorted before resolution.
type HFRequest struct {
	RepoID   string   `json:"repo_id"`
	Revision string   `json:"revision"`
	Files    []string `json:"files"`
}

func (request HFRequest) Validate() error {
	if !validRepo(request.RepoID) || !validPath(request.Revision, 256) {
		return errors.New("source requires an explicit owner/repository and revision")
	}
	if len(request.Files) < 1 || len(request.Files) > MaxFiles {
		return errors.New("source requires 1 to 32 explicit files")
	}
	seen := make(map[string]bool, len(request.Files))
	for _, file := range request.Files {
		if !validPath(file, 512) || seen[file] {
			return errors.New("source filenames must be unique bounded relative paths")
		}
		seen[file] = true
	}
	return nil
}

func validRepo(value string) bool {
	parts := strings.Split(value, "/")
	if len(parts) != 2 {
		return false
	}
	for _, part := range parts {
		if !repoPartPattern.MatchString(part) || strings.Contains(part, "..") ||
			strings.Contains(part, "--") || strings.HasSuffix(part, ".git") ||
			strings.HasSuffix(part, ".") || strings.HasSuffix(part, "-") {
			return false
		}
	}
	return true
}

func validPath(value string, maximum int) bool {
	if len(value) < 1 || len(value) > maximum || !pathPattern.MatchString(value) {
		return false
	}
	for _, part := range strings.Split(value, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

func requestedCommit(revision string) string {
	if len(revision) != 40 {
		return ""
	}
	if _, err := hex.DecodeString(revision); err != nil {
		return ""
	}
	return strings.ToLower(revision)
}

// FileMetadata keeps provider-declared LFS SHA-256 separate from a Git object
// ID. SizeBytes is nil when no byte count was supplied. State is present/missing.
type FileMetadata struct {
	Path           string `json:"path"`
	State          string `json:"state"`
	SizeBytes      *int64 `json:"size_bytes"`
	GitBlobOID     string `json:"git_blob_oid,omitempty"`
	DeclaredSHA256 string `json:"declared_sha256,omitempty"`
}

// DependencyFinding describes filename evidence only. Candidate files are not
// automatically selected or asserted to be compatible dependencies.
type DependencyFinding struct {
	Kind       string `json:"kind"`
	SourceFile string `json:"source_file,omitempty"`
	TargetFile string `json:"target_file,omitempty"`
	Status     string `json:"status"`
	Basis      string `json:"basis"`
}

// QueryObservation records each attempted metadata query. CompletedAt is when
// that query finished, including failures, rather than the overall start time.
// ResponseSHA256 is present only for a complete, bounded HTTP 200 body.
type QueryObservation struct {
	Revision       string `json:"revision"`
	StartedAt      string `json:"started_at"`
	CompletedAt    string `json:"completed_at"`
	HTTPStatus     int    `json:"http_status"`
	Outcome        string `json:"outcome"`
	ResponseSHA256 string `json:"response_sha256,omitempty"`
}

// Resolution is a sealed metadata observation. Its digest detects accidental
// changes, not forgery by someone able to construct a new receipt. No signature
// or locally verified artifact identity is implied.
type Resolution struct {
	Schema           string              `json:"schema"`
	ResolutionSHA256 string              `json:"resolution_sha256"`
	ObservedAt       string              `json:"observed_at"`
	ResolverVersion  string              `json:"resolver_version"`
	PolicyVersion    string              `json:"policy_version"`
	Provider         string              `json:"provider"`
	Scope            string              `json:"scope"`
	Request          HFRequest           `json:"request"`
	ResolvedRepo     string              `json:"resolved_repo,omitempty"`
	ResolvedCommit   string              `json:"resolved_commit,omitempty"`
	State            string              `json:"state"`
	Files            []FileMetadata      `json:"files"`
	InventoryPaths   []string            `json:"inventory_paths"`
	Dependencies     []DependencyFinding `json:"dependencies"`
	Gaps             []string            `json:"gaps"`
	Queries          []QueryObservation  `json:"queries"`
}

func selectedState(files []FileMetadata) (string, []string) {
	state := "resolved"
	gaps := []string{"dependency_closure_unverified", "local_artifact_unverified"}
	for _, file := range files {
		switch {
		case file.State == "missing":
			gaps = append(gaps, "selected_file_missing")
		case file.SizeBytes == nil:
			gaps = append(gaps, "selected_size_missing")
		}
		if file.State == "present" && file.DeclaredSHA256 == "" {
			gaps = append(gaps, "content_sha256_unavailable")
		}
		if file.State != "present" || file.SizeBytes == nil || file.DeclaredSHA256 == "" {
			state = "incomplete"
		}
	}
	slices.Sort(gaps)
	return state, slices.Compact(gaps)
}
