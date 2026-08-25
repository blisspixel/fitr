//go:build windows

package device

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func ps(ctx context.Context, script string) string {
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	exe := filepath.Join(root, `System32\WindowsPowerShell\v1.0\powershell.exe`)
	if _, err := os.Stat(exe); err != nil {
		return ""
	}
	cmd := exec.CommandContext(ctx, exe, "-NoProfile", "-NonInteractive", "-Command", script)
	cmd.WaitDelay = 250 * time.Millisecond
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// videoController is one Win32_VideoController row. AdapterRAM is a uint32
// that silently caps at 4 GB, so it ranks adapters but never sizes them.
type videoController struct {
	Name          string `json:"Name"`
	DriverVersion string `json:"DriverVersion"`
	DriverDate    string `json:"DriverDate"`
	AdapterRAM    int64  `json:"AdapterRAM"`
}

func gpuInfo(ctx context.Context) (name, driver, date string) {
	// Windows PowerShell 5.1 has no ConvertTo-Json -AsArray, and it collapses a
	// single adapter to a bare object, so both shapes have to be accepted here.
	raw := ps(ctx, `Get-CimInstance Win32_VideoController | Select-Object `+
		`Name,DriverVersion,AdapterRAM,@{n='DriverDate';e={$_.DriverDate.ToString('yyyy-MM-dd')}} `+
		`| ConvertTo-Json -Compress`)
	all, ok := parseVideoControllers(raw)
	if !ok {
		return "", "", ""
	}
	c := pickVideoController(all, nvidiaSMIName(ctx))
	return c.Name, c.DriverVersion, c.DriverDate
}

// parseVideoControllers accepts either the JSON array PowerShell emits for
// several adapters or the bare object it emits for exactly one.
func parseVideoControllers(raw string) ([]videoController, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, false
	}
	if strings.HasPrefix(raw, "[") {
		var all []videoController
		if json.Unmarshal([]byte(raw), &all) != nil {
			return nil, false
		}
		return all, true
	}
	var one videoController
	if json.Unmarshal([]byte(raw), &one) != nil {
		return nil, false
	}
	return []videoController{one}, true
}

// pickVideoController chooses the adapter that actually serves inference.
// Taking the first row picks whatever Windows enumerated first, which on a box
// with a headset dock or remote-desktop driver is a virtual monitor with no
// memory -- and GPU name is part of the device fingerprint key, so naming the
// wrong adapter silently partitions this machine's evidence. Prefer the card
// nvidia-smi already sized, then the largest reported adapter.
func pickVideoController(all []videoController, nvName string) videoController {
	if len(all) == 0 {
		return videoController{}
	}
	if nvName != "" {
		for _, c := range all {
			if strings.EqualFold(strings.TrimSpace(c.Name), nvName) {
				return c
			}
		}
		// nvidia-smi and CIM spell the same card differently often enough
		// ("NVIDIA GeForce RTX 4090" vs a vendor suffix) to warrant a
		// containment pass before falling back to size. Take the most
		// specific match: a stub adapter named "NVIDIA" is a substring of
		// every NVIDIA card, and letting it win reintroduces exactly the
		// wrong-fingerprint bug the exact pass above prevents.
		best, bestLen := videoController{}, -1
		for _, c := range all {
			n := strings.TrimSpace(c.Name)
			if n == "" || !(strings.Contains(n, nvName) || strings.Contains(nvName, n)) {
				continue
			}
			if len(n) > bestLen {
				best, bestLen = c, len(n)
			}
		}
		if bestLen >= 0 {
			return best
		}
	}
	best := all[0]
	for _, c := range all[1:] {
		if c.AdapterRAM > best.AdapterRAM {
			best = c
		}
	}
	return best
}

func cpuName(ctx context.Context) string {
	return ps(ctx, `(Get-CimInstance Win32_Processor | Select-Object -First 1).Name`)
}

func vramInfo(ctx context.Context) (float64, string) {
	if gb := nvidiaSMIMemory(ctx); gb > 0 {
		return gb, "nvidia-smi"
	}
	// AdapterRAM is a uint32 and silently caps at 4 GB. qwMemorySize is the
	// 64-bit figure the driver actually registered.
	raw := ps(ctx, `Get-ItemProperty 'HKLM:\SYSTEM\CurrentControlSet\Control\Class\{4d36e968-e325-11ce-bfc1-08002be10318}\0*' -ErrorAction SilentlyContinue | Where-Object { $_.'HardwareInformation.qwMemorySize' -gt 1GB } | Select-Object -First 1 -ExpandProperty 'HardwareInformation.qwMemorySize'`)
	n, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err == nil && n > 0 {
		return float64(n) / GB, "registry qwMemorySize"
	}
	return 0, ""
}

func ramGB(ctx context.Context) float64 {
	n, err := strconv.ParseInt(ps(ctx, `(Get-CimInstance Win32_ComputerSystem).TotalPhysicalMemory`), 10, 64)
	if err != nil {
		return 0
	}
	return float64(n) / GB
}
