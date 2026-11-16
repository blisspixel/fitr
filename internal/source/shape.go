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
	// Loop fields are file metadata. Empty means the key was absent, not that
	// a loop count of 1, 2, or 4 was assumed. recurrent_layer_count is the
	// scalar loop field and is distinct from recurrent_layers.
	NumLoops                  int    `json:"num_loops,omitempty"`
	NumLoopsState             string `json:"num_loops_state,omitempty"`
	RecurrentKVCacheMode      string `json:"recurrent_kv_cache_mode,omitempty"`
	RecurrentKVCacheModeState string `json:"recurrent_kv_cache_mode_state,omitempty"`
	InputLayerCount           int    `json:"input_layer_count,omitempty"`
	InputLayerCountState      string `json:"input_layer_count_state,omitempty"`
	RecurrentLayerCount       int    `json:"recurrent_layer_count,omitempty"`
	RecurrentLayerCountState  string `json:"recurrent_layer_count_state,omitempty"`
	OutputLayerCount          int    `json:"output_layer_count,omitempty"`
	OutputLayerCountState     string `json:"output_layer_count_state,omitempty"`
	LayerPatternState         string `json:"layer_pattern_state,omitempty"`
	UnsizableReasonCode       string `json:"unsizable_reason_code,omitempty"`
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
	return s.validateLoop()
}

// maxLoopCount matches advise maxArchDim. A larger declared count is not a
// smaller usable count.
const maxLoopCount = 1 << 20

func (s *ArchitectureShape) validateLoop() error {
	checks := []struct {
		count int
		state string
		name  string
	}{
		{s.NumLoops, s.NumLoopsState, "num_loops"},
		{s.InputLayerCount, s.InputLayerCountState, "input_layer_count"},
		{s.RecurrentLayerCount, s.RecurrentLayerCountState, "recurrent_layer_count"},
		{s.OutputLayerCount, s.OutputLayerCountState, "output_layer_count"},
	}
	for _, check := range checks {
		if err := validLoopCount(check.count, check.state, check.name); err != nil {
			return err
		}
	}
	switch s.RecurrentKVCacheModeState {
	case "", "declared", "unmeasured":
	default:
		return errors.New("recurrent KV cache mode state is invalid")
	}
	switch {
	case s.RecurrentKVCacheModeState == "" && s.RecurrentKVCacheMode != "":
		return errors.New("recurrent KV cache mode is missing its state")
	case s.RecurrentKVCacheModeState == "unmeasured" && s.RecurrentKVCacheMode != "":
		return errors.New("unmeasured recurrent KV cache mode cannot carry a value")
	case s.RecurrentKVCacheModeState == "declared" && s.RecurrentKVCacheMode != "per_loop" && s.RecurrentKVCacheMode != "last_loop":
		return errors.New("declared recurrent KV cache mode is invalid")
	}
	if s.LayerPatternState != "" && s.LayerPatternState != "unmeasured" {
		return errors.New("layer pattern state is invalid")
	}
	if s.UnsizableReasonCode != "" && s.UnsizableReasonCode != "looped-architecture" {
		return errors.New("unsupported unsizable reason code")
	}
	if s.UnsizableReasonCode == "looped-architecture" && s.KVSizable {
		return errors.New("a looped architecture cannot claim a KV projection")
	}
	return nil
}

func validLoopCount(count int, state, name string) error {
	switch state {
	case "":
		if count != 0 {
			return errors.New(name + " is missing its state")
		}
	case "declared":
		if count < 1 || count > maxLoopCount {
			return errors.New(name + " declaration is unmeasured")
		}
	case "unmeasured":
		if count != 0 {
			return errors.New("unmeasured " + name + " cannot carry a value")
		}
	default:
		return errors.New(name + " state is invalid")
	}
	return nil
}
