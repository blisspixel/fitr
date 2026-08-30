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
	// VRAMGb is a measured or declared GPU-memory capacity input. VRAMSource
	// defines its semantics: a dedicated-memory total, an addressable unified
	// pool, and an operator budget are not interchangeable. Zero with an empty
	// source means it was not measured; callers must SKIP rather than invent a
	// card-from-name number.
	VRAMGb     float64           `json:"vram_gb,omitempty"`
	VRAMSource string            `json:"vram_source,omitempty"`
	Config     map[string]string `json:"config"`
}

// vendorForAccel maps a compute API onto the vendor whose name must appear on
// the card serving it. Only APIs with a single possible vendor are listed:
// vulkan and opencl run on anyone's hardware and prove nothing about a name.
var vendorForAccel = map[string]string{
	"cuda":  "nvidia",
	"rocm":  "amd",
	"metal": "apple",
}

// IdentityConflicts reports parts of the fingerprint that were derived
// independently and disagree about what this machine is.
//
// Device identity is the worst failure class fitr has, because it is silent.
// A wrong GPU name produces no error: it produces a run that is sealed to a
// device that does not exist, and evidence that stops comparing the moment the
// misdetection changes. 0.9.6 named a headset's virtual display while sizing
// VRAM from a GeForce card in the same fingerprint, and nothing objected.
//
// Each check fires only on a genuine contradiction between two sources, never
// on a value that is merely missing: unmeasured is a legitimate state, and
// this must not turn one into a scary warning.
func (f Fingerprint) IdentityConflicts() []string {
	var out []string
	name := strings.ToLower(f.GPU)

	// A budget read from a vendor tool describes that vendor's card. If the
	// name came from somewhere else and names a different device, the
	// fingerprint is a composite of two machines.
	if f.VRAMSource == "nvidia-smi" && f.VRAMGb > 0 && name != "" &&
		!strings.Contains(name, "nvidia") && !strings.Contains(name, "geforce") &&
		!strings.Contains(name, "quadro") && !strings.Contains(name, "tesla") {
		out = append(out, fmt.Sprintf(
			"memory was measured with nvidia-smi but the GPU is named %q; "+
				"the name and the budget describe different devices", f.GPU))
	}

	// The compute API the server reports is a second, independent opinion
	// about the vendor.
	if vendor, single := vendorForAccel[strings.ToLower(f.GPUBackend)]; single && name != "" {
		if !strings.Contains(name, vendor) && !accelNameException(vendor, name) {
			out = append(out, fmt.Sprintf(
				"the runtime is using %s but the GPU is named %q; "+
					"one of the two is not this machine's inference device", f.GPUBackend, f.GPU))
		}
	}

	// A sized device with no name cannot be told apart from another sized
	// device with no name, and Key would join them.
	if f.VRAMGb > 0 && f.VRAMSource != "" && strings.TrimSpace(f.GPU) == "" {
		out = append(out, "GPU memory was measured but the device has no name; "+
			"results cannot be told apart from another unnamed device")
	}
	return out
}

// accelNameException covers vendors whose cards are not spelled with the
// vendor's own name.
func accelNameException(vendor, name string) bool {
	switch vendor {
	case "nvidia":
		return strings.Contains(name, "geforce") || strings.Contains(name, "quadro") ||
			strings.Contains(name, "tesla") || strings.Contains(name, "rtx") ||
			strings.Contains(name, "gtx")
	case "amd":
		return strings.Contains(name, "radeon") || strings.Contains(name, "instinct")
	case "apple":
		return strings.Contains(name, "m1") || strings.Contains(name, "m2") ||
			strings.Contains(name, "m3") || strings.Contains(name, "m4") ||
			strings.Contains(name, "m5")
	}
	return false
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
	add("os", f.OS, o.OS)
	add("cpu", f.CPU, o.CPU)
	add("ram_gb", strconv.FormatFloat(f.RAMGb, 'g', -1, 64), strconv.FormatFloat(o.RAMGb, 'g', -1, 64))
	add("gpu", f.GPU, o.GPU)
	add("gpu_driver", f.GPUDriver, o.GPUDriver)
	add("gpu_driver_date", f.GPUDriverDate, o.GPUDriverDate)
	add("gpu_backend", f.GPUBackend, o.GPUBackend)
	add("vram_gb", strconv.FormatFloat(f.VRAMGb, 'g', -1, 64), strconv.FormatFloat(o.VRAMGb, 'g', -1, 64))
	add("vram_source", f.VRAMSource, o.VRAMSource)
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
	go func() { defer wg.Done(); gpu, drv, date = cachedGPUInfo(probeCtx) }()
	go func() { defer wg.Done(); cpu = cachedCPUName(probeCtx) }()
	go func() { defer wg.Done(); ram = cachedRAMGB(probeCtx) }()
	go func() { defer wg.Done(); vram, vsrc = cachedVRAMInfo(probeCtx) }()

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
	// An empty CPU name is fatal further on: fingerprint v2 refuses to seal
	// without it, so the whole run dies with "fingerprint is missing CPU". The
	// probe is a process round-trip sharing one budget with three others, and
	// a loaded machine is exactly when someone measures -- this was first seen
	// on a busy CI runner. Retry once on its own budget before a slow probe
	// gets to cancel a measurement.
	if strings.TrimSpace(cpu) == "" {
		retryCtx, cancelRetry := context.WithTimeout(ctx, 5*time.Second)
		cpu = cpuName(retryCtx)
		cancelRetry()
	}
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
// model budget marks everything incompatible. Discrete NVIDIA remains
// nvidia-smi; GB10 and Thor use the measured Linux system pool only when the
// dedicated-memory probe has no answer.
func preferUnifiedMemory(gpu string, ram, vram float64, src string) (float64, string) {
	return preferUnifiedMemoryForOS(runtime.GOOS, gpu, ram, vram, src)
}

func preferUnifiedMemoryForOS(goos, gpu string, ram, vram float64, src string) (float64, string) {
	if ram <= 0 {
		return vram, src
	}
	// GB10 and Thor are NVIDIA unified-memory SoCs. Their nvidia-smi
	// dedicated-memory fields report N/A, so Linux MemTotal is the measured
	// addressable pool. Never replace a real vendor or DRM reading, and never
	// apply this Linux source label on another operating system.
	if nvidiaUnifiedMemoryGPU(gpu) {
		if goos == "linux" && vram > 0 && src == "nvidia-smi" {
			return vram, NVIDIAUnifiedProbeSource
		}
		if goos == "linux" && vram <= 0 && strings.TrimSpace(src) == "" {
			return ram, NVIDIAUnifiedMemorySource
		}
		return vram, src
	}
	if !unifiedMemoryGPU(gpu) {
		return vram, src
	}
	// Sources that already know what the GPU may use. Apple Silicon is on this
	// list because its budget is now computed against the wired-memory limit
	// rather than installed RAM, so second-guessing it here would undo that.
	switch src {
	case "nvidia-smi", "drm sysfs",
		NVIDIAUnifiedMemorySource, NVIDIAUnifiedProbeSource,
		AppleWiredLimitSource, AppleAssumedShareSource, AppleLegacyRAMSource:
		return vram, src
	}
	if vram > 0 && ram < vram*2 {
		return vram, src
	}
	return ram, "unified memory (system RAM)"
}

func nvidiaUnifiedMemoryGPU(name string) bool {
	u := strings.ToLower(name)
	return strings.Contains(u, "gb10") || strings.Contains(u, "thor")
}

// IsNVIDIAUnifiedMemoryGPU reports whether a device name identifies one of
// NVIDIA's shared-memory SoCs. Capacity-source provenance is deliberately not
// part of this decision: a future driver may return a nonzero memory field,
// but that does not turn the physical pool into dedicated VRAM.
func IsNVIDIAUnifiedMemoryGPU(name string) bool {
	return nvidiaUnifiedMemoryGPU(name)
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

func appleGPUName(displayInfo, cpuBrand string) string {
	if m := regexp.MustCompile(`Chipset Model:\s*(.+)`).FindStringSubmatch(displayInfo); len(m) > 1 {
		return strings.TrimSpace(m[1])
	}
	cpuBrand = strings.TrimSpace(cpuBrand)
	if strings.Contains(strings.ToLower(cpuBrand), "apple") {
		return cpuBrand
	}
	return "unknown"
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

// FormatVRAM renders a memory-capacity input with its source. Unmeasured is
// said out loud; 0.0 is never printed as if it were a reading.
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

// ParseNvidiaSMIName reads nvidia-smi "name,memory.total" CSV and returns the
// name of the largest card, matching the card ParseNvidiaSMIMemory reports.
// The two must agree: a fingerprint that names one GPU and sizes another is
// not a device receipt.
func ParseNvidiaSMIName(out string) string {
	var best float64
	name := ""
	for _, line := range strings.Split(out, "\n") {
		parts := strings.Split(line, ",")
		if len(parts) < 2 {
			continue
		}
		n := strings.TrimSpace(parts[0])
		mib, err := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
		if err != nil || mib <= 0 || n == "" {
			continue
		}
		if mib > best {
			best, name = mib, n
		}
	}
	return name
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
	if placement := inferenceDeviceFromRuntime(ctx, b, model); placement != "" {
		return placement
	}
	if placement := ollamaInferenceDeviceFromLog(b); placement != "" {
		return placement
	}
	return "unknown"
}

func inferenceDeviceFromRuntime(ctx context.Context, b llm.Backend, model string) string {
	if b == nil {
		return ""
	}
	running, err := b.PS(ctx)
	if err != nil {
		return ""
	}
	for _, candidate := range running {
		if model != "" && candidate.Name != model {
			continue
		}
		if candidate.Size <= 0 {
			continue
		}
		if candidate.SizeVRAM == 0 {
			return "CPU"
		}
		return fmt.Sprintf("GPU %d%%", int(100*(float64(candidate.SizeVRAM)/float64(candidate.Size))))
	}
	return ""
}

// Log parsing is an Ollama-only fallback; other runtimes do not write this log.
func ollamaInferenceDeviceFromLog(b llm.Backend) string {
	if b == nil || b.Name() != "ollama" {
		return ""
	}
	line := lastLogMatch(`msg="inference compute".*`)
	if line == "" {
		return ""
	}
	lib := submatch(`library=(\S+)`, line)
	desc := submatch(`description="([^"]*)"`, line)
	if lib == "" && desc == "" {
		return ""
	}
	return strings.TrimSpace(lib + " / " + desc)
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

// DenseSizeHintExceeded checks a profile-authored interactive-size hint. It is
// not a bottleneck diagnosis or a throughput prediction. MoE total parameters
// do not carry the same interpretation, so this dense-only hint excludes them.
func DenseSizeHintExceeded(paramSize, family string, p Profile) bool {
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

// ---------------------------------------------------------------- host probes
// The host facts below are process round-trips on Windows, and Detect runs on
// the path of nearly every command -- some commands call it several times. The
// CPU, RAM, GPU and memory of the machine cannot change while one fitr process
// runs, so probing them repeatedly buys nothing and costs a subprocess storm.
//
// That cost is not theoretical. An empty CPU name is fatal downstream:
// fingerprint v2 refuses to seal without it and the run dies. On a loaded
// machine a probe sharing a five-second budget with three others can return
// nothing at all, which is how a run failed with "fingerprint is missing CPU"
// on a busy CI runner. Probing once per process removes both the latency and
// the chance of a slow probe cancelling a measurement.
//
// Only successful readings are cached. A probe that failed under load must be
// allowed to succeed on the next attempt rather than pinning an empty value
// for the life of the process.
var hostProbes struct {
	sync.Mutex
	gpu, gpuDriver, gpuDate string
	gpuOK                   bool
	cpu                     string
	ram                     float64
	vram                    float64
	vramSource              string
	vramOK                  bool
}

func cachedGPUInfo(ctx context.Context) (name, driver, date string) {
	hostProbes.Lock()
	defer hostProbes.Unlock()
	if hostProbes.gpuOK {
		return hostProbes.gpu, hostProbes.gpuDriver, hostProbes.gpuDate
	}
	name, driver, date = gpuInfo(ctx)
	if name != "" {
		hostProbes.gpu, hostProbes.gpuDriver, hostProbes.gpuDate = name, driver, date
		hostProbes.gpuOK = true
	}
	return name, driver, date
}

func cachedCPUName(ctx context.Context) string {
	hostProbes.Lock()
	defer hostProbes.Unlock()
	if hostProbes.cpu != "" {
		return hostProbes.cpu
	}
	hostProbes.cpu = cpuName(ctx)
	return hostProbes.cpu
}

func cachedRAMGB(ctx context.Context) float64 {
	hostProbes.Lock()
	defer hostProbes.Unlock()
	if hostProbes.ram > 0 {
		return hostProbes.ram
	}
	hostProbes.ram = ramGB(ctx)
	return hostProbes.ram
}

func cachedVRAMInfo(ctx context.Context) (float64, string) {
	hostProbes.Lock()
	defer hostProbes.Unlock()
	if hostProbes.vramOK {
		return hostProbes.vram, hostProbes.vramSource
	}
	gb, source := vramInfo(ctx)
	// An unmeasured budget is a legitimate reading on a CPU-only box, so it is
	// cached too: the distinguishing fact is whether the probe ran, not
	// whether it found a GPU.
	hostProbes.vram, hostProbes.vramSource, hostProbes.vramOK = gb, source, true
	return gb, source
}

// resetHostProbes clears the per-process cache. Tests only.
func resetHostProbes() {
	hostProbes.Lock()
	defer hostProbes.Unlock()
	hostProbes.gpu, hostProbes.gpuDriver, hostProbes.gpuDate, hostProbes.gpuOK = "", "", "", false
	hostProbes.cpu = ""
	hostProbes.ram = 0
	hostProbes.vram, hostProbes.vramSource, hostProbes.vramOK = 0, "", false
}

// AvailableVRAM reports GPU memory not currently committed to something else,
// in GiB, and whether the reading was obtained at all.
//
// This is deliberately NOT part of the fingerprint. It changes minute to
// minute as a compositor, a browser or somebody's notebook takes and releases
// memory, and putting a volatile number in the comparability key would put
// every run in its own block and make nothing comparable to anything.
//
// It exists because a fit verdict computed against total memory answers a
// question nobody has. Nobody uses a machine only for inference. The useful
// question is what will run alongside the work already on the card, and a box
// reporting 24 GB total with 0.7 GB free will not load a 17 GB model no matter
// what the arithmetic says.
// appleWiredLimitFraction is a conservative assumed share of system RAM for
// Apple Silicon when iogpu.wired_limit_mb is unset, which is the default state.
//
// The exact figure is a kernel policy, not a published constant, and it varies
// with installed RAM. 0.75 is the widely-reported value for the large-memory
// machines this matters on. It is applied as an ASSUMPTION and labelled as one,
// because the alternative -- reporting all installed RAM as GPU-available --
// can certify a model that the active kernel policy will not load.
const appleWiredLimitFraction = 0.75

// Memory-source labels for Apple Silicon. They are constants because a trust
// decision keys on them by exact string: changing one silently turned every
// macOS model unproven once already.
const (
	// NVIDIAUnifiedMemorySource is the Linux addressable-memory pool on
	// NVIDIA GB10 and Thor SoCs, whose nvidia-smi dedicated-memory fields are
	// unavailable. It is capacity, not a live free-memory reading.
	NVIDIAUnifiedMemorySource = "unified memory (/proc/meminfo MemTotal)"
	// NVIDIAUnifiedProbeSource preserves a future nonzero nvidia-smi capacity
	// reading without presenting the physical shared pool as dedicated VRAM.
	NVIDIAUnifiedProbeSource = "unified memory (nvidia-smi capacity)"
	// AppleWiredLimitSource is an explicit kernel setting the user chose.
	AppleWiredLimitSource = "iogpu.wired_limit_mb"
	// AppleAssumedShareSource is derived. It says "assumed" because every place
	// the budget is printed shows its source, and a derived number must not
	// read like a measured one.
	AppleAssumedShareSource = "unified memory (assumed GPU share of system RAM)"
	// AppleLegacyRAMSource is what fitr wrote before the budget was corrected.
	// Retained so saved results still read as measured.
	AppleLegacyRAMSource = "unified memory (system RAM)"
)

func AvailableVRAM(ctx context.Context) (float64, bool) {
	if _, err := exec.LookPath("nvidia-smi"); err != nil {
		// Not every GPU ships an nvidia-smi. Free memory is the number that
		// decides whether a model loads right now, and answering it only for
		// one vendor makes the caveat a vendor feature.
		return availableVRAMFallback(ctx)
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, "nvidia-smi",
		"--query-gpu=memory.free", "--format=csv,noheader,nounits")
	cmd.WaitDelay = 250 * time.Millisecond
	out, err := cmd.Output()
	if err != nil {
		return 0, false
	}
	gb := ParseNvidiaSMIMemory(string(out))
	return gb, gb > 0
}
