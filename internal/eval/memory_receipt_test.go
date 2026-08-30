package eval

import "testing"

func TestMemoryResultReceiptValidation(t *testing.T) {
	effective := 32768
	valid := MemoryResult{
		Outcome: OutcomePass, ResidentGB: 20, PctOnGPU: 75, RequestedCtx: 32768, EffectiveCtx: &effective,
		ResidentBytes: 20 << 30, AcceleratorBytes: 15 << 30,
	}
	if err := valid.ValidateReceipt(); err != nil {
		t.Fatal(err)
	}
	legacy := MemoryResult{ResidentGB: 20}
	if err := legacy.ValidateReceipt(); err != nil {
		t.Fatalf("legacy receipt should remain readable: %v", err)
	}
	badEffective := 0
	for name, receipt := range map[string]MemoryResult{
		"negative requested context":  {Outcome: OutcomeSkipped, RequestedCtx: -1, UnavailableReason: "none"},
		"invalid effective context":   {Outcome: OutcomeSkipped, RequestedCtx: 32768, EffectiveCtx: &badEffective, UnavailableReason: "none"},
		"accelerator exceeds total":   {Outcome: OutcomePass, RequestedCtx: 32768, ResidentBytes: 1, AcceleratorBytes: 2},
		"missing exact bytes":         {Outcome: OutcomePass, RequestedCtx: 32768, ResidentGB: 1},
		"percentage mismatch":         {Outcome: OutcomePass, RequestedCtx: 32768, ResidentBytes: 100, AcceleratorBytes: 50, PctOnGPU: 49},
		"skipped without reason":      {Outcome: OutcomeSkipped, RequestedCtx: 32768},
		"skipped with GPU percentage": {Outcome: OutcomeSkipped, RequestedCtx: 32768, PctOnGPU: 50, UnavailableReason: "none"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := receipt.ValidateReceipt(); err == nil {
				t.Fatalf("invalid receipt was accepted: %+v", receipt)
			}
		})
	}
	if got, ok := valid.VerifiedAt(32768); !ok || got != 20 {
		t.Fatalf("verified allocation = %v, %v", got, ok)
	}
	if _, ok := valid.VerifiedAt(8192); ok {
		t.Fatal("32K receipt was reused at 8K")
	}
	allocation, ok := valid.VerifiedAllocationAt(32768)
	if !ok || allocation.ResidentBytes != 20<<30 || allocation.AcceleratorBytes != 15<<30 ||
		allocation.RequestedCtx != 32768 || allocation.EffectiveCtx != 32768 {
		t.Fatalf("verified allocation = %+v, %v", allocation, ok)
	}
	if _, ok := valid.VerifiedAllocationAt(8192); ok {
		t.Fatal("32K allocation was reused at 8K")
	}
	invalid := valid
	invalid.PctOnGPU = 74
	if _, ok := invalid.VerifiedAllocationAt(32768); ok {
		t.Fatal("an internally inconsistent receipt was returned as verified")
	}
}
