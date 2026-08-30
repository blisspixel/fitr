package render

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"html/template"
	"io"
	"regexp"
	"strconv"
	"strings"

	"github.com/blisspixel/fitr/internal/device"
	"github.com/blisspixel/fitr/internal/score"
)

// Artifact is the self-contained shareable result. Opt-in only: it carries an
// allowlisted device view and opaque comparison ID, so it is never written
// unless asked.
type Artifact struct {
	FitrVersion   string
	SchemaVersion int
	Model         string
	StartedAt     string
	Level         string
	Repeats       int
	WallSeconds   float64
	Device        ShareDevice
	DeviceKey     string
	Profile       string
	Scorecard     score.Scorecard
	Meta          Meta
	NextCommand   string
	Contamination []string
}

// ShareDevice is an allowlisted hardware view. It deliberately cannot carry a
// hostname, endpoint, local path, or arbitrary environment configuration.
type ShareDevice struct {
	OS              string            `json:"os,omitempty"`
	CPU             string            `json:"cpu,omitempty"`
	GPU             string            `json:"gpu,omitempty"`
	GPUDriver       string            `json:"gpu_driver,omitempty"`
	GPUDriverDate   string            `json:"gpu_driver_date,omitempty"`
	Runtime         string            `json:"runtime,omitempty"`
	InferenceDevice string            `json:"inference_device,omitempty"`
	GPUBackend      string            `json:"gpu_backend,omitempty"`
	RAMGb           float64           `json:"ram_gb,omitempty"`
	Config          map[string]string `json:"config,omitempty"`
}

func NewShareDevice(fp device.Fingerprint) ShareDevice {
	shared := ShareDevice{
		OS: safeShareText(fp.OS), CPU: safeShareText(fp.CPU), RAMGb: fp.RAMGb,
		GPU: safeShareText(fp.GPU), GPUDriver: safeShareText(fp.GPUDriver),
		GPUDriverDate: safeShareText(fp.GPUDriverDate), Runtime: safeShareText(fp.Runtime),
		InferenceDevice: safeShareText(fp.InferenceDevice), GPUBackend: safeShareText(fp.GPUBackend),
		Config: map[string]string{},
	}
	for _, key := range []string{
		"OLLAMA_CONTEXT_LENGTH", "OLLAMA_FLASH_ATTENTION", "OLLAMA_KV_CACHE_TYPE", "OLLAMA_NUM_PARALLEL",
	} {
		if value := safeShareConfig(key, fp.Config[key]); value != "" {
			shared.Config[key] = value
		}
	}
	return shared
}

var (
	shareURIPattern         = regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]{1,31}://`)
	shareWindowsPathPattern = regexp.MustCompile(`(?i)(?:^|[\s="'])[A-Z]:[\\/]`)
	shareUnixPathPattern    = regexp.MustCompile(`(?:^|[\s="'])/(?:[^/\s]+/|Users/|home/|tmp/)`)
	shareNetworkPathPattern = regexp.MustCompile(`(?:^|[\s="'])(?:\\\\|//)[^\\/\s]+[\\/]`)
	shareTokenPattern       = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,32}$`)
	shareIntegerPattern     = regexp.MustCompile(`^[0-9]{1,10}$`)
)

func safeShareText(value string) string {
	value = SingleLine(value)
	if shareURIPattern.MatchString(value) || shareWindowsPathPattern.MatchString(value) ||
		shareUnixPathPattern.MatchString(value) || shareNetworkPathPattern.MatchString(value) ||
		strings.HasPrefix(value, "./") ||
		strings.HasPrefix(value, "../") || strings.HasPrefix(value, `.\`) || strings.HasPrefix(value, `..\`) {
		return ""
	}
	runes := []rune(value)
	if len(runes) > 160 {
		value = string(runes[:160])
	}
	return value
}

func safeShareConfig(key, value string) string {
	value = strings.TrimSpace(value)
	switch key {
	case "OLLAMA_CONTEXT_LENGTH", "OLLAMA_NUM_PARALLEL":
		if shareIntegerPattern.MatchString(value) {
			return value
		}
	case "OLLAMA_FLASH_ATTENTION":
		switch strings.ToLower(value) {
		case "0", "1", "false", "true":
			return strings.ToLower(value)
		}
	case "OLLAMA_KV_CACHE_TYPE":
		if shareTokenPattern.MatchString(value) {
			return value
		}
	}
	return ""
}

// NewShareMeta strips local-only identity duplicates from the generic render
// metadata before it becomes part of a shareable artifact. Device identity is
// carried only by the separately allowlisted ShareDevice.
func NewShareMeta(meta Meta) Meta {
	meta.ParamSize = safeShareText(meta.ParamSize)
	meta.Quant = safeShareText(meta.Quant)
	meta.Family = safeShareText(meta.Family)
	meta.Profile = safeShareText(meta.Profile)
	meta.GPU, meta.Driver, meta.Device, meta.SavedPath = "", "", "", ""
	return meta
}

type htmlNeed struct {
	Label, State, Why, Class string
}

type htmlKV struct{ K, V string }

type htmlData struct {
	CSS           template.CSS
	Title         string
	Model         string
	SizeLine      string
	UseFor        string
	Profile       string
	Uncalibrated  bool
	DeviceKey     string
	OS            string
	CPU           string
	RAM           string
	GPU           string
	Driver        string
	Runtime       string
	Inference     string
	GPUBackend    string
	NumCtx        string
	Config        []htmlKV
	Needs         []htmlNeed
	Gaps          []htmlNeed
	EvidenceGaps  []htmlKV
	Decode        string
	Prefill       string
	TTFT          string
	Resident      string
	RepeatsWarn   bool
	Contamination []string
	StartedAt     string
	Level         string
	Schema        int
	Version       string
	Wall          string
	Next          string
}

const artifactCSS = `:root{--bg:#0f1419;--fg:#e6edf3;--muted:#8b949e;--pass:#3fb950;--fail:#f85149;--skip:#8b949e;--blkd:#d29922;--line:#30363d;--card:#161b22}
*{box-sizing:border-box}
body{margin:0;background:var(--bg);color:var(--fg);font:14px/1.45 ui-monospace,SFMono-Regular,Menlo,Consolas,monospace}
main{max-width:54rem;margin:0 auto;padding:2rem 1.25rem 4rem}
h1{font-size:1.15rem;font-weight:600;margin:0 0 .35rem}
.sub{color:var(--muted);margin:0 0 1rem}
.banner{border:1px solid var(--line);background:var(--card);padding:.8rem 1rem;margin:0 0 1.25rem;color:var(--muted)}
.banner strong{color:var(--fg);font-weight:600}
h2{font-size:.75rem;letter-spacing:.08em;text-transform:uppercase;color:var(--muted);margin:1.6rem 0 .5rem;font-weight:600}
table{width:100%;border-collapse:collapse}
th,td{text-align:left;vertical-align:top;padding:.4rem .5rem .4rem 0;border-bottom:1px solid var(--line)}
th{color:var(--muted);font-weight:500}
.k{color:var(--muted);width:11rem;white-space:nowrap}
.pass{color:var(--pass)} .fail{color:var(--fail)} .skip{color:var(--skip)} .blkd{color:var(--blkd)}
.use{color:#d2a8ff}
.warn{color:var(--blkd)}
footer{margin-top:2rem;color:var(--muted);font-size:.8rem}
@media print{body{background:#fff;color:#111} .banner,.pass,.fail,.skip,.blkd,.use,.warn,footer,.sub,th,.k{color:inherit}}`

var artifactTmpl = template.Must(template.New("artifact").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}}</title>
<style>{{.CSS}}</style>
</head>
<body>
<main>
<h1>{{.Model}}</h1>
<p class="sub">{{.SizeLine}} · profile {{.Profile}}{{if .Uncalibrated}} (uncalibrated){{end}}{{if .Wall}} · {{.Wall}}{{end}}</p>
<p class="use">{{.UseFor}}</p>

<div class="banner"><strong>A number without its device is meaningless.</strong>
Do not rank this result against a different device/config ID. Change the GPU, driver, runtime, KV dtype, or request context and these numbers are void.</div>

<h2>Device and configuration</h2>
<table>
<tr><th class="k">opaque ID</th><td>{{.DeviceKey}}</td></tr>
<tr><th class="k">os</th><td>{{.OS}}</td></tr>
<tr><th class="k">cpu</th><td>{{.CPU}}</td></tr>
<tr><th class="k">ram</th><td>{{.RAM}}</td></tr>
<tr><th class="k">gpu</th><td>{{.GPU}}</td></tr>
<tr><th class="k">driver</th><td>{{.Driver}}</td></tr>
<tr><th class="k">runtime</th><td>{{.Runtime}}</td></tr>
<tr><th class="k">placement</th><td>{{.Inference}}</td></tr>
{{if .GPUBackend}}<tr><th class="k">gpu backend</th><td>{{.GPUBackend}}</td></tr>{{end}}
{{if .NumCtx}}<tr><th class="k">num_ctx</th><td>{{.NumCtx}}</td></tr>{{end}}
{{range .Config}}<tr><th class="k">{{.K}}</th><td>{{.V}}</td></tr>{{end}}
</table>

<h2>Needs</h2>
<table>
{{range .Needs}}
<tr><th class="{{.Class}}">{{.State}}</th><td>{{.Label}} - {{.Why}}</td></tr>
{{end}}
</table>

{{if .Gaps}}
<h2>Not measured</h2>
<table>
{{range .Gaps}}
<tr><th class="{{.Class}}">{{.State}}</th><td>{{.Label}} - {{.Why}}</td></tr>
{{end}}
</table>
{{end}}

{{if .EvidenceGaps}}
<h2>Evidence limits</h2>
<table>
{{range .EvidenceGaps}}
<tr><th class="k">{{.K}}</th><td>{{.V}}</td></tr>
{{end}}
</table>
{{end}}

{{if or .Decode .Prefill .TTFT}}
<h2>Performance</h2>
<table>
{{if .Decode}}<tr><th class="k">decode</th><td>{{.Decode}}</td></tr>{{end}}
{{if .Prefill}}<tr><th class="k">prefill</th><td>{{.Prefill}}</td></tr>{{end}}
{{if .TTFT}}<tr><th class="k">TTFT</th><td>{{.TTFT}}</td></tr>{{end}}
</table>
{{end}}

{{if .Resident}}
<h2>Capacity</h2>
<table>
<tr><th class="k">resident</th><td>{{.Resident}}</td></tr>
</table>
{{end}}

{{if .RepeatsWarn}}
<p class="warn">Single-sample run - one trial cannot establish a stable rate; re-run with -k 3 before comparing a close result.</p>
{{end}}

{{if .Contamination}}
<p class="warn">Timings may be contaminated; still resident: {{range $i, $m := .Contamination}}{{if $i}}, {{end}}{{$m}}{{end}}</p>
{{end}}

{{if .Next}}
<p class="sub">next <span class="use">{{.Next}}</span></p>
{{end}}
<footer>
fitr {{.Version}} · schema {{.Schema}} · {{.Level}} · {{.StartedAt}}<br>
Written only because you asked (fitr export / fitr run --html). Never uploaded. Contains an opaque device ID and allowlisted comparison configuration.
</footer>
</main>
</body>
</html>
`))

// WriteHTML writes a self-contained scorecard. Every string from the result
// is HTML-escaped by the template; raw model output is not accepted on
// Artifact and so cannot leak into the page.
func WriteHTML(w io.Writer, a Artifact) error {
	return artifactTmpl.Execute(w, htmlDataFrom(a))
}

func htmlDataFrom(a Artifact) htmlData {
	g := glyphs{" | ", "-", "+/-", "..."}
	a.Meta = resolvedMeta(a.Meta)
	d := baseHTMLData(a)
	d.NumCtx = htmlContext(a.Meta)
	d.RAM = htmlRAM(a.Device.RAMGb)
	d.SizeLine = htmlSizeLine(a.Meta)
	if a.WallSeconds > 0 {
		d.Wall = fmt.Sprintf("%.0fs", a.WallSeconds)
	}
	d.Config = htmlConfig(a.Device)
	d.Needs, d.Gaps = htmlNeeds(a.Scorecard)
	if a.Meta.Analysis != nil {
		for _, gap := range a.Meta.Analysis.Gaps {
			d.EvidenceGaps = append(d.EvidenceGaps, htmlKV{K: string(gap.Code), V: gap.Message})
		}
	}
	d.Decode, d.Prefill, d.TTFT = htmlPerformance(a.Meta, g)
	d.Resident = htmlCapacity(a.Meta)
	return d
}

func baseHTMLData(a Artifact) htmlData {
	return htmlData{
		CSS:           template.CSS(artifactCSS),
		Title:         "fitr · " + a.Model,
		Model:         a.Model,
		UseFor:        a.Scorecard.UseFor,
		Profile:       a.Profile,
		Uncalibrated:  a.Profile == "default",
		DeviceKey:     a.DeviceKey,
		OS:            a.Device.OS,
		CPU:           a.Device.CPU,
		GPU:           a.Device.GPU,
		Driver:        strings.TrimSpace(a.Device.GPUDriver + " " + a.Device.GPUDriverDate),
		Runtime:       a.Device.Runtime,
		Inference:     a.Device.InferenceDevice,
		GPUBackend:    a.Device.GPUBackend,
		Contamination: a.Contamination,
		StartedAt:     a.StartedAt,
		Level:         a.Level,
		Schema:        a.SchemaVersion,
		Version:       a.FitrVersion,
		RepeatsWarn:   a.Meta.Repeats > 0 && a.Meta.Repeats < 3,
		Next:          a.NextCommand,
	}
}

func htmlContext(meta Meta) string {
	if meta.NumCtx <= 0 {
		return ""
	}
	switch {
	case meta.EffectiveCtx > 0 && meta.EffectiveCtx != meta.NumCtx:
		return fmt.Sprintf("%d requested, %d effective (%s)", meta.NumCtx,
			meta.EffectiveCtx, meta.ContextState)
	case meta.EffectiveCtx > 0:
		return fmt.Sprintf("%d effective (%s)", meta.EffectiveCtx, meta.ContextState)
	case meta.ContextState != "":
		return fmt.Sprintf("%d requested, effective %s", meta.NumCtx, meta.ContextState)
	default:
		return strconv.Itoa(meta.NumCtx)
	}
}

func htmlRAM(ramGB float64) string {
	if ramGB <= 0 {
		return ""
	}
	return fmt.Sprintf("%.1f GB", ramGB)
}

func htmlSizeLine(meta Meta) string {
	var size []string
	if meta.ParamSize != "" {
		size = append(size, meta.ParamSize)
	}
	if meta.Quant != "" {
		size = append(size, meta.Quant)
	}
	if meta.Family != "" {
		size = append(size, meta.Family)
	}
	return strings.Join(size, "  ")
}

func htmlConfig(fingerprint ShareDevice) []htmlKV {
	var config []htmlKV
	for _, k := range []string{
		"OLLAMA_CONTEXT_LENGTH", "OLLAMA_FLASH_ATTENTION", "OLLAMA_KV_CACHE_TYPE", "OLLAMA_NUM_PARALLEL",
	} {
		v := fingerprint.Config[k]
		if v != "" {
			config = append(config, htmlKV{K: k, V: v})
		}
	}
	return config
}

func htmlNeeds(card score.Scorecard) (needs, gaps []htmlNeed) {
	for _, k := range score.SortedNeeds(card.Needs) {
		v, ok := card.Needs[k]
		if !ok {
			continue
		}
		row := htmlNeed{
			Label: score.NeedLabel[k],
			State: string(v.State),
			Why:   v.Why,
			Class: htmlStateClass(v.State),
		}
		if row.Label == "" {
			row.Label = k
		}
		switch v.State {
		case score.Skip, score.NA:
			gaps = append(gaps, row)
		default:
			needs = append(needs, row)
		}
	}
	return needs, gaps
}

func htmlPerformance(meta Meta, g glyphs) (decode, prefill, ttft string) {
	if meta.DecodeN > 0 {
		decode = stat(meta.DecodeMean, meta.DecodeSD, meta.DecodeN, g) + " tok/s"
		if meta.DecodeMin > 0 || meta.DecodeMax > 0 {
			decode += fmt.Sprintf("; min %.2f, max %.2f", meta.DecodeMin, meta.DecodeMax)
		}
	}
	if meta.PrefillN > 0 {
		prefill = stat(meta.PrefillMean, meta.PrefillSD, meta.PrefillN, g) + " tok/s"
	}
	if meta.TTFTN > 0 {
		ttft = stat(meta.TTFTMean, meta.TTFTSD, meta.TTFTN, g) + " s"
	}
	return decode, prefill, ttft
}

func htmlCapacity(meta Meta) string {
	if meta.ResidentGB <= 0 {
		return ""
	}
	return fmt.Sprintf("%.2f GB after requested %s load probe", meta.ResidentGB,
		residentContextLabel(meta.ResidentContext))
}

func ShareFingerprintID(deviceKey string) string {
	if strings.TrimSpace(deviceKey) == "" {
		return "unavailable"
	}
	sum := sha256.Sum256([]byte("fitr.share.device.v1\x00" + deviceKey))
	return "device-" + hex.EncodeToString(sum[:8])
}

func htmlStateClass(s score.State) string {
	switch s {
	case score.Pass:
		return "pass"
	case score.Fail:
		return "fail"
	case score.Blocked:
		return "blkd"
	default:
		return "skip"
	}
}
