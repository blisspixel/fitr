// Package reference seals an opt-in external coding pack and derives
// its rate from recorded verifier output. It does not launch a harness,
// a container, or a model, and it never feeds FIT, PERFORMANCE, a role
// floor, or a global score.
package reference

const (
	PlanSchema         = "fitr.external-reference.plan.v1"
	ResultSchema       = "fitr.external-reference.v1"
	VerifierSchema     = "fitr.external-reference.verifier.v1"
	ConditionsSchema   = "fitr.external-reference.conditions.v1"
	ImagesSchema       = "fitr.external-reference.images.v1"
	EvidenceClass      = "external_reference"
	manifestDomain     = "fitr.external-reference.manifest.v1"
	harnessDomain      = "fitr.external-reference.harness.v1"
	observationDomain  = "fitr.external-reference.observations.v1"
	rateMeasured       = "measured"
	rateUnmeasured     = "unmeasured"
	contaminationClean = "clean"

	OutcomeResolved     = "resolved"
	OutcomeUnresolved   = "unresolved"
	OutcomeHarness      = "harness_error"
	OutcomeEnvironment  = "environment_error"
	OutcomeTimeout      = "timeout"
	ContaminationMaybe  = "possibly_contaminated"
	ContaminationAbsent = "unmeasured"

	// ResultNote is the limit carried on every derived result.
	ResultNote = "recorded verifier output is not fresh evidence. " +
		"This process did not launch a harness, a container, or a model. " +
		"GPU load and residency here are operator declarations, not device probes. " +
		"The worker probe does not prove a benchmark container was unprivileged. " +
		"The pack does not feed FIT, PERFORMANCE, a role floor, or a global score."
)

const (
	maxK                = 20
	maxSteps            = 500
	maxTaskWall         = 8 * 3600
	maxContext          = 16 << 20
	maxWallBudget       = 24 * 3600
	maxDisk       int64 = 512 << 30
	maxTokens     int64 = 20_000_000
	maxRate       int64 = 1_000_000
	maxObserved   int64 = 50_000_000
	maxWallMillis int64 = 24 * 3600 * 1000

	MaxVerifierBytes   = 1 << 20
	MaxPlanBytes       = 256 << 10
	MaxConditionsBytes = 8 << 10
	MaxImagesBytes     = 64 << 10
	MaxResultBytes     = 1 << 20
)

// Instance is one sealed task. The digest is supplied by the operator.
// fitr does not look up an image and does not treat a name as a digest.
type Instance struct {
	ID          string `json:"instance_id"`
	Created     string `json:"created"`
	ImageDigest string `json:"image_digest"`
}

// Plan is the schedule sealed before any verifier file is accepted.
type Plan struct {
	Schema                 string     `json:"schema"`
	PlanSHA256             string     `json:"plan_sha256"`
	ManifestSHA256         string     `json:"manifest_sha256"`
	HarnessBindingSHA256   string     `json:"harness_binding_sha256"`
	Dataset                string     `json:"dataset"`
	DatasetRevision        string     `json:"dataset_revision"`
	DatasetSplit           string     `json:"dataset_split"`
	SubsetID               string     `json:"subset_id"`
	Instances              []Instance `json:"instances"`
	HarnessName            string     `json:"harness_name"`
	HarnessVersion         string     `json:"harness_version"`
	HarnessConfigSHA256    string     `json:"harness_config_sha256"`
	StepLimit              int        `json:"step_limit"`
	TaskWallSeconds        int        `json:"task_wall_seconds"`
	ContextCap             int        `json:"context_cap"`
	K                      int        `json:"k"`
	SeedSet                string     `json:"seed_set"`
	WallBudgetSeconds      int64      `json:"wall_budget_seconds"`
	DiskBudgetBytes        int64      `json:"disk_budget_bytes"`
	RequiredDiskBytes      int64      `json:"required_disk_bytes"`
	PrefillTokensPerSecond int64      `json:"prefill_tokens_per_second"`
	DecodeTokensPerSecond  int64      `json:"decode_tokens_per_second"`
	PromptTokens           int64      `json:"prompt_tokens"`
	CompletionTokens       int64      `json:"completion_tokens"`
	ArtifactSHA256         string     `json:"artifact_sha256"`
	ArtifactRelease        string     `json:"artifact_release"`
	Quant                  string     `json:"quant"`
	Runtime                string     `json:"runtime"`
	RuntimeBuild           string     `json:"runtime_build"`
	DeviceKey              string     `json:"device_key"`
}

// RunConditions is what preflight accepted. The booleans for the GPU and
// a resident model are declarations. The worker probe is the live isolate
// probe the command recorded, not a container audit.
type RunConditions struct {
	GPUBusyDeclared      bool   `json:"gpu_busy_declared"`
	OtherModelDeclared   bool   `json:"other_model_declared"`
	WorkerProbeAvailable bool   `json:"worker_probe_available"`
	WorkerProbeReason    string `json:"worker_probe_reason,omitempty"`
	ServedContext        int    `json:"served_context"`
}

// Observation is one upstream verifier row. AgentClaim is retained only
// so a load can show that it was ignored. It is never the outcome.
type Observation struct {
	InstanceID        string   `json:"instance_id"`
	Repeat            int      `json:"repeat"`
	Verdict           string   `json:"verdict"`
	Steps             int      `json:"steps"`
	PromptTokens      int64    `json:"prompt_tokens"`
	CompletionTokens  int64    `json:"completion_tokens"`
	WallMillis        int64    `json:"wall_ms"`
	VerifierLogSHA256 string   `json:"verifier_log_sha256"`
	AgentClaim        string   `json:"agent_claim,omitempty"`
	CacheHitTokens    *int64   `json:"cache_hit_tokens,omitempty"`
	APICostUSD        *float64 `json:"api_cost_usd,omitempty"`
	EnergyJoules      *float64 `json:"energy_joules,omitempty"`
}

// TaskResult is the derived row. Outcome comes from Verdict.
type TaskResult struct {
	InstanceID        string `json:"instance_id"`
	Repeat            int    `json:"repeat"`
	Created           string `json:"created"`
	Outcome           string `json:"outcome"`
	Contamination     string `json:"contamination"`
	Steps             int    `json:"steps"`
	PromptTokens      int64  `json:"prompt_tokens"`
	CompletionTokens  int64  `json:"completion_tokens"`
	WallMillis        int64  `json:"wall_ms"`
	VerifierLogSHA256 string `json:"verifier_log_sha256"`
	AgentClaimIgnored bool   `json:"agent_claim_ignored,omitempty"`
	CacheHitTokens    *int64 `json:"cache_hit_tokens,omitempty"`
}

// Counts are shown even when they stay out of the rate.
type Counts struct {
	Resolved                int `json:"resolved"`
	Unresolved              int `json:"unresolved"`
	HarnessError            int `json:"harness_error"`
	EnvironmentError        int `json:"environment_error"`
	Timeout                 int `json:"timeout"`
	PossiblyContaminated    int `json:"possibly_contaminated"`
	ContaminationUnmeasured int `json:"contamination_unmeasured"`
}

// Rate is a Wilson interval when the scorable denominator is positive.
// Errors and contaminated rows do not enter the clean denominator.
type Rate struct {
	Resolved    int      `json:"resolved"`
	Denominator int      `json:"denominator"`
	Estimate    *float64 `json:"estimate,omitempty"`
	Lo          *float64 `json:"lo,omitempty"`
	Hi          *float64 `json:"hi,omitempty"`
	Status      string   `json:"status"`
	Reason      string   `json:"reason,omitempty"`
}

// Result is derived at load from the plan and the observations.
// Fresh is always false: a file of verifier output is not a live run.
type Result struct {
	Schema           string        `json:"schema"`
	EvidenceClass    string        `json:"evidence_class"`
	Fresh            bool          `json:"fresh"`
	RecordedFixture  bool          `json:"recorded_fixture"`
	Plan             Plan          `json:"plan"`
	Conditions       RunConditions `json:"conditions"`
	Observations     []Observation `json:"observations"`
	Tasks            []TaskResult  `json:"tasks"`
	Clean            Counts        `json:"clean"`
	Contaminated     Counts        `json:"contaminated"`
	Unmeasured       Counts        `json:"contamination_unmeasured"`
	CleanRate        Rate          `json:"clean_rate"`
	ContaminatedRate Rate          `json:"contaminated_rate"`
	TotalWallMillis  int64         `json:"total_wall_ms"`
	EnergyJoules     *float64      `json:"energy_joules,omitempty"`
	APICostUSD       *float64      `json:"api_cost_usd,omitempty"`
	Note             string        `json:"note"`
}

// Pair is the fixed-plan comparison of two results. The feed flags stay
// false so a caller cannot read a regression as a fit or a score.
type Pair struct {
	Comparable       bool     `json:"comparable"`
	Reason           string   `json:"reason,omitempty"`
	LeftArtifact     string   `json:"left_artifact,omitempty"`
	RightArtifact    string   `json:"right_artifact,omitempty"`
	Shared           int      `json:"shared"`
	Concordant       int      `json:"concordant"`
	LeftOnly         int      `json:"left_only"`
	RightOnly        int      `json:"right_only"`
	Discordant       int      `json:"discordant"`
	Excluded         int      `json:"excluded"`
	Separable        bool     `json:"separable"`
	PExact           *float64 `json:"p_exact,omitempty"`
	FeedsFit         bool     `json:"feeds_fit"`
	FeedsPerformance bool     `json:"feeds_performance"`
	FeedsRoleFloor   bool     `json:"feeds_role_floor"`
	FeedsGlobalScore bool     `json:"feeds_global_score"`
}
