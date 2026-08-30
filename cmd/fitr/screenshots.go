package main

// `fitr screenshots [dir]` regenerates the README's terminal images from MOCK
// data pushed through the REAL display code paths, so a screenshot cannot
// drift from what the tool actually prints. Dev-only; not listed in usage.
// The mock numbers are labeled as such by the model names they carry.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/blisspixel/fitr/internal/advise"
	"github.com/blisspixel/fitr/internal/atomicfile"
	"github.com/blisspixel/fitr/internal/buildinfo"
	"github.com/blisspixel/fitr/internal/device"
	"github.com/blisspixel/fitr/internal/eval"
	"github.com/blisspixel/fitr/internal/record"
	"github.com/blisspixel/fitr/internal/render"
	"github.com/blisspixel/fitr/internal/score"
	"github.com/blisspixel/fitr/internal/stats"
	"github.com/blisspixel/fitr/internal/top"
)

func cmdScreenshots(ctx context.Context, args []string) int {
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help") {
		fmt.Fprintln(os.Stderr, "usage: fitr screenshots [directory]")
		return exitOK
	}
	if len(args) > 1 {
		errPrint("too many arguments", "screenshots accepts at most one output directory", "fitr screenshots [directory]")
		return exitUsage
	}
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
		{"inventory", shotInventory},
		{"advise", shotAdvise}, {"run", shotRun}, {"apply", shotApply},
		{"board", shotBoard}, {"top", shotTop}, {"doctor", shotDoctor}, {"compare", shotCompare},
	}
	for _, s := range shots {
		text, err := captureStdout(ctx, s.fn)
		if err != nil {
			errPrint(s.name+": "+err.Error(), "", "")
			return exitError
		}
		path := filepath.Join(dir, s.name+".svg")
		if err := atomicfile.Write(path, []byte(ansiToSVG(text)), 0o644); err != nil {
			errPrint(err.Error(), "", "")
			return exitError
		}
		fmt.Fprintf(os.Stderr, "wrote %s\n", terminalText(path))
	}
	return exitOK
}

func shotTop(context.Context) (string, error) {
	a := mockResult("qwen3:30b-q4", 23.16, .44, 226.64, 3.10, 6, 6, 20, 22)
	a.ModelMeta.Details.ParameterSize = "30.5B"
	a.ModelMeta.Details.QuantizationLevel = "Q4_K_M"
	a.Memory.ResidentGB = 20.34
	a.Scorecard.Serves = []string{"fast_and_decent", "coding", "structured_output"}
	b := mockResult("llama3.1:8b", 14.61, .52, 148.20, 4.70, 4, 6, 12, 22)
	b.ModelMeta.Details.ParameterSize = "8.0B"
	b.ModelMeta.Details.QuantizationLevel = "Q4_K_M"
	b.Memory.ResidentGB = 5.10
	b.Scorecard.Serves = []string{"fast_and_decent", "low_footprint"}
	a.DeviceKey = "demo-device|ctx=8192"
	b.DeviceKey = a.DeviceKey
	a.Device.GPU, b.Device.GPU = "Demo GPU 24 GB", "Demo GPU 24 GB"
	a.Device.Runtime, b.Device.Runtime = "llama-server b7000", "llama-server b7000"
	if err := prepareMockEvidence(a); err != nil {
		return "", err
	}
	if err := prepareMockEvidence(b); err != nil {
		return "", err
	}
	snapshot := buildTopSnapshot([]*Result{a, b})
	state := top.NewState(snapshot)
	// The demo canvas matches the width every other surface composes to, so
	// the docs show one terminal rather than eight different ones.
	state.View, state.Width, state.Height = top.ViewBoard, render.DefaultWidth, 18
	return "$ fitr top\n\n" + topCanvasANSI(top.Render(state, top.DefaultGlyphs(false))), nil
}

func topCanvasANSI(canvas top.Canvas) string {
	var out strings.Builder
	for _, row := range canvas.Rows {
		for _, span := range row {
			style := ""
			switch span.Role {
			case top.RoleMuted:
				style = "\x1b[90m"
			case top.RoleHeader:
				style = "\x1b[1;34m"
			case top.RoleAccent:
				style = "\x1b[35m"
			case top.RolePass:
				style = "\x1b[32m"
			case top.RoleFail:
				style = "\x1b[31m"
			case top.RoleWarning:
				style = "\x1b[33m"
			case top.RoleSelected:
				style = "\x1b[7m"
			}
			out.WriteString(style)
			out.WriteString(span.Text)
			if style != "" {
				out.WriteString("\x1b[0m")
			}
		}
		out.WriteByte('\n')
	}
	return out.String()
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
	// The frozen golden record predates the fixed 22-spec plan. Rebuild its
	// generated-check evidence from the current embedded spec so the command in
	// the screenshot is reproducible by today's default full-run semantics.
	spec, err := eval.LoadSpec()
	if err != nil {
		return "", err
	}
	failures := map[string]bool{
		"json_object_nested": true,
		"math_chain_noise":   true,
		"tool_call_strict":   true,
	}
	res.Checks = make([]eval.CheckOutcome, 0, len(spec.Checks))
	for _, check := range spec.Checks {
		pass := !failures[check.ID]
		res.Checks = append(res.Checks, eval.CheckOutcome{
			TaskID: check.ID, Family: check.Family, Need: check.Need, Origin: check.Origin,
			Seed: eval.InstanceSeed(res.SeedSet, check.ID, 0), Pass: pass, Outcome: mockBinaryOutcome(pass),
		})
	}
	if err := prepareMockEvidence(&res); err != nil {
		return "", err
	}
	artifact, err := artifactFrom(&res)
	if err != nil {
		return "", err
	}
	pre := "$ fitr run qwen3-coder:30b --full --pull\n\n"
	disp := render.New("rich")
	disp.Result(artifact.Scorecard, resultMeta(&res, artifact.Profile))
	return pre, nil
}

func shotInventory(context.Context) (string, error) {
	fmt.Println("$ fitr")
	fmt.Println()
	render.WriteInventory(os.Stdout, render.Inventory{
		// Tracks the real version. Hardcoding it left the headline screenshot
		// claiming 0.9.6 two releases later, which is the first thing a reader
		// sees and the easiest thing to leave stale.
		Fitr: buildinfo.Version(), CPU: "AMD Ryzen 7 7840U  (16 logical)", GPU: "AMD Radeon 780M",
		GPUBackend: "rocm", MemoryGB: 32, MemorySource: "unified memory (system RAM)",
		RuntimeKind: "ollama", RuntimeURL: "http://127.0.0.1:11434",
		Profile: "lappy", Uncalibrated: false,
		Rows: []render.InventoryRow{
			{Model: "qwen3:30b-q4", State: "measured", Fit: "low_memory", SizeB: 18 << 30, Loaded: true,
				Ctx: "16k/8k", Next: "fitr apply qwen3:30b-q4",
				Note:    "measured ctx=16384; serving ctx=8192",
				Windows: "2k ok | 4k ok | 8k ok | *16k ok | >32k no"},
			{Model: "gemma4:12b", State: "unproven", Fit: "compatible", SizeB: 8 << 30,
				Next: "fitr run gemma4:12b", Windows: "2k ok | 4k ok | *8k ok | 16k ok | 32k no"},
			{Model: "qwen3:32b", State: "stale", SizeB: 20 << 30, Next: "fitr run qwen3:32b",
				Note: "device or runtime changed since the last measurement"},
			{Model: "llama3.1:70b", State: "incompatible", Fit: "incompatible", SizeB: 40 << 30, Next: "try a smaller quant",
				Note: "weights 40.0 GB exceed 32.0 GB (unified memory (system RAM))"},
		},
	}, "rich")
	return "", nil
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

	a := mockResult("qwen3-coder:30b", 23.16, 0.44, 226.64, 3.10, 6, 6, 20, 22)
	b := mockResult("llama3.1:8b", 14.61, 0.52, 148.20, 4.70, 4, 6, 12, 22)
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
	a := mockResult("qwen3:30b-q4", 23.16, 0.44, 226.64, 3.10, 6, 6, 20, 22)
	a.ModelMeta.Details.ParameterSize = "30.5B"
	a.ModelMeta.Details.QuantizationLevel = "Q4_K_M"
	a.Memory.ResidentGB = 20.34
	a.Scorecard.Serves = []string{"fast_and_decent", "coding", "structured_output"}
	b := mockResult("llama3.1:8b", 14.61, 0.52, 148.20, 4.70, 4, 6, 12, 22)
	b.ModelMeta.Details.ParameterSize = "8.0B"
	b.ModelMeta.Details.QuantizationLevel = "Q4_K_M"
	b.Memory.ResidentGB = 5.10
	b.Scorecard.Serves = []string{"fast_and_decent", "low_footprint"}
	// Same key as Detect so the board labels this block "this machine"
	// instead of "different hardware" (the rows are demo; the grouping is real).
	a.Device, a.DeviceKey = cur, cur.Key()
	b.Device, b.DeviceKey = cur, cur.Key()
	if err := prepareMockEvidence(a); err != nil {
		return "", err
	}
	if err := prepareMockEvidence(b); err != nil {
		return "", err
	}
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
// seedset produce clean discordant families for the paired exact demo.
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
	for i := range codeN {
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
	for i := range checksN {
		pass := i >= checksN-checksPass
		r.Checks = append(r.Checks, eval.CheckOutcome{
			TaskID: fmt.Sprintf("task%02d", i), Family: fmt.Sprintf("family%02d", i),
			Need: needs[i%len(needs)], Origin: "builtin",
			Seed: uint64(1000 + i), Pass: pass, Outcome: mockBinaryOutcome(pass),
		})
	}
	if err := prepareMockEvidence(r); err != nil {
		panic(err)
	}
	return r
}

// prepareMockEvidence gives demo and test records the same sealed denominator
// contract as a real current result. Executable observations remain explicitly
// inconclusive because the mock never ran an isolated verifier.
func prepareMockEvidence(r *Result) error {
	if r == nil {
		return errors.New("nil mock result")
	}
	if r.Profile == "" {
		r.Profile = "default"
	}
	if r.DeviceKey == "" {
		r.DeviceKey = "mock-device|mock-runtime|ctx=8192"
	}
	if r.Device.Host == "" {
		r.Device.Host = "mock-host"
	}
	if r.Device.OS == "" {
		r.Device.OS = "mock-os"
	}
	if r.Device.CPU == "" {
		r.Device.CPU = "mock-cpu"
	}
	if r.Device.GPU == "" {
		r.Device.GPU = "mock-gpu"
	}
	if r.Device.Runtime == "" {
		r.Device.Runtime = "mock-runtime-v1"
	}
	if r.Device.InferenceDevice == "" {
		r.Device.InferenceDevice = "mock placement"
	}
	if r.Device.Config == nil {
		r.Device.Config = map[string]string{}
	}
	if r.SeedSet == "" {
		r.SeedSet = "mock-seedset-v1"
	}
	r.SchemaVersion = record.EvidenceSchemaVersion
	r.ExecutionPolicy = record.ExecutionDisabled
	r.TaskPlan = record.TaskPlan{
		SpeedSamples:     len(r.Speed),
		Memory:           true,
		CodeTrials:       len(r.CodeWrite) + len(r.CodeFix),
		CheckTrialsLimit: len(r.Checks),
		Plumbing:         r.Plumbing != nil,
		ToolTrials:       len(r.Tools),
		Withdrawal:       r.Withdrawal != nil,
		RefusalTrials:    len(r.Refusal),
		AgenticTrials:    boolInt(r.Agentic != nil),
	}
	for i := range r.Checks {
		if r.Checks[i].TaskID == "" {
			r.Checks[i].TaskID = fmt.Sprintf("mock-check-%02d", i)
		}
		if r.Checks[i].Family == "" {
			r.Checks[i].Family = r.Checks[i].TaskID
		}
		if r.Checks[i].Need == "" {
			r.Checks[i].Need = "user_tasks"
		}
		if r.Checks[i].Origin == "" {
			r.Checks[i].Origin = "builtin"
		}
	}
	if r.TaskPlan.CheckTrialsLimit > 0 {
		checkPlanSHA256, err := record.ObservedCheckPlanSHA256(r.Checks)
		if err != nil {
			return err
		}
		r.TaskPlan.CheckPlanSHA256 = checkPlanSHA256
	}

	coding := make([]eval.Outcome, 0, r.TaskPlan.CodeTrials)
	for i := range r.CodeWrite {
		r.CodeWrite[i].Outcome = eval.OutcomeInconclusive
		coding = append(coding, eval.OutcomeInconclusive)
	}
	for i := range r.CodeFix {
		r.CodeFix[i].Outcome = eval.OutcomeInconclusive
		coding = append(coding, eval.OutcomeInconclusive)
	}
	checks := make([]eval.Outcome, 0, len(r.Checks))
	for i := range r.Checks {
		if r.Checks[i].Outcome == "" {
			r.Checks[i].Outcome = mockBinaryOutcome(r.Checks[i].Pass)
		}
		checks = append(checks, r.Checks[i].Outcome)
	}
	tools := make([]eval.Outcome, len(r.Tools))
	for i := range r.Tools {
		r.Tools[i].Outcome = eval.OutcomeInconclusive
		tools[i] = eval.OutcomeInconclusive
	}
	refusal := make([]eval.Outcome, 0, len(r.Refusal))
	for key, verdict := range r.Refusal {
		if verdict.Outcome == "" {
			verdict.Outcome = mockBinaryOutcome(verdict.Verdict == "answered")
			r.Refusal[key] = verdict
		}
		refusal = append(refusal, verdict.Outcome)
	}
	if r.TaskPlan.RefusalTrials > 0 {
		refusalPlanSHA256, err := record.ObservedRefusalPlanSHA256(r.Refusal)
		if err != nil {
			return err
		}
		r.TaskPlan.RefusalPlanSHA256 = refusalPlanSHA256
	}
	plumbing := []eval.Outcome{}
	if r.Plumbing != nil {
		if r.Plumbing.Outcome == "" {
			r.Plumbing.Outcome = mockBinaryOutcome(r.Plumbing.Healthy)
		}
		plumbing = append(plumbing, r.Plumbing.Outcome)
	}
	withdrawal := []eval.Outcome{}
	if r.Withdrawal != nil {
		if r.Withdrawal.Outcome == "" {
			r.Withdrawal.Outcome = mockBinaryOutcome(r.Withdrawal.Pass)
		}
		withdrawal = append(withdrawal, r.Withdrawal.Outcome)
	}
	agentic := []eval.Outcome{}
	if r.Agentic != nil {
		r.Agentic.Outcome = eval.OutcomeInconclusive
		agentic = append(agentic, eval.OutcomeInconclusive)
	}
	r.EvidenceCounts = map[string]eval.OutcomeCounts{
		"coding":     eval.CountOutcomes(r.TaskPlan.CodeTrials, coding...),
		"checks":     eval.CountOutcomes(r.TaskPlan.CheckTrialsLimit, checks...),
		"tools":      eval.CountOutcomes(r.TaskPlan.ToolTrials, tools...),
		"refusal":    eval.CountOutcomes(r.TaskPlan.RefusalTrials, refusal...),
		"plumbing":   eval.CountOutcomes(boolInt(r.TaskPlan.Plumbing), plumbing...),
		"withdrawal": eval.CountOutcomes(boolInt(r.TaskPlan.Withdrawal), withdrawal...),
		"agentic":    eval.CountOutcomes(r.TaskPlan.AgenticTrials, agentic...),
	}
	decode, ttft, prefill := make([]float64, 0, len(r.Speed)), make([]float64, 0, len(r.Speed)), make([]float64, 0, len(r.Speed))
	for _, sample := range r.Speed {
		decode = append(decode, sample.DecodeTPS)
		ttft = append(ttft, sample.TTFT)
		prefill = append(prefill, sample.PrefillTPS)
	}
	for i := range r.Speed {
		r.Speed[i].FirstOutputObserved = true
		r.Speed[i].GatedCacheKnown = true
		r.Speed[i].PrefillCacheKnown = true
		if r.Speed[i].GatedPromptTok == 0 && r.Speed[i].GatedCachedTok == 0 {
			r.Speed[i].GatedPromptTok = 1
		}
		if r.Speed[i].PromptTok == 0 && r.Speed[i].CachedPromptTok == 0 {
			r.Speed[i].PromptTok = 1
		}
		if r.Speed[i].ColdTTFT > 0 && r.Speed[i].ColdLoad <= 0.1 {
			r.Speed[i].ColdLoad = r.Speed[i].ColdTTFT
		}
		if r.Speed[i].WarmTTFT > 0 {
			r.Speed[i].WarmCacheKnown = true
			r.Speed[i].WarmCachedTok = 1
		}
	}
	if r.Memory.ResidentGB > 0 {
		r.Memory.Outcome = eval.OutcomePass
		r.Memory.UnavailableReason = ""
		r.Memory.RequestedCtx = memoryProbeCtx
		effectiveMemoryCtx := memoryProbeCtx
		r.Memory.EffectiveCtx = &effectiveMemoryCtx
		r.Memory.ResidentBytes = int64(r.Memory.ResidentGB * advise.GiB)
		r.Memory.AcceleratorBytes = r.Memory.ResidentBytes * int64(r.Memory.PctOnGPU) / 100
		r.Memory.PctOnGPU = int(100 * float64(r.Memory.AcceleratorBytes) / float64(r.Memory.ResidentBytes))
	} else {
		r.Memory.Outcome = eval.OutcomeSkipped
		r.Memory.RequestedCtx = memoryProbeCtx
		r.Memory.UnavailableReason = "demo runtime did not report a resident allocation"
	}
	r.DecodeSum, r.TTFTSum, r.PrefillSum = stats.MeanSD(decode), stats.MeanSD(ttft), stats.MeanSD(prefill)
	longest := ""
	for _, result := range append(append([]eval.ExecResult{}, r.CodeWrite...), r.CodeFix...) {
		if len(result.Raw) > len(longest) {
			longest = result.Raw
		}
	}
	r.Refused = 0
	for _, verdict := range r.Refusal {
		if len(verdict.Text) > len(longest) {
			longest = verdict.Text
		}
		if verdict.Outcome == eval.OutcomeFail {
			r.Refused++
		}
	}
	r.Rep, r.Density = score.RepetitionMetrics(longest), score.InformationDensity(longest)
	effective := r.ContextSize()
	fingerprintV2, err := device.NewFingerprintV2(r.Device, device.ContextVerification{
		RequestedTokens: effective, EffectiveTokens: &effective,
		EffectiveSource: device.ContextSourceRuntimeReport,
	})
	if err != nil {
		return err
	}
	r.DeviceV2 = &fingerprintV2
	r.DeviceKey, err = fingerprintV2.ComparabilityKey()
	if err != nil {
		return err
	}

	sum := sha256.Sum256([]byte("fitr mock artifact\x00" + r.Model))
	identity, err := record.NewModelIdentity(r.Model, r.Model, "mock", "mock-runtime-v1",
		"sha256:"+hex.EncodeToString(sum[:]), "", 0)
	if err != nil {
		return err
	}
	spec, err := eval.LoadSpec()
	if err != nil {
		return err
	}
	hashes, err := eval.EffectiveHashes(spec)
	if err != nil {
		return err
	}
	softwareBuild, err := buildinfo.BinarySHA256()
	if err != nil {
		return err
	}
	profile, err := device.SelectProfile(r.Profile, r.Device)
	if err != nil {
		return err
	}
	provenance, err := record.NewRunProvenance(hashes.TaskSetSHA256, hashes.SpecSHA256,
		profile, record.CurrentScoringPolicy(),
		record.SoftwareReceipt{FitrVersion: buildinfo.Version(), SoftwareBuildSHA256: softwareBuild,
			BackendProtocol: "fitr.backend.mock.v1"})
	if err != nil {
		return err
	}
	r.Manifest = nil
	r.Completion = nil
	if err := r.AttachManifest(identity, provenance); err != nil {
		return err
	}
	r.Scorecard = score.Score(measure(r), profile)
	return r.CompleteEvidence(profile)
}

func mockBinaryOutcome(pass bool) eval.Outcome {
	if pass {
		return eval.OutcomePass
	}
	return eval.OutcomeFail
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
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
