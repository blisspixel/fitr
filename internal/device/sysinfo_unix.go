//go:build !windows

package device

import (
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
)

func run(name string, args ...string) string {
	if _, err := exec.LookPath(name); err != nil {
		return ""
	}
	out, err := exec.Command(name, args...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func gpuInfo() (name, driver, date string) {
	if runtime.GOOS == "darwin" {
		raw := run("system_profiler", "SPDisplaysDataType")
		if m := regexp.MustCompile(`Chipset Model:\s*(.+)`).FindStringSubmatch(raw); len(m) > 1 {
			return strings.TrimSpace(m[1]), "", ""
		}
		return runtime.GOARCH, "", ""
	}
	// NVIDIA first: nvidia-smi is the most precise source when present.
	if nv := run("nvidia-smi", "--query-gpu=name,driver_version", "--format=csv,noheader"); nv != "" {
		parts := strings.SplitN(strings.SplitN(nv, "\n", 2)[0], ",", 2)
		if len(parts) == 2 {
			return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), ""
		}
	}
	if ro := run("rocm-smi", "--showproductname"); ro != "" {
		if m := regexp.MustCompile(`Card series:\s*(.+)`).FindStringSubmatch(ro); len(m) > 1 {
			return strings.TrimSpace(m[1]), "", ""
		}
		return "AMD ROCm GPU", "", ""
	}
	if l := run("sh", "-c", "lspci | grep -iE -m1 'vga|3d|display'"); l != "" {
		if i := strings.LastIndex(l, ":"); i >= 0 {
			return strings.TrimSpace(l[i+1:]), "", ""
		}
	}
	return "unknown", "", ""
}

func cpuName() string {
	if runtime.GOOS == "darwin" {
		return run("sysctl", "-n", "machdep.cpu.brand_string")
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

func ramGB() float64 {
	if runtime.GOOS == "darwin" {
		n, err := strconv.ParseInt(run("sysctl", "-n", "hw.memsize"), 10, 64)
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
