package device

import (
	"io"
	"strings"
	"testing"
)

func TestSelectedAvailabilityCannotBorrowAnotherCardsMemory(t *testing.T) {
	fp := Fingerprint{GPU: "NVIDIA RTX 4090", VRAMGb: 24, VRAMSource: "nvidia-smi"}
	for _, valid := range []string{"NVIDIA RTX 4090, 24576, 4096\n", "NVIDIA RTX 4090,24576,0\n"} {
		free, err := parseSingleNVIDIAAvailable(valid, fp)
		if err != nil || free < 0 || free > 4<<30 {
			t.Fatal(free, err)
		}
	}
	for _, invalid := range []string{
		"NVIDIA RTX 4090,24576,4096\nNVIDIA RTX 5090,32768,30000\n",
		"NVIDIA RTX 4090,24576,4096\nNVIDIA RTX 4090,24576,23000\n",
		"NVIDIA RTX 5090,24576,23000", "NVIDIA RTX 4090,32768,23000", "",
		"NVIDIA RTX 4090,24576,N/A", "NVIDIA RTX 4090,24576,30000", "NVIDIA RTX 4090,24576,-1",
		"NVIDIA RTX 4090,24576", "NVIDIA RTX 4090,24576,4096,extra",
	} {
		if free, err := parseSingleNVIDIAAvailable(invalid, fp); err == nil {
			t.Fatalf("ambiguous or invalid current availability accepted: %q = %d", invalid, free)
		}
	}
	fp.VRAMSource = "operator declaration"
	if _, err := parseSingleNVIDIAAvailable("NVIDIA RTX 4090,24576,4096", fp); err == nil {
		t.Fatal("declaration became observed device identity")
	}
	var out selectedMemoryOutput
	if _, err := out.Write([]byte(strings.Repeat("x", 8192))); err != nil {
		t.Fatal(err)
	}
	if _, err := out.Write([]byte("x")); err == nil || out.Len() != 8192 {
		t.Fatal("observation output limit ignored")
	}
	var copied selectedMemoryOutput
	source := struct{ io.Reader }{strings.NewReader(strings.Repeat("x", 8193))}
	if _, err := io.Copy(&copied, source); err == nil || copied.Len() > 8192 {
		t.Fatal("io.Copy bypassed observation output bounds")
	}
}
