// Package device fingerprints the machine and loads its gate profile.
//
// A model score is meaningless without the device it was measured on. The SAME
// model on one laptop scored "crashes on load" and "daily driver" depending
// only on a GPU driver update. So every result carries a fingerprint, and
// results are comparable only within a matching one.
//
// Gates live in spec/profiles/*.json, never in code. A profile is auto-selected
// by matching GPU or hostname; otherwise `default` is used and the run is
// clearly marked as running on uncalibrated thresholds.
package device

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/blisspixel/fitr/internal/boundedio"
	"github.com/blisspixel/fitr/internal/llm"
)

//go:embed all:profiles
var profilesFS embed.FS

const GB = 1024 * 1024 * 1024
const maxServerLogSample = 1 << 20

// Gate is one threshold set, with the reasoning attached. Every number in a
// profile carries a `why` so it can be argued with rather than cargo-culted.
type Gate map[string]any

type Profile struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Match       map[string]string `json:"match"`
	Notes       []string          `json:"notes"`
	Gates       map[string]Gate   `json:"gates"`
	Hints       map[string]any    `json:"hints"`
}

// Float returns a numeric gate field, and whether it was present. A missing
// gate must cause a SKIP, never a FAIL -- you cannot fail a test you did not run.
func (p Profile) Float(gate, key string) (float64, bool) {
	g, ok := p.Gates[gate]
	if !ok {
		return 0, false
	}
	v, ok := g[key]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	}
	return 0, false
}

func (p Profile) Bool(gate, key string) (bool, bool) {
	g, ok := p.Gates[gate]
	if !ok {
		return false, false
	}
	if v, ok := g[key].(bool); ok {
		return v, true
	}
	return false, false
}

func LoadEmbeddedProfiles() ([]Profile, error) {
	entries, err := profilesFS.ReadDir("profiles")
	if err != nil {
		return nil, err
	}
	var out []Profile
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := profilesFS.ReadFile(path.Join("profiles", e.Name()))
		if err != nil {
			return nil, fmt.Errorf("profile %s: %w", e.Name(), err)
		}
		p, err := decodeProfile("embedded/"+e.Name(), b)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

func LoadProfiles() ([]Profile, error) {
	embedded, err := LoadEmbeddedProfiles()
	if err != nil {
		return nil, err
	}
	user, err := loadUserProfiles()
	if err != nil {
		return nil, err
	}
	// User files first so a local calibration wins GPU/host match and
	// overrides an embedded profile of the same name.
	byName := map[string]Profile{}
	var order []string
	for _, p := range append(user, embedded...) {
		if _, seen := byName[p.Name]; seen {
			continue
		}
		byName[p.Name] = p
		order = append(order, p.Name)
	}
	out := make([]Profile, 0, len(order))
	for _, n := range order {
		out = append(out, byName[n])
	}
	return out, nil
}

// SelectProfile: explicit name wins, then GPU/host match, then `default`.
func SelectProfile(name string, fp Fingerprint) (Profile, error) {
	profs, err := LoadProfiles()
	if err != nil {
		return Profile{}, err
	}
	byName := map[string]Profile{}
	for _, p := range profs {
		byName[p.Name] = p
	}
	if name != "" {
		p, ok := byName[name]
		if !ok {
			var names []string
			for n := range byName {
				names = append(names, n)
			}
			sort.Strings(names)
			return Profile{}, fmt.Errorf("profile %q not found; available: %s",
				name, strings.Join(names, ", "))
		}
		return p, nil
	}
	gpu, host := strings.ToLower(fp.GPU), strings.ToLower(fp.Host)
	for _, p := range profs {
		if len(p.Match) == 0 {
			continue
		}
		if g := strings.ToLower(p.Match["gpu_contains"]); g != "" && strings.Contains(gpu, g) {
			return p, nil
		}
		if h := strings.ToLower(p.Match["host"]); h != "" && h == host {
			return p, nil
		}
	}
	p, ok := byName["default"]
	if !ok {
		return Profile{}, errors.New("default profile is missing")
	}
	return p, nil
}

// ---------------------------------------------------------------- fingerprint
type Fingerprint struct {
	Host          string  `json:"host"`
	OS            string  `json:"os"`
	CPU           string  `json:"cpu"`
	RAMGb         float64 `json:"ram_gb"`
	GPU           string  `json:"gpu"`
	GPUDriver     string  `json:"gpu_driver"`
	GPUDriverDate string  `json:"gpu_driver_date"`
	// Runtime is the serving runtime and version - "0.32.14" for Ollama
	// (bare, for continuity with results recorded before other backends
	// existed; hence the json tag), "llama-server b6xxx" otherwise. A runtime
	// change is a different measurement.
	Runtime         string `json:"ollama"`
	InferenceDevice string `json:"inference_device"`
	// GPUBackend is cuda|metal|vulkan|rocm|sycl|opencl|cpu - the compute
	// API the server is actually using. The runtime version implies it
	// poorly (a Vulkan llama-server and a CUDA one share a build number).
	GPUBackend string `json:"gpu_backend,omitempty"`
	// VRAMGb is the measured GPU (or unified) memory budget. Zero with an
	// empty VRAMSource means it was not measured - callers must SKIP, not
	// invent a card-from-name number.
	VRAMGb     float64           `json:"vram_gb,omitempty"`
	VRAMSource string            `json:"vram_source,omitempty"`
	Config     map[string]string `json:"config"`
}

// Key is what decides comparability. Two results may only be ranked against
// each other if this matches exactly.
// Diff lists fields that disagree. Used by `fitr tune` so a knob change is
// visible as a fingerprint delta rather than a silent incomparability.
func (f Fingerprint) Diff(o Fingerprint) [][3]string {
	var out [][3]string
	add := func(name, a, b string) {
		if a != b {
			out = append(out, [3]string{name, a, b})
		}
	}
	add("host", f.Host, o.Host)
	add("gpu", f.GPU, o.GPU)
	add("gpu_driver", f.GPUDriver, o.GPUDriver)
	add("gpu_backend", f.GPUBackend, o.GPUBackend)
	add("runtime", f.Runtime, o.Runtime)
	add("inference_device", f.InferenceDevice, o.InferenceDevice)
	keys := map[string]bool{}
	for k := range f.Config {
		keys[k] = true
	}
	for k := range o.Config {
		keys[k] = true
	}
	var names []string
	for k := range keys {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		add("config."+k, f.Config[k], o.Config[k])
	}
	return out
}

func (f Fingerprint) Key() string {
	c := f.Config
	return strings.Join([]string{
		f.Host, f.GPU, f.GPUDriver, f.GPUBackend, f.Runtime,
		c["OLLAMA_FLASH_ATTENTION"], c["OLLAMA_KV_CACHE_TYPE"],
	}, "|")
}

var configKeys = []string{
	"OLLAMA_MODELS", "OLLAMA_IGPU_ENABLE", "OLLAMA_FLASH_ATTENTION",
	"OLLAMA_KV_CACHE_TYPE", "OLLAMA_MAX_LOADED_MODELS", "OLLAMA_NUM_PARALLEL",
	"OLLAMA_CONTEXT_LENGTH", "LLAMA_ARG_FIT",
}

func Detect(ctx context.Context, b llm.Backend) Fingerprint {
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	host, _ := os.Hostname()
	// GPU/CPU/RAM/VRAM probes are independent process or sysfs reads.
	// On Windows each is a PowerShell round-trip; paying them in series
	// wastes cores and hundreds of milliseconds before the first line prints.
	var gpu, drv, date, cpu string
	var ram, vram float64
	var vsrc string
	var wg sync.WaitGroup
	wg.Add(4)
	go func() { defer wg.Done(); gpu, drv, date = gpuInfo(probeCtx) }()
	go func() { defer wg.Done(); cpu = cpuName(probeCtx) }()
	go func() { defer wg.Done(); ram = ramGB(probeCtx) }()
	go func() { defer wg.Done(); vram, vsrc = vramInfo(probeCtx) }()

	cfg := map[string]string{}
	version := ""
	isOllama := b != nil && b.Name() == "ollama"
	if isOllama {
		for _, k := range configKeys {
			cfg[k] = os.Getenv(k)
		}
		// The server log is authoritative for how Ollama was actually started,
		// which frequently differs from this process's environment.
		mergeServerLogConfig(cfg)
	}
	if b != nil {
		version = b.Version(probeCtx)
	}
	accel := ""
	if a, ok := b.(interface{ Accel(context.Context) string }); ok {
		accel = NormalizeAccel(a.Accel(probeCtx))
	}
	placement := inferenceDevice(probeCtx, b, "")
	wg.Wait()
	vram, vsrc = preferUnifiedMemory(gpu, ram, vram, vsrc)
	return Fingerprint{
		Host: host, OS: runtime.GOOS, CPU: cpu, RAMGb: ram,
		GPU: gpu, GPUDriver: drv, GPUDriverDate: date,
		Runtime: version, InferenceDevice: placement,
		GPUBackend: accel, VRAMGb: vram, VRAMSource: vsrc, Config: cfg,
	}
}

// FormatCPU is display-only. Logical CPU count is not part of Fingerprint.Key:
// adding it would void comparable history without changing what a measurement
// means. The serving runtime, not fitr, owns inference threads.
// preferUnifiedMemory replaces a discrete-carve VRAM reading on APUs with
// system RAM. Windows registry qwMemorySize often reports ~2 GB of shared
// graphics memory on a 32-128 GB 780M / Strix Halo box; treating that as the
// model budget marks everything incompatible. NVIDIA remains nvidia-smi.
func preferUnifiedMemory(gpu string, ram, vram float64, src string) (float64, string) {
	if ram <= 0 || !unifiedMemoryGPU(gpu) {
		return vram, src
	}
	if src == "nvidia-smi" || src == "unified memory (system RAM)" || src == "drm sysfs" {
		return vram, src
	}
	if vram > 0 && ram < vram*2 {
		return vram, src
	}
	return ram, "unified memory (system RAM)"
}

func unifiedMemoryGPU(name string) bool {
	u := strings.ToLower(name)
	if u == "" {
		return false
	}
	if strings.Contains(u, "nvidia") || strings.Contains(u, "geforce") || strings.Contains(u, "rtx ") {
		return false
	}
	if strings.Contains(u, " rx ") || strings.HasPrefix(strings.TrimSpace(u), "rx ") {
		return false
	}
	if strings.Contains(u, "iris") || strings.Contains(u, "uhd graphics") || strings.Contains(u, "intel graphics") {
		return true
	}
	if strings.Contains(u, "apple") || strings.Contains(u, "metal") {
		return true
	}
	if strings.Contains(u, "radeon") {
		return strings.Contains(u, "strix") || strings.Contains(u, "graphics") ||
			strings.Contains(u, "m") || strings.Contains(u, "8060s") || strings.Contains(u, "8050s")
	}
	return false
}

func FormatCPU(name string) string {
	if name == "" {
		name = "unknown"
	}
	n := runtime.NumCPU()
	if n < 1 {
		return name
	}
	return fmt.Sprintf("%s  (%d logical)", name, n)
}

// FormatVRAM renders a memory budget. Unmeasured is said out loud; 0.0 is
// never printed as if it were a reading.
func FormatVRAM(gb float64, source string) string {
	if gb <= 0 || source == "" {
		return "unknown (not measured)"
	}
	return fmt.Sprintf("%.1f (%s)", gb, source)
}

func nvidiaSMIMemory(ctx context.Context) float64 {
	if _, err := exec.LookPath("nvidia-smi"); err != nil {
		return 0
	}
	cmd := exec.CommandContext(ctx, "nvidia-smi",
		"--query-gpu=memory.total", "--format=csv,noheader,nounits")
	cmd.WaitDelay = 250 * time.Millisecond
	out, err := cmd.Output()
	if err != nil {
		return 0
	}
	return ParseNvidiaSMIMemory(string(out))
}

// ParseNvidiaSMIMemory reads nvidia-smi memory.total CSV (MiB per line) and
// returns the largest card in GiB. Multiple GPUs take the max so an iGPU
// 128 MiB line cannot hide a 8 GiB dGPU.
func ParseNvidiaSMIMemory(out string) float64 {
	var best float64
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(strings.Split(line, ",")[0])
		if line == "" {
			continue
		}
		mib, err := strconv.ParseFloat(line, 64)
		if err != nil || mib <= 0 {
			continue
		}
		gb := mib * 1024 * 1024 / GB
		if gb > best {
			best = gb
		}
	}
	return best
}

// NormalizeAccel maps a free-form build/log string onto the small set of
// compute APIs that change measurements. GPU needles win over "cpu" so
// "CUDA + CPU offload" does not classify as a CPU run.
func NormalizeAccel(s string) string {
	u := strings.ToLower(s)
	for _, pair := range []struct{ needle, name string }{
		{"cuda", "cuda"}, {"cublas", "cuda"},
		{"metal", "metal"},
		{"vulkan", "vulkan"},
		{"rocm", "rocm"}, {"hip", "rocm"},
		{"sycl", "sycl"},
		{"opencl", "opencl"},
		{"cpu", "cpu"},
	} {
		if hasToken(u, pair.needle) {
			return pair.name
		}
	}
	return ""
}

func hasToken(s, needle string) bool {
	for i := 0; i <= len(s)-len(needle); {
		j := strings.Index(s[i:], needle)
		if j < 0 {
			return false
		}
		j += i
		before, after := byte('_'), byte('_')
		if j > 0 {
			before = s[j-1]
		}
		if j+len(needle) < len(s) {
			after = s[j+len(needle)]
		}
		if !isAlphaNum(before) && !isAlphaNum(after) {
			return true
		}
		i = j + 1
	}
	return false
}

func isAlphaNum(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= '0' && b <= '9'
}

// InferenceDevice reports what Ollama actually computes on.
//
// /api/ps is authoritative and cross-platform: size_vram > 0 means the weights
// are on the GPU. Log parsing is only a fallback -- it is platform-specific and
// the startup line scrolls out of a busy log surprisingly fast.
func inferenceDevice(ctx context.Context, b llm.Backend, model string) string {
	if b != nil {
		if running, err := b.PS(ctx); err == nil {
			for _, m := range running {
				if model != "" && m.Name != model {
					continue
				}
				if m.Size > 0 {
					if m.SizeVRAM == 0 {
						return "CPU"
					}
					return fmt.Sprintf("GPU %d%%", int(100*(float64(m.SizeVRAM)/float64(m.Size))))
				}
			}
		}
	}
	// Log parsing is an Ollama-only fallback; other runtimes do not write this log.
	if b != nil && b.Name() == "ollama" {
		if line := lastLogMatch(`msg="inference compute".*`); line != "" {
			lib := submatch(`library=(\S+)`, line)
			desc := submatch(`description="([^"]*)"`, line)
			if lib != "" || desc != "" {
				return strings.TrimSpace(lib + " / " + desc)
			}
		}
	}
	return "unknown"
}

// InferenceDeviceFor re-checks placement for a specific loaded model.
func InferenceDeviceFor(ctx context.Context, b llm.Backend, model string) string {
	return inferenceDevice(ctx, b, model)
}

func serverLogPath() string {
	if runtime.GOOS == "windows" {
		base := os.Getenv("LOCALAPPDATA")
		if base == "" {
			return ""
		}
		return filepath.Join(base, "Ollama", "server.log")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".ollama", "logs", "server.log")
}

func readLog() string {
	b, err := boundedio.ReadEdges(serverLogPath(), maxServerLogSample)
	if err != nil {
		return ""
	}
	return string(b)
}

func mergeServerLogConfig(cfg map[string]string) {
	text := readLog()
	if text == "" {
		return
	}
	for _, k := range configKeys {
		re := regexp.MustCompile(k + `:([^\s\]]*)`)
		all := re.FindAllStringSubmatch(text, -1)
		if len(all) > 0 {
			v := all[len(all)-1][1]
			cfg[k] = strings.ReplaceAll(v, `\\`, `\`)
		}
	}
}

func lastLogMatch(pattern string) string {
	text := readLog()
	if text == "" {
		return ""
	}
	all := regexp.MustCompile(pattern).FindAllString(text, -1)
	if len(all) == 0 {
		return ""
	}
	return all[len(all)-1]
}

func submatch(pattern, s string) string {
	m := regexp.MustCompile(pattern).FindStringSubmatch(s)
	if len(m) > 1 {
		return m[1]
	}
	return ""
}

// IsDenseAndBig is a hint, never a gate: a dense model above the profile's
// limit will crawl on a bandwidth-bound device. Decode speed tracks ACTIVE
// parameters, so a 30B MoE (~3B active) outruns an 8B dense model.
func IsDenseAndBig(paramSize, family string, p Profile) bool {
	limRaw, ok := p.Hints["dense_param_b_interactive_max"]
	if !ok {
		return false
	}
	var lim float64
	switch n := limRaw.(type) {
	case float64:
		lim = n
	case int:
		lim = float64(n)
	default:
		return false
	}
	if strings.Contains(strings.ToLower(family), "moe") {
		return false
	}
	digits := regexp.MustCompile(`[0-9.]+`).FindString(strings.ToUpper(paramSize))
	v, err := strconv.ParseFloat(digits, 64)
	if err != nil {
		return false
	}
	return v > lim
}
