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

func gpuInfo(ctx context.Context) (name, driver, date string) {
	raw := ps(ctx, `Get-CimInstance Win32_VideoController | Select-Object -First 1 `+
		`Name,DriverVersion,@{n='DriverDate';e={$_.DriverDate.ToString('yyyy-MM-dd')}} `+
		`| ConvertTo-Json -Compress`)
	var d struct {
		Name          string `json:"Name"`
		DriverVersion string `json:"DriverVersion"`
		DriverDate    string `json:"DriverDate"`
	}
	if json.Unmarshal([]byte(raw), &d) != nil {
		return "", "", ""
	}
	return d.Name, d.DriverVersion, d.DriverDate
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
