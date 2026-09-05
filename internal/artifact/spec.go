// Package artifact records bounded observations of explicitly mapped local
// bytes. A binding does not establish dependency compatibility or runtime use.
package artifact

import (
	"errors"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/blisspixel/fitr/internal/source"
)

const (
	SpecSchema            = "fitr.artifact.bind.spec.v1"
	BindingSchema         = "fitr.artifact.binding.v1"
	PolicyVersion         = "1"
	MaxFiles              = 32
	MaxSpecBytes          = 256 << 10
	MaxReceiptBytes       = 2 << 20
	DefaultMaxBytes int64 = 64 << 30
	HardMaxBytes    int64 = 1 << 40
	DefaultTimeout        = 10 * time.Minute
	HardTimeout           = time.Hour
)

var shaPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

type Spec struct {
	Schema           string    `json:"schema"`
	ResolutionSHA256 string    `json:"resolution_sha256"`
	Files            []Mapping `json:"files"`
}

// ComponentRole is an operator declaration, not a verified relationship.
// LocalPath must be absolute; bindings never depend on a later working directory.
type Mapping struct {
	SourcePath    string `json:"source_path"`
	LocalPath     string `json:"local_path"`
	ComponentRole string `json:"component_role"`
}

type Options struct {
	MaxBytes int64
	Timeout  time.Duration
}

type Limits struct {
	MaxBytes      int64 `json:"max_bytes"`
	TimeoutMillis int64 `json:"timeout_millis"`
}

func (spec Spec) Validate() error {
	if spec.Schema != SpecSchema || !shaPattern.MatchString(spec.ResolutionSHA256) || len(spec.Files) < 1 || len(spec.Files) > MaxFiles {
		return errors.New("artifact mapping requires its schema, full source digest and 1 to 32 files")
	}
	paths, locals := map[string]bool{}, map[string]bool{}
	for _, file := range spec.Files {
		if !validSourcePath(file.SourcePath) || paths[file.SourcePath] || !validAbsolutePath(file.LocalPath) {
			return errors.New("artifact mapping requires unique source paths and safe absolute local paths")
		}
		key := localKey(file.LocalPath)
		if locals[key] || !slices.Contains([]string{"weights", "shard", "projector", "encoder", "tokenizer", "template", "other"}, file.ComponentRole) {
			return errors.New("artifact mapping has a duplicate local path or unsupported component role")
		}
		paths[file.SourcePath], locals[key] = true, true
	}
	return nil
}

func validSourcePath(path string) bool {
	request := source.HFRequest{RepoID: "owner/model", Revision: "main", Files: []string{path}}
	return request.Validate() == nil
}

func validAbsolutePath(path string) bool {
	if path == "" || len(path) > 4096 || strings.ContainsAny(path, "\x00\r\n\t") {
		return false
	}
	normal := strings.ReplaceAll(path, "\\", "/")
	if strings.HasPrefix(normal, "//") || (!strings.HasPrefix(normal, "/") && !windowsAbsolute(normal)) {
		return false
	}
	if windowsAbsolute(normal) {
		for _, part := range strings.Split(normal[3:], "/") {
			if part != "." && (strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ")) {
				return false
			}
		}
		normal = normal[2:]
	}
	if strings.Contains(normal, ":") {
		return false
	}
	for _, part := range strings.Split(normal, "/") {
		if part == ".." || strings.ContainsAny(part, "*?\"") {
			return false
		}
	}
	return true
}

func windowsAbsolute(path string) bool {
	return len(path) >= 3 && ((path[0] >= 'A' && path[0] <= 'Z') || (path[0] >= 'a' && path[0] <= 'z')) && path[1:3] == ":/"
}

func localKey(path string) string {
	normal := cleanPortablePath(path)
	if windowsAbsolute(normal) {
		return strings.ToLower(normal)
	}
	return normal
}

func cleanPortablePath(value string) string { return path.Clean(strings.ReplaceAll(value, "\\", "/")) }

func (options Options) Validate() error { _, err := options.limits(); return err }

func (options Options) limits() (Limits, error) {
	if options.MaxBytes == 0 {
		options.MaxBytes = DefaultMaxBytes
	}
	if options.Timeout == 0 {
		options.Timeout = DefaultTimeout
	}
	if options.MaxBytes < 1 || options.MaxBytes > HardMaxBytes || options.Timeout < time.Millisecond || options.Timeout > HardTimeout || options.Timeout%time.Millisecond != 0 {
		return Limits{}, errors.New("artifact limits require 1 byte to 1 TiB and whole milliseconds from 1 ms to 1 hour")
	}
	return Limits{MaxBytes: options.MaxBytes, TimeoutMillis: options.Timeout.Milliseconds()}, nil
}
