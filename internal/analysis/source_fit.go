package analysis

import "github.com/blisspixel/fitr/internal/source"

// SourceArchitectureShape contains the architecture facts and cache figures
// derived by the arithmetic owner. Presentation never recomputes these facts.
type SourceArchitectureShape struct {
	Name                  string `json:"name,omitempty"`
	Blocks                int    `json:"block_count,omitempty"`
	RealBlocks            int    `json:"attention_blocks,omitempty"`
	Heads                 int    `json:"head_count,omitempty"`
	KVHeads               int    `json:"head_count_kv,omitempty"`
	KVHeadsPerLayer       []int  `json:"head_count_kv_per_layer,omitempty"`
	KeyLength             int    `json:"key_length,omitempty"`
	ValLength             int    `json:"value_length,omitempty"`
	MaxCtx                int    `json:"context_length,omitempty"`
	Experts               int    `json:"expert_count,omitempty"`
	ExpertUsed            int    `json:"expert_used_count,omitempty"`
	FullAttentionInterval int    `json:"full_attention_interval,omitempty"`
	FullAttentionLayers   int    `json:"full_attention_layers,omitempty"`
	RecurrentLayers       int    `json:"recurrent_layers,omitempty"`
	NextNPredictLayers    int    `json:"nextn_predict_layers,omitempty"`
	SlidingWindow         int    `json:"sliding_window,omitempty"`
	SlidingWindowPattern  int    `json:"sliding_window_pattern,omitempty"`
	KeyLengthSWA          int    `json:"key_length_swa,omitempty"`
	ValLengthSWA          int    `json:"value_length_swa,omitempty"`
	SSMInnerSize          int    `json:"ssm_inner_size,omitempty"`
	SSMStateSize          int    `json:"ssm_state_size,omitempty"`
	SSMConvKernel         int    `json:"ssm_conv_kernel,omitempty"`
	SSMGroupCount         *int   `json:"ssm_group_count,omitempty"`
	Hybrid                bool   `json:"hybrid,omitempty"`
	KVSizable             bool   `json:"kv_sizable"`
	KVBytesPerToken       int64  `json:"kv_bytes_per_token,omitempty"`
	FixedCacheBytes       int64  `json:"fixed_cache_bytes,omitempty"`
	UnsizableReason       string `json:"unsizable_reason,omitempty"`
}

// SourceFitReport describes declared weights plus modeled cache under one
// explicit component ceiling. It never establishes runtime support or quality.
type SourceFitReport struct {
	Schema             string                   `json:"schema"`
	SourceSHA256       string                   `json:"source_sha256,omitempty"`
	ArchitectureStatus string                   `json:"architecture_status"`
	ArchitectureReason string                   `json:"architecture_reason"`
	ProjectionStatus   string                   `json:"projection_status"`
	ProjectionReason   string                   `json:"projection_reason"`
	RuntimeStatus      string                   `json:"runtime_status"`
	Files              []string                 `json:"files"`
	Context            int                      `json:"context,omitempty"`
	CapacityBytes      *int64                   `json:"capacity_bytes,omitempty"`
	CapacitySource     string                   `json:"capacity_source,omitempty"`
	WeightsBytes       *int64                   `json:"weights_bytes,omitempty"`
	CacheBytes         *int64                   `json:"cache_bytes,omitempty"`
	ComponentBytes     *int64                   `json:"component_bytes,omitempty"`
	Shape              *SourceArchitectureShape `json:"architecture_shape,omitempty"`
	Gaps               []string                 `json:"gaps"`
}

// SourceHeaderRead retains the selected filename, prefix digest and transport
// provenance without raw model bytes or a signed download location.
type SourceHeaderRead struct {
	Path        string                   `json:"path"`
	SHA256      string                   `json:"prefix_sha256,omitempty"`
	Observation source.PrefixObservation `json:"observation"`
}
