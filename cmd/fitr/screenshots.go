package main

// `fitr screenshots [dir]` regenerates the README's terminal images from MOCK
// data pushed through the REAL display code paths, so a screenshot cannot
// drift from what the tool actually prints. Dev-only; not listed in usage.
// The mock numbers are labeled as such by the model names they carry.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/blisspixel/fitr/internal/advise"
	"github.com/blisspixel/fitr/internal/device"
	"github.com/blisspixel/fitr/internal/eval"
	"github.com/blisspixel/fitr/internal/render"
	"github.com/blisspixel/fitr/internal/score"
	"github.com/blisspixel/fitr/internal/stats"
)

func cmdScreenshots(ctx context.Context, args []string) int {
	dir := "docs/assets"
	if len(args) > 0 {
		dir = args[0]
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		errPrint(err.Error(), "", "")
		return exitError
	}
	// Colour and unicode on regardless of TTY or the caller's environment: the
	// capture pipe is not a terminal, and checked-in screenshots must reproduce.
	oldNoColor, hadNoColor := os.LookupEnv("NO_COLOR")
	oldForceColor, hadForceColor := os.LookupEnv("FORCE_COLOR")
	oldUnicode, hadUnicode := os.LookupEnv("FITR_UNICODE")
	os.Setenv("NO_COLOR", "")
	os.Setenv("FORCE_COLOR", "1")
	os.Setenv("FITR_UNICODE", "1")
	defer restoreEnv("NO_COLOR", oldNoColor, hadNoColor)
	defer restoreEnv("FORCE_COLOR", oldForceColor, hadForceColor)
	defer restoreEnv("FITR_UNICODE", oldUnicode, hadUnicode)

	shots := []struct {
		name string
		fn   func(ctx context.Context) (string, error)
	}{
		{"advise", shotAdvise}, {"run", shotRun}, {"apply", shotApply},
		{"board", shotBoard}, {"doctor", shotDoctor}, {"compare", shotCompare},
	}
	for _, s := range shots {
		text, err := captureStdout(ctx, s.fn)
		if err != nil {
			errPrint(s.name+": "+err.Error(), "", "")
			return exitError
		}
		path := filepath.Join(dir, s.name+".svg")
		if err := os.WriteFile(path, []byte(ansiToSVG(text)), 0o644); err != nil {
			errPrint(err.Error(), "", "")
			return exitError
		}
		fmt.Fprintf(os.Stderr, "wrote %s\n", path)
	}
	return exitOK
}

func restoreEnv(key, value string, existed bool) {
	if existed {
		os.Setenv(key, value)
		return
	}
	os.Unsetenv(key)
}

func captureStdout(ctx context.Context, fn func(context.Context) (string, error)) (string, error) {
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		return "", err
	}
	os.Stdout = w
	pre, ferr := fn(ctx)
	w.Close()
	os.Stdout = old
	out, _ := io.ReadAll(r)
	return pre + string(out), ferr
}

// shotRun renders the golden fixture through the real scorecard renderer.
// shotAdvise prints a Low-memory verdict through the real advise printer.
// The numbers are demo: Qwen3-30B-A3B architecture, a 20 GB budget, 32k ctx.
func shotAdvise(ctx context.Context) (string, error) {
	in := advise.Input{
		Model: "qwen3:30b", Quant: "Q4_K_M", Backend: "ollama",
		WeightsB: 18 * advise.GiB, HaveGB: 20, HaveSrc: "demo (not your GPU)",
		Ctx: 32768, Source: "demo GGUF metadata",
		Arch: advise.Arch{
			Name: "qwen3moe", Blocks: 48, Embed: 2048, Heads: 32, KVHeads: 4,
			KeyLength: 128, ValLength: 128, MaxCtx: 40960,
			Experts: 128, ExpertUsed: 8, FFN: 768, Vocab: 151936,
			Params: 30532132864,
		},
	}
	fmt.Println("$ fitr advise qwen3:30b")
	fmt.Println()
	advise.Write(os.Stdout, advise.Evaluate(in))
	return "", nil
}

func shotApply(ctx context.Context) (string, error) {
	fmt.Println("$ fitr apply qwen3:30b")
	fmt.Println()
	advise.WriteApply(os.Stdout, advise.PlanApply("ollama", "qwen3:30b", 4096))
	return "", nil
}

func shotRun(ctx context.Context) (string, error) {
	b, err := os.ReadFile(goldenResultPath())
	if err != nil {
		return "", fmt.Errorf("run from the repo root: %w", err)
	}
	var res Result
	if err := json.Unmarshal(b, &res); err != nil {
		return "", err
	}
	prof, err := device.SelectProfile("lappy", res.Device)
	if err != nil {
		return "", err
	}
	sc := score.Score(measure(&res), prof)
	trials := len(res.CodeWrite) + len(res.CodeFix) + len(res.Tools) + len(res.Checks) + 1
	meta := render.Meta{
		ParamSize: res.ModelMeta.Details.ParameterSize, Quant: res.ModelMeta.Details.QuantizationLevel,
		Family: res.ModelMeta.Details.Family, GPU: res.Device.GPU, Driver: res.Device.GPUDriver,
		Device: res.Device.InferenceDevice, Profile: res.Profile,
		StartedAt: res.StartedAt, Level: res.Level, WallSeconds: res.WallSeconds,
		NumCtx: resultNumCtx(&res), Repeats: res.Repeats,
		DecodeMean: res.DecodeSum.Mean, DecodeSD: res.DecodeSum.SD,
		DecodeMin: res.DecodeSum.Min, DecodeMax: res.DecodeSum.Max, DecodeN: res.DecodeSum.N,
		PrefillMean: res.PrefillSum.Mean, PrefillSD: res.PrefillSum.SD, PrefillN: res.PrefillSum.N,
		TTFTMean: res.TTFTSum.Mean, TTFTSD: res.TTFTSum.SD, TTFTN: res.TTFTSum.N,
		ResidentGB: res.Memory.ResidentGB,
		Trials:     trials, MDEpp: 100 * stats.MinDetectableEffect(trials, 1),
	}
	for _, sample := range res.Speed {
		meta.DecodeSeries = append(meta.DecodeSeries, sample.DecodeTPS)
		meta.PrefillSeries = append(meta.PrefillSeries, sample.PrefillTPS)
		meta.TTFTSeries = append(meta.TTFTSeries, sample.TTFT)
	}
	pre := "$ fitr run qwen3-coder:30b --full --pull\n\n"
	disp := render.New("rich")
	disp.Result(sc, meta)
	return pre, nil
}

// shotDoctor prints a representative doctor result through the shared printer.
func shotDoctor(ctx context.Context) (string, error) {
	r := eval.DoctorResult{
		Model: "qwen3-coder:30b", Runs: 5,
		Checks: []eval.DoctorCheck{
			{ID: "real_token", State: "PASS", Detail: "generated 2 token(s), TTFT 0.91s, load 8.2s"},
			{ID: "placement", State: "PASS", Detail: "GPU 100%"},
			{ID: "served_context", State: "PASS", Detail: "~2.8k-token prompt evaluated as 2736 tokens"},
			{ID: "determinism_text", State: "PASS", Detail: "5/5 runs byte-identical - greedy decoding reproduces " +
				"on this stack (bounds divergence <45% at 95% CL; raise -n to tighten)"},
			{ID: "determinism_json", State: "WARN", Detail: "3 distinct output(s) across 5 identical requests " +
				"(first divergence at byte 214) - plain text reproduces but JSON mode does not: the constrained " +
				"decoding path is the culprit (a known local-stack bug class); prefer prompt-level JSON when you " +
				"need repeatability"},
			{ID: "config", State: "WARN", Detail: "OLLAMA_MAX_LOADED_MODELS=2 allows a second resident model " +
				"to contaminate timings"},
		},
		Verdict: "measurable, with 2 caveat(s) worth knowing before trusting numbers",
		Healthy: true,
	}
	fmt.Println("$ fitr doctor qwen3-coder:30b")
	fmt.Println()
	fmt.Println("doctor: qwen3-coder:30b on AMD Radeon(TM) 780M (0.32.14)")
	printDoctor(os.Stdout, r, true)
	return "", nil
}

// shotCompare runs the REAL cmdCompare against two mock results in a
// temporary results dir, paired on a shared seedset.
func shotCompare(ctx context.Context) (string, error) {
	dir, err := os.MkdirTemp("", "fitr_shots")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	os.Setenv("FITR_RESULTS", dir)
	defer os.Unsetenv("FITR_RESULTS")

	a := mockResult("qwen3-coder:30b", 23.16, 0.44, 226.64, 3.10, 6, 6, 14, 16)
	b := mockResult("llama3.1:8b", 14.61, 0.52, 148.20, 4.70, 4, 6, 5, 16)
	for _, r := range []*Result{a, b} {
		if _, err := save(r); err != nil {
			return "", err
		}
	}
	fmt.Println("$ fitr compare qwen3-coder:30b llama3.1:8b")
	fmt.Println()
	if code := cmdCompare(ctx, []string{a.Model, b.Model}); code != exitOK {
		return "", fmt.Errorf("cmdCompare exited %d", code)
	}
	return "", nil
}

func shotBoard(ctx context.Context) (string, error) {
	dir, err := os.MkdirTemp("", "fitr_shots")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	os.Setenv("FITR_RESULTS", dir)
	defer os.Unsetenv("FITR_RESULTS")

	cur := device.Detect(ctx, probeBackend(ctx))
	a := mockResult("qwen3:30b-q4", 23.16, 0.44, 226.64, 3.10, 6, 6, 14, 16)
	a.ModelMeta.Details.ParameterSize = "30.5B"
	a.ModelMeta.Details.QuantizationLevel = "Q4_K_M"
	a.Memory.ResidentGB = 20.34
	a.Scorecard.Serves = []string{"fast_and_decent", "coding", "structured_output"}
	b := mockResult("llama3.1:8b", 14.61, 0.52, 148.20, 4.70, 4, 6, 5, 16)
	b.ModelMeta.Details.ParameterSize = "8.0B"
	b.ModelMeta.Details.QuantizationLevel = "Q4_K_M"
	b.Memory.ResidentGB = 5.10
	b.Scorecard.Serves = []string{"fast_and_decent", "low_footprint"}
	// Same key as Detect so the board labels this block "this machine"
	// instead of "different hardware" (the rows are demo; the grouping is real).
	a.Device, a.DeviceKey = cur, cur.Key()
	b.Device, b.DeviceKey = cur, cur.Key()
	for _, r := range []*Result{a, b} {
		if _, err := save(r); err != nil {
			return "", err
		}
	}
	fmt.Println("$ fitr board")
	if code := cmdBoard(ctx, []string{"--display", "rich"}); code != exitOK {
		return "", fmt.Errorf("cmdBoard exited %d", code)
	}
	return "", nil
}

func goldenResultPath() string {
	for _, p := range []string{
		filepath.Join("cmd", "fitr", "testdata", "golden_result.json"),
		filepath.Join("testdata", "golden_result.json"),
	} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return filepath.Join("cmd", "fitr", "testdata", "golden_result.json")
}

// mockResult fabricates a plausible stored result for the compare screenshot.
// The first checksN-checksPass generated checks fail, so two models sharing a
// seedset produce clean discordant sets for the McNemar demo.
func mockResult(model string, dec, decSD, pre, preSD float64, codePass, codeN, checksPass, checksN int) *Result {
	r := &Result{
		SchemaVersion: 4, Model: model, StartedAt: "2026-08-19T09:00:00Z",
		Level: "full", Repeats: 3, NumCtx: eval.NumCtx, SeedSet: "shared1",
		DeviceKey: "lappy|AMD Radeon(TM) 780M|32.0.31007.5012|0.32.14|1|f16",
		Device: device.Fingerprint{
			Host: "lappy", GPU: "AMD Radeon(TM) 780M",
			GPUDriver: "32.0.31007.5012", Runtime: "0.32.14",
			Config: map[string]string{"OLLAMA_KV_CACHE_TYPE": "f16", "OLLAMA_FLASH_ATTENTION": "1"},
		},
	}
	r.DecodeSum = stats.Summary{Mean: dec, SD: decSD, N: 3, Min: dec - decSD, Max: dec + decSD}
	r.PrefillSum = stats.Summary{Mean: pre, SD: preSD, N: 3, Min: pre - preSD, Max: pre + preSD}
	r.Speed = []eval.SpeedResult{
		{DecodeTPS: dec - decSD, PrefillTPS: pre - preSD},
		{DecodeTPS: dec, PrefillTPS: pre},
		{DecodeTPS: dec + decSD, PrefillTPS: pre + preSD},
	}
	for i := 0; i < codeN; i++ {
		res := eval.ExecResult{Pass: i < codePass}
		if i%2 == 0 {
			r.CodeWrite = append(r.CodeWrite, res)
		} else {
			r.CodeFix = append(r.CodeFix, res)
		}
	}
	needs := []string{"structured_output", "structured_output", "structured_output", "structured_output",
		"structured_output", "structured_output", "structured_output", "instruction_precision",
		"instruction_precision", "instruction_precision", "instruction_precision", "reasoning",
		"reasoning", "reasoning", "reasoning", "reasoning"}
	for i := 0; i < checksN; i++ {
		r.Checks = append(r.Checks, eval.CheckOutcome{
			TaskID: fmt.Sprintf("task%02d", i), Need: needs[i%len(needs)], Origin: "builtin",
			Seed: uint64(1000 + i), Pass: i >= checksN-checksPass,
		})
	}
	return r
}

// ---------------------------------------------------------------- ansi->svg
// ansiToSVG turns fitr's own ANSI output into a self-contained SVG that
// renders identically in every browser and both GitHub themes (it carries
// its own dark background).
func ansiToSVG(text string) string {
	colors := map[string]string{
		"":     "#e6edf3",
		"31":   "#f85149",
		"32":   "#3fb950",
		"33":   "#d29922",
		"35":   "#d2a8ff",
		"90":   "#8b949e",
		"1;34": "#79c0ff",
	}
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	const charW, lineH, pad = 7.85, 19, 18
	maxCols := 0
	type span struct{ color, text string }
	var rows [][]span
	for _, line := range lines {
		var spans []span
		cur, color, cols := strings.Builder{}, "", 0
		flush := func() {
			if cur.Len() > 0 {
				spans = append(spans, span{color, cur.String()})
				cur.Reset()
			}
		}
		for i := 0; i < len(line); i++ {
			if line[i] == 0x1b && i+1 < len(line) && line[i+1] == '[' {
				j := strings.IndexByte(line[i:], 'm')
				if j < 0 {
					break
				}
				code := line[i+2 : i+j]
				flush()
				if code == "0" {
					color = ""
				} else {
					color = code
				}
				i += j
				continue
			}
			cur.WriteByte(line[i])
			if line[i]&0xC0 != 0x80 { // count runes, not bytes
				cols++
			}
		}
		flush()
		if cols > maxCols {
			maxCols = cols
		}
		rows = append(rows, spans)
	}

	w := float64(maxCols)*charW + 2*pad
	h := float64(len(rows)*lineH) + 2*pad + 6
	var sb strings.Builder
	fmt.Fprintf(&sb, `<svg xmlns="http://www.w3.org/2000/svg" width="%.0f" height="%.0f" viewBox="0 0 %.0f %.0f">`, w, h, w, h)
	fmt.Fprintf(&sb, `<rect width="100%%" height="100%%" rx="8" fill="#0d1117" stroke="#30363d"/>`)
	fmt.Fprintf(&sb, `<g font-family="ui-monospace,SFMono-Regular,Menlo,Consolas,monospace" font-size="13">`)
	for i, spans := range rows {
		y := pad + 13 + i*lineH
		fmt.Fprintf(&sb, `<text x="%d" y="%d" xml:space="preserve">`, pad, y)
		for _, s := range spans {
			fill, weight := colors[""], ""
			if c, ok := colors[s.color]; ok {
				fill = c
			}
			if strings.HasPrefix(s.color, "1") {
				weight = ` font-weight="bold"`
			}
			fmt.Fprintf(&sb, `<tspan fill="%s"%s>%s</tspan>`, fill, weight, xmlEscape(s.text))
		}
		sb.WriteString(`</text>`)
	}
	sb.WriteString(`</g></svg>`)
	return sb.String()
}

func xmlEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	return strings.ReplaceAll(s, ">", "&gt;")
}
