package source

import "errors"

// ArchitectureShape contains the architecture facts and cache figures
// derived by the arithmetic owner. Presentation never recomputes these facts.
type ArchitectureShape struct {
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

func (s *ArchitectureShape) validate() error {
	if s == nil {
		return nil
	}
	if len(s.Name) > 128 {
		return errors.New("architecture name exceeds 128 characters")
	}
	if s.Blocks < 0 || s.RealBlocks < 0 || s.Heads < 0 || s.KVHeads < 0 ||
		s.KeyLength < 0 || s.ValLength < 0 || s.MaxCtx < 0 || s.Experts < 0 ||
		s.ExpertUsed < 0 || s.FullAttentionInterval < 0 || s.FullAttentionLayers < 0 ||
		s.RecurrentLayers < 0 || s.NextNPredictLayers < 0 || s.SlidingWindow < 0 ||
		s.SlidingWindowPattern < 0 || s.KeyLengthSWA < 0 || s.ValLengthSWA < 0 ||
		s.SSMInnerSize < 0 || s.SSMStateSize < 0 || s.SSMConvKernel < 0 ||
		s.KVBytesPerToken < 0 || s.FixedCacheBytes < 0 {
		return errors.New("architecture shape contains negative dimensions")
	}
	if len(s.KVHeadsPerLayer) > 1024 {
		return errors.New("per-layer KV heads array exceeds 1024 entries")
	}
	for _, heads := range s.KVHeadsPerLayer {
		if heads < 0 {
			return errors.New("per-layer KV head count cannot be negative")
		}
	}
	if s.SSMGroupCount != nil && *s.SSMGroupCount < 0 {
		return errors.New("SSM group count cannot be negative")
	}
	return nil
}
