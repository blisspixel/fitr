//go:build windows

package device

import "testing"

// A headset dock, remote-desktop driver, or capture device enumerates ahead of
// the real card often enough that "first adapter" is not a device reading.
// GPU name is part of Fingerprint.Key, so the wrong name partitions evidence.
func TestPickVideoControllerSkipsVirtualAdapters(t *testing.T) {
	all := []videoController{
		{Name: "Meta Virtual Monitor", DriverVersion: "1.0.0.0"},
		{Name: "NVIDIA GeForce RTX 4090", DriverVersion: "32.0.16.1047", AdapterRAM: 4293918720},
	}
	got := pickVideoController(all, "NVIDIA GeForce RTX 4090")
	if got.Name != "NVIDIA GeForce RTX 4090" {
		t.Fatalf("got %q, want the card nvidia-smi sized", got.Name)
	}
	if got.DriverVersion != "32.0.16.1047" {
		t.Fatalf("driver %q must come from the same adapter as the name", got.DriverVersion)
	}
}

func TestPickVideoControllerFallsBackToLargestAdapter(t *testing.T) {
	all := []videoController{
		{Name: "Meta Virtual Monitor"},
		{Name: "AMD Radeon RX 7900 XTX", AdapterRAM: 4293918720},
	}
	// No nvidia-smi on an AMD box, so size has to break the tie.
	if got := pickVideoController(all, ""); got.Name != "AMD Radeon RX 7900 XTX" {
		t.Fatalf("got %q, want the largest adapter", got.Name)
	}
	if got := pickVideoController(nil, ""); got.Name != "" {
		t.Fatalf("no adapters must stay unmeasured, got %q", got.Name)
	}
}

func TestPickVideoControllerMatchesVendorSpelling(t *testing.T) {
	all := []videoController{
		{Name: "Intel(R) UHD Graphics 770", AdapterRAM: 1073741824},
		{Name: "NVIDIA GeForce RTX 4090 Laptop GPU", AdapterRAM: 4293918720},
	}
	got := pickVideoController(all, "NVIDIA GeForce RTX 4090 Laptop GPU")
	if got.Name != "NVIDIA GeForce RTX 4090 Laptop GPU" {
		t.Fatalf("got %q", got.Name)
	}
}

// Windows PowerShell 5.1 emits a bare object for a single adapter and an array
// for several; -AsArray does not exist there. Both shapes must parse or the
// whole GPU probe silently returns nothing.
func TestParseVideoControllersAcceptsBothJSONShapes(t *testing.T) {
	one, ok := parseVideoControllers(`{"Name":"NVIDIA GeForce RTX 4090","DriverVersion":"32.0.16.1047","AdapterRAM":4293918720,"DriverDate":"2025-01-02"}`)
	if !ok || len(one) != 1 || one[0].Name != "NVIDIA GeForce RTX 4090" {
		t.Fatalf("single-adapter object did not parse: ok=%v %+v", ok, one)
	}
	if one[0].DriverDate != "2025-01-02" {
		t.Fatalf("driver date %q", one[0].DriverDate)
	}

	many, ok := parseVideoControllers(`[{"Name":"Meta Virtual Monitor"},{"Name":"NVIDIA GeForce RTX 4090","AdapterRAM":4293918720}]`)
	if !ok || len(many) != 2 {
		t.Fatalf("array did not parse: ok=%v %+v", ok, many)
	}

	for _, bad := range []string{"", "   ", "not json", "<CimInstance>"} {
		if _, ok := parseVideoControllers(bad); ok {
			t.Fatalf("%q must not parse as adapters", bad)
		}
	}
}

// The containment pass exists for vendor spelling drift, but taking the first
// substring match lets a stub adapter named "NVIDIA" outrank the real card,
// which is the same wrong-fingerprint failure the exact pass prevents. The
// most specific name wins.
func TestPickVideoControllerPrefersTheMostSpecificNameMatch(t *testing.T) {
	all := []videoController{
		{Name: "NVIDIA", DriverVersion: "stub"},
		{Name: "NVIDIA GeForce RTX 4090 Laptop GPU", DriverVersion: "32.0.16.1047", AdapterRAM: 4293918720},
	}
	got := pickVideoController(all, "NVIDIA GeForce RTX 4090")
	if got.Name != "NVIDIA GeForce RTX 4090 Laptop GPU" {
		t.Fatalf("got %q, want the specific card, not the stub", got.Name)
	}
	if got.DriverVersion != "32.0.16.1047" {
		t.Fatalf("driver %q came from the wrong adapter", got.DriverVersion)
	}
}
