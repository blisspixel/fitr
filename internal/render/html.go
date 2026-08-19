package render

import (
	"fmt"
	"html/template"
	"io"
	"sort"
	"strings"

	"github.com/blisspixel/fitr/internal/device"
	"github.com/blisspixel/fitr/internal/score"
)

// Artifact is the self-contained shareable result. Opt-in only: results
// carry a hardware fingerprint, so they are never written unless asked.
type Artifact struct {
	FitrVersion   string
	SchemaVersion int
	Model         string
	StartedAt     string
	Level         string
	Repeats       int
	WallSeconds   float64
	Device        device.Fingerprint
	DeviceKey     string
	Profile       string
	Scorecard     score.Scorecard
	Meta          Meta
	Contamination []string
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
	Host          string
	OS            string
	CPU           string
	RAM           string
	GPU           string
	Driver        string
	Runtime       string
	Inference     string
	GPUBackend    string
	Config        []htmlKV
	Needs         []htmlNeed
	Gaps          []htmlNeed
	Decode        string
	Prefill       string
	MDE           string
	RepeatsWarn   bool
	Contamination []string
	StartedAt     string
	Level         string
	Schema        int
	Version       string
	Wall          string
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
Do not rank this result against a different fingerprint. Change the GPU, driver, runtime, or KV dtype and these numbers are void.</div>

<h2>Fingerprint</h2>
<table>
<tr><th class="k">key</th><td>{{.DeviceKey}}</td></tr>
<tr><th class="k">host</th><td>{{.Host}}</td></tr>
<tr><th class="k">os</th><td>{{.OS}}</td></tr>
<tr><th class="k">cpu</th><td>{{.CPU}}</td></tr>
<tr><th class="k">ram</th><td>{{.RAM}}</td></tr>
<tr><th class="k">gpu</th><td>{{.GPU}}</td></tr>
<tr><th class="k">driver</th><td>{{.Driver}}</td></tr>
<tr><th class="k">runtime</th><td>{{.Runtime}}</td></tr>
<tr><th class="k">placement</th><td>{{.Inference}}</td></tr>
{{if .GPUBackend}}<tr><th class="k">gpu backend</th><td>{{.GPUBackend}}</td></tr>{{end}}
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

{{if or .Decode .MDE}}
<h2>Sample</h2>
<table>
{{if .Decode}}<tr><th class="k">decode</th><td>{{.Decode}}</td></tr>{{end}}
{{if .Prefill}}<tr><th class="k">prefill</th><td>{{.Prefill}}</td></tr>{{end}}
{{if .MDE}}<tr><th class="k">resolution</th><td>{{.MDE}}</td></tr>{{end}}
</table>
{{end}}

{{if .RepeatsWarn}}
<p class="warn">Single-sample run - identical configs vary 10-20 pp; re-run with -k 3 before ranking against a close model.</p>
{{end}}

{{if .Contamination}}
<p class="warn">Timings may be contaminated; still resident: {{range $i, $m := .Contamination}}{{if $i}}, {{end}}{{$m}}{{end}}</p>
{{end}}

<footer>
fitr {{.Version}} · schema {{.Schema}} · {{.Level}} · {{.StartedAt}}<br>
Written only because you asked (fitr export / fitr run --html). Never uploaded. Contains a hardware fingerprint.
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
	d := htmlData{
		CSS:           template.CSS(artifactCSS),
		Title:         "fitr · " + a.Model,
		Model:         a.Model,
		UseFor:        a.Scorecard.UseFor,
		Profile:       a.Profile,
		Uncalibrated:  a.Profile == "default",
		DeviceKey:     a.DeviceKey,
		Host:          a.Device.Host,
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
	}
	if a.Device.RAMGb > 0 {
		d.RAM = fmt.Sprintf("%.1f GB", a.Device.RAMGb)
	}
	var size []string
	if a.Meta.ParamSize != "" {
		size = append(size, a.Meta.ParamSize)
	}
	if a.Meta.Quant != "" {
		size = append(size, a.Meta.Quant)
	}
	if a.Meta.Family != "" {
		size = append(size, a.Meta.Family)
	}
	d.SizeLine = strings.Join(size, "  ")
	if a.WallSeconds > 0 {
		d.Wall = fmt.Sprintf("%.0fs", a.WallSeconds)
	}

	keys := make([]string, 0, len(a.Device.Config))
	for k := range a.Device.Config {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := a.Device.Config[k]
		if v == "" {
			v = "(unset)"
		}
		d.Config = append(d.Config, htmlKV{K: k, V: v})
	}

	for _, k := range score.SortedNeeds(a.Scorecard.Needs) {
		v, ok := a.Scorecard.Needs[k]
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
			d.Gaps = append(d.Gaps, row)
		default:
			d.Needs = append(d.Needs, row)
		}
	}

	if a.Meta.DecodeN > 0 {
		d.Decode = stat(a.Meta.DecodeMean, a.Meta.DecodeSD, a.Meta.DecodeN,
			a.Meta.DecodeMin, a.Meta.DecodeMax, g)
	}
	if a.Meta.PrefillN > 0 {
		d.Prefill = stat(a.Meta.PrefillMean, a.Meta.PrefillSD, a.Meta.PrefillN, 0, 0, g)
	}
	if a.Meta.MDEpp > 0 {
		d.MDE = fmt.Sprintf("%d binary trials - min detectable effect ~%.0fpp - separates broken from working, not good from slightly better",
			a.Meta.Trials, a.Meta.MDEpp)
	}
	return d
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
