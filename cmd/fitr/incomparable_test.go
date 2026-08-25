package main

import (
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/device"
)

// Refusing to compare is correct, but "re-measure both on this machine" is a
// dead end when both runs already happened on it. The usual cause is that the
// machine's state moved between them -- something else took VRAM, so one run
// was partly offloaded -- and the fingerprint delta already names it.
func TestIncomparableNoteNamesWhatActuallyMoved(t *testing.T) {
	a := device.Fingerprint{Host: "box", GPU: "RTX 4090", InferenceDevice: "GPU 26%"}
	b := device.Fingerprint{Host: "box", GPU: "RTX 4090", InferenceDevice: "GPU 74%"}

	note := incomparableNote(a.Diff(b))
	if !strings.Contains(note, "inference_device") {
		t.Fatalf("note does not name the field that differs: %q", note)
	}
	if !strings.Contains(note, "GPU 26%") || !strings.Contains(note, "GPU 74%") {
		t.Fatalf("note does not show both values: %q", note)
	}
	// It must not blame the hardware when the hardware is identical.
	if strings.Contains(note, "host") || strings.Contains(note, "gpu ") {
		t.Fatalf("note blames unchanged hardware: %q", note)
	}

	// With no delta there is nothing to name, and the generic reason stands.
	if got := incomparableNote(nil); !strings.Contains(got, "device-specific") {
		t.Fatalf("empty delta produced %q", got)
	}
}
