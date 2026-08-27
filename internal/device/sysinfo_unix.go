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
		return appleGPUBudget(ctx)
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

// appleGPUBudget reports how much memory the GPU may actually use, not how much
// is installed.
//
// Apple Silicon shares one pool, but the GPU cannot wire all of it: the kernel
// caps the working set, and `sysctl iogpu.wired_limit_mb` both reports an
// explicit override and is how users raise it. Reading hw.memsize and calling
// it VRAM is the unified-memory version of grading against total instead of
// free, which is precisely the failure fitr exists to catch elsewhere.
func appleGPUBudget(ctx context.Context) (float64, string) {
	ram := ramGB(ctx)
	if ram <= 0 {
		return 0, ""
	}
	// An explicit override is a measurement: the user set it, the kernel honours
	// it, and it needs no assumption.
	if mb, err := strconv.ParseInt(strings.TrimSpace(
		run(ctx, "sysctl", "-n", "iogpu.wired_limit_mb")), 10, 64); err == nil && mb > 0 {
		if gb := float64(mb) * 1024 * 1024 / GB; gb > 0 && gb <= ram {
			return gb, AppleWiredLimitSource
		}
	}
	return ram * appleWiredLimitFraction, AppleAssumedShareSource
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

// availableVRAMFallback reads free VRAM where a vendor tool is absent.
//
// AMD on Linux exposes used and total bytes in the same sysfs directory the
// total already comes from, so free is a subtraction and needs no new
// dependency and no root.
//
// Apple Silicon deliberately returns nothing. There is no separate VRAM to be
// free: the GPU budget is a share of one pool that the OS is also using, and
// "free system RAM" is a different question wearing the same words. Reporting
// it would invent a number.
func availableVRAMFallback(context.Context) (float64, bool) {
	totals, err := filepath.Glob("/sys/class/drm/card*/device/mem_info_vram_total")
	if err != nil {
		return 0, false
	}
	var best float64
	var found bool
	for _, totalPath := range totals {
		total := readSysfsUint(totalPath)
		used := readSysfsUint(strings.Replace(totalPath, "_total", "_used", 1))
		if total <= 0 || used < 0 || used > total {
			continue
		}
		if gb := float64(total-used) / GB; gb > best {
			best, found = gb, true
		}
	}
	return best, found
}

func readSysfsUint(path string) int64 {
	b, err := os.ReadFile(path)
	if err != nil {
		return -1
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	if err != nil || n < 0 {
		return -1
	}
	return n
}
