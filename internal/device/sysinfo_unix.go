//go:build !windows

package device

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

func run(ctx context.Context, name string, args ...string) string {
	if _, err := exec.LookPath(name); err != nil {
		return ""
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = 250 * time.Millisecond
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func gpuInfo(ctx context.Context) (name, driver, date string) {
	if runtime.GOOS == "darwin" {
		raw := run(ctx, "system_profiler", "SPDisplaysDataType")
		if m := regexp.MustCompile(`Chipset Model:\s*(.+)`).FindStringSubmatch(raw); len(m) > 1 {
			return strings.TrimSpace(m[1]), "", ""
		}
		return runtime.GOARCH, "", ""
	}
	// NVIDIA first: nvidia-smi is the most precise source when present.
	if nv := run(ctx, "nvidia-smi", "--query-gpu=name,driver_version", "--format=csv,noheader"); nv != "" {
		parts := strings.SplitN(strings.SplitN(nv, "\n", 2)[0], ",", 2)
		if len(parts) == 2 {
			return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), ""
		}
	}
	if ro := run(ctx, "rocm-smi", "--showproductname"); ro != "" {
		if m := regexp.MustCompile(`Card series:\s*(.+)`).FindStringSubmatch(ro); len(m) > 1 {
			return strings.TrimSpace(m[1]), "", ""
		}
		return "AMD ROCm GPU", "", ""
	}
	if l := run(ctx, "sh", "-c", "lspci | grep -iE -m1 'vga|3d|display'"); l != "" {
		if i := strings.LastIndex(l, ":"); i >= 0 {
			return strings.TrimSpace(l[i+1:]), "", ""
		}
	}
	return "unknown", "", ""
}

func cpuName(ctx context.Context) string {
	if runtime.GOOS == "darwin" {
		return run(ctx, "sysctl", "-n", "machdep.cpu.brand_string")
	}
	b, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		return runtime.GOARCH
	}
	if m := regexp.MustCompile(`model name\s*:\s*(.+)`).FindStringSubmatch(string(b)); len(m) > 1 {
		return strings.TrimSpace(m[1])
	}
	return runtime.GOARCH
}

func vramInfo(ctx context.Context) (float64, string) {
	if gb := nvidiaSMIMemory(ctx); gb > 0 {
		return gb, "nvidia-smi"
	}
	if runtime.GOOS == "darwin" {
		if r := ramGB(ctx); r > 0 {
			return r, "unified memory (system RAM)"
		}
	}
	if gb := drmVRAM(); gb > 0 {
		return gb, "drm sysfs"
	}
	return 0, ""
}

func drmVRAM() float64 {
	matches, err := filepath.Glob("/sys/class/drm/card*/device/mem_info_vram_total")
	if err != nil {
		return 0
	}
	var best float64
	for _, p := range matches {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		n, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
		if err != nil || n <= 0 {
			continue
		}
		gb := float64(n) / GB
		if gb > best {
			best = gb
		}
	}
	return best
}

func ramGB(ctx context.Context) float64 {
	if runtime.GOOS == "darwin" {
		n, err := strconv.ParseInt(run(ctx, "sysctl", "-n", "hw.memsize"), 10, 64)
		if err != nil {
			return 0
		}
		return float64(n) / GB
	}
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	if m := regexp.MustCompile(`MemTotal:\s+(\d+)`).FindStringSubmatch(string(b)); len(m) > 1 {
		kb, _ := strconv.ParseFloat(m[1], 64)
		return kb * 1024 / GB
	}
	return 0
}

// ProbeTooling reports the interpreter the platform probes run through. The
// unix probes read sysfs and run vendor binaries directly, with no shell in
// between, so there is no interpreter version to record.
func ProbeTooling(context.Context) string { return "" }
