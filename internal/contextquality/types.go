package contextquality

type Family string
type Position string

const (
	IndirectRetrieval    Family   = "indirect_retrieval"
	DistantDependency    Family   = "distant_dependency"
	InstructionRetention Family   = "instruction_retention"
	Beginning            Position = "beginning"
	Middle               Position = "middle"
	End                  Position = "end"
)

type Span struct {
	Name   string `json:"name"`
	Offset int    `json:"offset"`
	Length int    `json:"length"`
}

// Cell offsets are bytes within Payload, not tokens or positions in a model's
// rendered request. ID binds every descriptor field and both text digests.
type Cell struct {
	ID               string   `json:"id"`
	Sequence         int      `json:"sequence"`
	PolicySHA256     string   `json:"policy_sha256"`
	Family           Family   `json:"family"`
	Position         Position `json:"position"`
	Seed             uint64   `json:"seed"`
	InstanceSHA256   string   `json:"instance_sha256"`
	PayloadUTF8Bytes int      `json:"payload_utf8_bytes"`
	PromptUTF8Bytes  int      `json:"prompt_utf8_bytes"`
	PayloadSHA256    string   `json:"payload_sha256"`
	PromptSHA256     string   `json:"prompt_sha256"`
	Spans            []Span   `json:"spans"`
}

type Plan struct {
	Schema       string `json:"schema"`
	Policy       Policy `json:"policy"`
	PolicySHA256 string `json:"policy_sha256"`
	SeedSet      string `json:"seed_set"`
	Cells        []Cell `json:"cells"`
	PlanSHA256   string `json:"plan_sha256"`
}

// Task contains no expected answer. Only Prompt is intended for submission.
// JSON transport bytes, template tokens and native payload tokens are unknown.
type Task struct {
	Cell    Cell
	Payload string
	Prompt  string
}

type Outcome string

const (
	Pass        Outcome = "pass"
	Fail        Outcome = "fail"
	Unavailable Outcome = "unavailable"
)

type Verification struct {
	Schema  string  `json:"schema"`
	CellID  string  `json:"cell_id"`
	Outcome Outcome `json:"outcome"`
	Reason  string  `json:"reason"`
}

type Disposition string

const (
	Answered     Disposition = "answered"
	OutputLimit  Disposition = "output_limit"
	ContextLimit Disposition = "context_limit"
	NotAvailable Disposition = "unavailable"
)

// Observation is an adapter input, not runtime proof. Analyze never trusts a
// claimed pass. The later signed execution adapter must independently validate
// disposition, accounting and runtime facts before consuming this task report.
type Observation struct {
	CellID            string      `json:"cell_id"`
	PayloadSHA256     string      `json:"payload_sha256"`
	PromptSHA256      string      `json:"prompt_sha256"`
	Disposition       Disposition `json:"disposition"`
	Answer            string      `json:"answer,omitempty"`
	UnavailableReason string      `json:"unavailable_reason,omitempty"`
}

type Counts struct {
	Planned     int `json:"planned"`
	Pass        int `json:"pass"`
	Fail        int `json:"fail"`
	Unavailable int `json:"unavailable"`
}

type Tier struct {
	PayloadUTF8Bytes int     `json:"payload_utf8_bytes"`
	Outcome          Outcome `json:"outcome"`
	Counts           Counts  `json:"counts"`
}

type Report struct {
	Schema                  string         `json:"schema"`
	PlanSHA256              string         `json:"plan_sha256"`
	Scope                   string         `json:"scope"`
	BoundKind               string         `json:"bound_kind"`
	Runtime                 string         `json:"runtime"`
	NativeTokenAccounting   string         `json:"native_token_accounting"`
	Outcome                 Outcome        `json:"outcome"`
	Complete                bool           `json:"complete"`
	Counts                  Counts         `json:"counts"`
	Cells                   []Verification `json:"cells"`
	Tiers                   []Tier         `json:"tiers"`
	KnownPrefixUTF8Bytes    *int           `json:"known_prefix_utf8_bytes,omitempty"`
	PossiblePrefixUTF8Bytes *int           `json:"possible_prefix_utf8_bytes,omitempty"`
	VerifiedPrefixUTF8Bytes *int           `json:"verified_prefix_utf8_bytes,omitempty"`
	AtLeastLargestTested    bool           `json:"at_least_largest_tested"`
}
