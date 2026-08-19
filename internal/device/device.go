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
	"encoding/json"
	"fmt"
	"os"
	"path"
	"regexp"
	"runtime"
	"strconv"
	"strings"

	"github.com/blisspixel/fitr/internal/llm"
)

//go:embed all:profiles
var profilesFS embed.FS

const GB = 1024 * 1024 * 1024

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

func LoadProfiles() ([]Profile, error) {
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
			continue
		}
		var p Profile
		if json.Unmarshal(b, &p) == nil {
			out = append(out, p)
		}
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
	return byName["default"], nil
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
	Runtime         string            `json:"ollama"`
	InferenceDevice string            `json:"inference_device"`
	Config          map[string]string `json:"config"`
}

// Key is what decides comparability. Two results may only be ranked against
// each other if this matches exactly.
func (f Fingerprint) Key() string {
	c := f.Config
	return strings.Join([]string{
		f.Host, f.GPU, f.GPUDriver, f.Runtime,
		c["OLLAMA_FLASH_ATTENTION"], c["OLLAMA_KV_CACHE_TYPE"],
	}, "|")
}

var configKeys = []string{
	"OLLAMA_MODELS", "OLLAMA_IGPU_ENABLE", "OLLAMA_FLASH_ATTENTION",
	"OLLAMA_KV_CACHE_TYPE", "OLLAMA_MAX_LOADED_MODELS", "OLLAMA_NUM_PARALLEL",
	"OLLAMA_CONTEXT_LENGTH", "LLAMA_ARG_FIT",
}

func Detect(ctx context.Context, b llm.Backend) Fingerprint {
	host, _ := os.Hostname()
	gpu, drv, date := gpuInfo()
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
		version = b.Version(ctx)
	}
	return Fingerprint{
		Host: host, OS: runtime.GOOS, CPU: cpuName(), RAMGb: ramGB(),
		GPU: gpu, GPUDriver: drv, GPUDriverDate: date,
		Runtime: version, InferenceDevice: inferenceDevice(ctx, b, ""),
		Config: cfg,
	}
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
					return fmt.Sprintf("GPU %d%%", int(100*m.SizeVRAM/m.Size))
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
		return path.Join(os.Getenv("LOCALAPPDATA"), "Ollama", "server.log")
	}
	home, _ := os.UserHomeDir()
	return path.Join(home, ".ollama", "logs", "server.log")
}

func readLog() string {
	b, err := os.ReadFile(serverLogPath())
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
