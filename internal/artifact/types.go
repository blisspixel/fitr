package artifact

import "github.com/blisspixel/fitr/internal/source"

// Binding is an integrity-sealed historical observation, not provider
// authentication, a filesystem snapshot or evidence of a runtime loading bytes.
// The embedded source and mapping are independent copies of the caller's input.
type Binding struct {
	Schema           string            `json:"schema"`
	PolicyVersion    string            `json:"policy_version"`
	BinderVersion    string            `json:"binder_version"`
	BindingSHA256    string            `json:"binding_sha256"`
	ResolutionSHA256 string            `json:"resolution_sha256"`
	Source           source.Resolution `json:"source"`
	Mapping          Spec              `json:"mapping"`
	Limits           Limits            `json:"limits"`
	StartedAt        string            `json:"started_at"`
	CompletedAt      string            `json:"completed_at"`
	BytesRead        int64             `json:"bytes_read"`
	State            string            `json:"state"`
	Files            []FileObservation `json:"files"`
	UnmappedFiles    []string          `json:"unmapped_files"`
	Gaps             []string          `json:"gaps"`
	DependencyState  string            `json:"dependency_state"`
	RuntimeState     string            `json:"runtime_state"`
	CapacityState    string            `json:"capacity_state"`
	QualityState     string            `json:"quality_state"`
}

type FileObservation struct {
	SourcePath     string     `json:"source_path"`
	LocalPath      string     `json:"local_path"`
	ComponentRole  string     `json:"component_role"`
	State          string     `json:"state"`
	Before         *FileFacts `json:"before"`
	After          *FileFacts `json:"after"`
	BytesRead      int64      `json:"bytes_read"`
	ObservedSHA256 string     `json:"observed_sha256,omitempty"`
	// verified means the retained open handle and checked path agreed at the
	// final whole-set recheck. It is not an assertion about the current path.
	IdentityState string `json:"identity_state"`
}

type FileFacts struct {
	SizeBytes  int64  `json:"size_bytes"`
	ModifiedAt string `json:"modified_at"`
}
