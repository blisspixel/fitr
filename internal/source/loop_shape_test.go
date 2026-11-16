package source

import "testing"

func TestLoopShapeValidationKeepsAnOrdinaryShape(t *testing.T) {
	if err := (&ArchitectureShape{Name: "llama", Blocks: 32, KVSizable: true}).validate(); err != nil {
		t.Fatal(err)
	}
	declared := &ArchitectureShape{
		Name: "dory", NumLoops: 4, NumLoopsState: "declared",
		RecurrentKVCacheMode: "per_loop", RecurrentKVCacheModeState: "declared",
		InputLayerCount: 16, InputLayerCountState: "declared",
		RecurrentLayerCount: 40, RecurrentLayerCountState: "declared",
		OutputLayerCount: 16, OutputLayerCountState: "declared",
		UnsizableReasonCode: "looped-architecture",
	}
	if err := declared.validate(); err != nil {
		t.Fatal(err)
	}
	declared.KVSizable = true
	if err := declared.validate(); err == nil {
		t.Fatal("a looped shape claimed a KV projection")
	}
	declared.KVSizable = false
	declared.NumLoops = 2
	declared.NumLoopsState = ""
	if err := declared.validate(); err == nil {
		t.Fatal("a count without a state was accepted")
	}
}
