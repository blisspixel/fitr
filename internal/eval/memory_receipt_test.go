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
}
