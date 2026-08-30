package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"

	"github.com/blisspixel/fitr/internal/device"
	"github.com/blisspixel/fitr/internal/eval"
	"github.com/blisspixel/fitr/internal/record"
	"github.com/blisspixel/fitr/internal/render"
	"github.com/blisspixel/fitr/internal/score"
	"github.com/blisspixel/fitr/internal/stats"
)

// ---------------------------------------------------------------- export
// cmdExport writes a self-contained HTML scorecard from a saved result.
// Never automatic: the JSON in ~/.fitr is local storage; HTML is what you
// share, and it contains a hardware fingerprint.
func cmdExport(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("export", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	out := fs.String("out", "", "HTML path (default: <results>/<model>.html)")
	retonrFlag := fs.Bool("retonr", false, "also write opt-in evidence JSON for the retonr sister project")
	if code, ok := parseCommandFlags(fs, args); !ok {
		return code
	}
	if fs.NArg() < 1 {
		errPrint("missing model", "", "fitr export <model>  or  fitr export <model> --retonr")
		return exitUsage
	}
	if fs.NArg() > 1 {
		errPrint("too many arguments", "export accepts exactly one model", "fitr export <model> [--out PATH] [--retonr]")
		return exitUsage
	}
	model := normalizeModelRef(fs.Arg(0))
	results, err := loadResults()
	if err != nil || len(results) == 0 {
		errPrint("no results yet", "", "fitr run "+model)
		return exitError
	}
	r := latestNamed(results, model)
	if r == nil {
		errPrint(fmt.Sprintf("no stored result for %q", model), "",
			"fitr run "+model)
		return exitError
	}
	path := *out
	if path == "" {
		path = "auto"
	}
	if !*retonrFlag || *out != "" {
		html, err := writeHTMLArtifact(r, path, "")
		if err != nil {
			errPrint("could not write HTML: "+err.Error(), "", "")
			return exitError
		}
		fmt.Fprintf(os.Stderr, "  wrote  %s\n", terminalText(html))
	}
	if *retonrFlag {
		evPath := filepath.Join(resultsDir(), record.ArtifactStem(r.Model)+".retonr.json")
		if err := writeRetonrEvidence(r, evPath, ""); err != nil {
			errPrint("could not write retonr evidence: "+err.Error(), "", "")
			return exitError
		}
		fmt.Fprintf(os.Stderr, "  wrote  %s\n", terminalText(evPath))
		fmt.Fprintln(os.Stderr, "  note   evidence for https://github.com/blisspixel/retonr; not a qualification")
	}
	return exitOK
}

// ---------------------------------------------------------------- view
// cmdView replays a saved measurement through the same terminal renderer used
// at run completion. With no model it selects the newest result, making the
// command a quick dashboard rather than a file-hunting exercise.
func cmdView(_ context.Context, args []string) int {
	command, code, ok := parseViewCommand(args)
	if !ok {
		return code
	}
	selected, code, ok := selectViewResult(command.candidate, command.hasCandidate)
	if !ok {
		return code
	}
	return writeViewedResult(selected, command.mode, command.full)
}

type viewCommand struct {
	candidate    string
	mode         string
	full         bool
	hasCandidate bool
}

func parseViewCommand(args []string) (viewCommand, int, bool) {
	fs := flag.NewFlagSet("view", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	viewFull := fs.Bool("full", false, "with --display json, emit the complete sealed result record instead of the scorecard")
	mode := fs.String("display", "auto", "auto|rich|plain|json|none")
	if code, ok := parseCommandFlags(fs, args); !ok {
		return viewCommand{}, code, false
	}
	if !render.ValidMode(*mode) {
		errPrint("invalid display mode", *mode, "use auto, rich, plain, json, or none")
		return viewCommand{}, exitUsage, false
	}
	if fs.NArg() > 1 {
		errPrint("too many arguments", "view accepts one model or result path", "fitr view [model|result.json]")
		return viewCommand{}, exitUsage, false
	}
	return viewCommand{candidate: fs.Arg(0), mode: *mode, full: *viewFull, hasCandidate: fs.NArg() == 1}, exitOK, true
}

func selectViewResult(candidate string, hasCandidate bool) (*Result, int, bool) {
	if !hasCandidate {
		return selectNewestResult()
	}
	if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
		result, err := record.NewStore(resultsDir()).Read(candidate)
		if err != nil {
			errPrint("not a fitr result", "expected a saved run JSON with a model", candidate)
			return nil, exitError, false
		}
		return result, exitOK, true
	}
	results, err := loadResults()
	if err != nil || len(results) == 0 {
		errPrint("no results yet", "", "fitr run "+normalizeModelRef(candidate))
		return nil, exitError, false
	}
	selected := latestNamed(results, normalizeModelRef(candidate))
	if selected == nil {
		errPrint(fmt.Sprintf("no stored result for %q", candidate), "", "fitr board")
		return nil, exitError, false
	}
	return selected, exitOK, true
}

func selectNewestResult() (*Result, int, bool) {
	results, err := loadResults()
	if err != nil || len(results) == 0 {
		errPrint("no results yet", "", "run one first: fitr run <model>")
		return nil, exitError, false
	}
	var selected *Result
	for _, result := range results {
		if selected == nil || startedAfter(result.StartedAt, selected.StartedAt) {
			selected = result
		}
	}
	return selected, exitOK, true
}

func writeViewedResult(selected *Result, mode string, full bool) int {
	switch render.Resolve(mode) {
	case "none":
		return exitOK
	case "json":
		return writeViewedJSON(selected, full)
	}
	scorecard, meta := viewedPresentation(selected)
	display := render.New(mode)
	defer display.Close()
	display.Result(scorecard, meta)
	return exitOK
}

func writeViewedJSON(selected *Result, full bool) int {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	// The sealed record carries per-trial evidence and the run manifest,
	// which is what `--full` is for. What a reader of `view` wants is the
	// scorecard the command prints.
	var payload any = selected
	if !full {
		card, meta := selected.Scorecard, resultMeta(selected, selected.Profile)
		if artifact, err := artifactFrom(selected); err == nil {
			card, meta = artifact.Scorecard, artifact.Meta
		}
		payload = map[string]any{
			"schema": "fitr.view.v1", "model": selected.Model,
			"scorecard": card, "meta": meta,
		}
	}
	if err := encoder.Encode(payload); err != nil {
		errPrint("could not render result: "+err.Error(), "", "")
		return exitError
	}
	return exitOK
}

func viewedPresentation(selected *Result) (score.Scorecard, render.Meta) {
	scorecard := selected.Scorecard
	meta := resultMeta(selected, selected.Profile)
	if artifact, err := artifactFrom(selected); err == nil {
		scorecard, meta = artifact.Scorecard, artifact.Meta
	} else {
		scorecard = score.ExcludeEvidence(scorecard,
			"the scoring profile is unavailable, so the stored verdict cannot be reproduced")
	}
	return scorecard, meta
}

// ---------------------------------------------------------------- board
func cmdBoard(ctx context.Context, args []string) int {
	command, code, ok := parseBoardCommand(args)
	if !ok {
		return code
	}
	results, err := loadResults()
	if err != nil || len(results) == 0 {
		errPrint("no results yet", "", "run one first: fitr run <model>")
		return exitError
	}
	curDevice := device.Detect(ctx, probeBackend(ctx))
	groups, order, excludedContext := groupBoardResults(results, command.current, curDevice)
	board, visible, excluded := buildBoard(groups, order, curDevice)
	excluded.context = excludedContext
	writeBoardExclusions(excluded)
	if len(board.Groups) == 0 {
		return emptyBoardResult(excluded)
	}
	if render.Resolve(command.mode) == "json" {
		writeBoardJSON(board, visible, curDevice.Key(), command.full, excluded)
		return exitOK
	}
	render.WriteBoard(os.Stdout, board, command.mode)
	return exitOK
}

type boardCommand struct {
	current bool
	full    bool
	mode    string
}

func parseBoardCommand(args []string) (boardCommand, int, bool) {
	fs := flag.NewFlagSet("board", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	current := fs.Bool("current", false, "only this machine, including its measured context variants")
	full := fs.Bool("full", false, "with --display json, emit the complete sealed result records instead of the reported columns")
	mode := fs.String("display", "auto", "auto|rich|plain|json|none")
	if code, ok := parseCommandFlags(fs, args); !ok {
		return boardCommand{}, code, false
	}
	if !render.ValidMode(*mode) {
		errPrint("invalid display mode", *mode, "use auto, rich, plain, json, or none")
		return boardCommand{}, exitUsage, false
	}
	if fs.NArg() != 0 {
		errPrint("unexpected argument", fs.Arg(0), "fitr board [--current] [--display MODE]")
		return boardCommand{}, exitUsage, false
	}
	return boardCommand{current: *current, full: *full, mode: *mode}, exitOK, true
}

func groupBoardResults(results []*Result, current bool, curDevice device.Fingerprint) (
	map[string][]*Result, []string, int) {
	// Group by fingerprint. Rows measured under different hardware/config are
	// NOT comparable and must never be ranked against each other. A |ctx=N
	// suffix is a config split, not a different box - labeled as such below.
	groups := map[string][]*Result{}
	var order []string
	excludedContext := 0
	for _, r := range results {
		key, keyErr := r.ComparableDeviceKey()
		if keyErr != nil {
			excludedContext++
			continue
		}
		if current && !samePhysicalMachine(r.Device, curDevice) {
			continue
		}
		if _, ok := groups[key]; !ok {
			order = append(order, key)
		}
		groups[key] = append(groups[key], r)
	}
	sort.Strings(order)
	return groups, order, excludedContext
}

type boardExclusions struct {
	contaminated int
	unverified   int
	context      int
	performance  int
	unscored     int
}

func buildBoard(groups map[string][]*Result, order []string, curDevice device.Fingerprint) (
	render.Board, map[string][]*Result, boardExclusions) {
	board := render.Board{}
	excluded := boardExclusions{}
	visible := map[string][]*Result{}
	for _, key := range order {
		rows := claimableBoardRows(groups[key], &excluded)
		if len(rows) == 0 {
			continue
		}
		group := makeBoardGroup(rows, curDevice)
		sort.Slice(rows, func(i, j int) bool {
			return rows[i].DecodeSum.Mean > rows[j].DecodeSum.Mean
		})
		visible[key] = rows
		for _, result := range rows {
			group.Rows = append(group.Rows, makeBoardRow(result, &excluded))
		}
		board.Results += len(rows)
		board.Groups = append(board.Groups, group)
	}
	return board, visible, excluded
}

func claimableBoardRows(rows []*Result, excluded *boardExclusions) []*Result {
	clean := make([]*Result, 0, len(rows))
	for _, result := range rows {
		if len(result.Contamination) > 0 {
			excluded.contaminated++
			continue
		}
		if result.EvidenceIntegrityIssue() != "" {
			excluded.unverified++
			continue
		}
		if !hasSupportedBoardDecode(result) {
			excluded.performance++
			continue
		}
		clean = append(clean, result)
	}
	return clean
}

func makeBoardGroup(rows []*Result, curDevice device.Fingerprint) render.BoardGroup {
	latest := rows[len(rows)-1]
	nctx := resultNumCtx(latest)
	group := render.BoardGroup{
		GPU: latest.Device.GPU, Driver: latest.Device.GPUDriver,
		KV: latest.Device.Config["OLLAMA_KV_CACHE_TYPE"], NumCtx: nctx,
		Note: boardGroupNote(latest, curDevice, nctx),
	}
	if latest.DeviceV2 != nil {
		group.ContextState = string(latest.DeviceV2.Context.State())
		if latest.DeviceV2.Context.EffectiveTokens != nil {
			group.EffectiveCtx = *latest.DeviceV2.Context.EffectiveTokens
		}
	}
	return group
}

func boardGroupNote(result *Result, curDevice device.Fingerprint, nctx int) string {
	if !samePhysicalMachine(result.Device, curDevice) {
		return "different hardware/config or effective context; not comparable to other blocks"
	}
	if nctx != eval.NumCtx {
		return fmt.Sprintf("this machine, requested num_ctx=%d; effective context is part of this block", nctx)
	}
	return "this machine, verified effective context and current config"
}

func makeBoardRow(result *Result, excluded *boardExclusions) render.BoardRow {
	scorecard := result.Scorecard
	if artifact, err := artifactFrom(result); err == nil {
		scorecard = artifact.Scorecard
	} else {
		scorecard = score.ExcludeEvidence(scorecard,
			"the scoring profile is unavailable, so the stored verdict cannot be reproduced")
		excluded.unscored++
	}
	var codes []string
	for _, served := range scorecard.Serves {
		if code := score.NeedCode[served]; code != "" {
			codes = append(codes, code)
		}
	}
	meta := resultMeta(result, result.Profile)
	return render.BoardRow{
		Model: result.Model, ParamSize: result.ModelMeta.Details.ParameterSize,
		Quant:      result.ModelMeta.Details.QuantizationLevel,
		DecodeMean: meta.DecodeMean, DecodeSD: meta.DecodeSD,
		PrefillMean: meta.PrefillMean, ResidentGB: meta.ResidentGB,
		DecodeSeries: slices.Clone(meta.DecodeSeries), Repeats: result.Repeats, Serves: codes,
	}
}

func writeBoardExclusions(excluded boardExclusions) {
	if excluded.contaminated > 0 {
		fmt.Fprintf(os.Stderr, "! INCONCLUSIVE: excluded %d contaminated result(s) from board ranking and claims\n",
			excluded.contaminated)
	}
	if excluded.unverified > 0 {
		fmt.Fprintf(os.Stderr, "! INCONCLUSIVE: excluded %d result(s) without a valid evidence contract from board ranking and claims\n",
			excluded.unverified)
	}
	if excluded.context > 0 {
		fmt.Fprintf(os.Stderr, "! INCONCLUSIVE: excluded %d result(s) without verified effective context from board ranking and claims\n",
			excluded.context)
	}
	if excluded.performance > 0 {
		fmt.Fprintf(os.Stderr, "! INCONCLUSIVE: excluded %d result(s) without supported decode evidence from board ranking and claims\n",
			excluded.performance)
	}
	if excluded.unscored > 0 {
		fmt.Fprintf(os.Stderr, "! INCONCLUSIVE: %d board row(s) have no reproducible qualification because their scoring profile is unavailable\n",
			excluded.unscored)
	}
}

func emptyBoardResult(excluded boardExclusions) int {
	if excluded.contaminated == 0 && excluded.unverified == 0 && excluded.context == 0 && excluded.performance == 0 {
		errPrint("no results for this machine", "", "run fitr run <model>")
		return exitError
	}
	detail := "all matching results lacked claimable evidence"
	if excluded.contaminated > 0 && excluded.unverified == 0 && excluded.context == 0 && excluded.performance == 0 {
		detail = "all matching results were contaminated"
	} else if excluded.unverified > 0 && excluded.contaminated == 0 && excluded.context == 0 && excluded.performance == 0 {
		detail = "all matching results lacked a valid evidence contract"
	} else if excluded.context > 0 && excluded.contaminated == 0 && excluded.unverified == 0 && excluded.performance == 0 {
		detail = "all matching results lacked verified effective context"
	} else if excluded.performance > 0 && excluded.contaminated == 0 && excluded.unverified == 0 && excluded.context == 0 {
		detail = "all matching results lacked supported decode evidence"
	}
	errPrint("no conclusive results for this machine", detail,
		"re-run with the current fitr version after unloading all models")
	return exitError
}

func writeBoardJSON(board render.Board, visible map[string][]*Result, current string,
	full bool, excluded boardExclusions) {
	// Default to what the board reports, not the sealed records behind it.
	// The records are an order of magnitude larger, grow without bound in
	// the number of models, and none of the extra fields appear on screen.
	payload := map[string]any{
		"schema": "fitr.board.v1", "current": current,
		"groups": board.Groups, "results": board.Results,
	}
	if full {
		payload["groups"] = visible
		payload["schema"] = "fitr.board.full.v1"
	}
	if excluded.contaminated > 0 {
		payload["inconclusive_excluded"] = excluded.contaminated
	}
	if excluded.unverified > 0 {
		payload["unverified_excluded"] = excluded.unverified
	}
	if excluded.context > 0 {
		payload["context_unverified_excluded"] = excluded.context
	}
	b, _ := json.Marshal(payload)
	fmt.Println(string(b))
}

// ---------------------------------------------------------------- compare
func cmdCompare(ctx context.Context, args []string) int {
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help") {
		fmt.Fprintln(os.Stderr, "usage: fitr compare <model-a> <model-b>")
		return exitOK
	}
	if len(args) != 2 {
		errPrint("need two models", "", "fitr compare <a> <b>")
		return exitUsage
	}
	a, b, code := loadComparisonResults(args)
	if code != exitOK {
		return code
	}
	if code := validateComparison(a, b); code != exitOK {
		return code
	}

	fmt.Printf("  %s  vs  %s\n\n", terminalText(a.Model), terminalText(b.Model))
	writeThroughputComparison(a, b)
	writeBehaviorComparison(a, b)
	fmt.Println("\n  note  throughput uses 95% intervals. Behavior differences require")
	fmt.Println("        a complete identical plan and a per-need family-level exact test.")
	return exitOK
}

func loadComparisonResults(args []string) (*Result, *Result, int) {
	results, err := loadResults()
	if err != nil {
		errPrint("no results", "", "fitr run <model>")
		return nil, nil, exitError
	}
	a, b := latestNamed(results, args[0]), latestNamed(results, args[1])
	for i, r := range []*Result{a, b} {
		if r == nil {
			errPrint(fmt.Sprintf("no stored result for %q", args[i]), "",
				"fitr run "+args[i])
			return nil, nil, exitError
		}
	}
	return a, b, exitOK
}

func validateComparison(a, b *Result) int {
	if len(a.Contamination) > 0 || len(b.Contamination) > 0 {
		return rejectContaminatedComparison(a, b)
	}
	if issueA, issueB := a.EvidenceIntegrityIssue(), b.EvidenceIntegrityIssue(); issueA != "" || issueB != "" {
		return rejectUnverifiedComparison(a, b, issueA, issueB)
	}
	if err := record.ProvenanceCompatibilityError(a, b); err != nil {
		return rejectIncompatibleProvenance(a, b, err)
	}
	return validateComparisonKeys(a, b)
}

func rejectContaminatedComparison(a, b *Result) int {
	fmt.Printf("  %s  vs  %s\n\n", terminalText(a.Model), terminalText(b.Model))
	fmt.Println("  INCONCLUSIVE  resident model contamination invalidates this comparison")
	for _, result := range []*Result{a, b} {
		if len(result.Contamination) == 0 {
			continue
		}
		fmt.Printf("  %-12s %s\n", terminalText(result.Model)+":",
			terminalText(strings.Join(result.Contamination, ", ")))
	}
	fmt.Println("  remedy       unload all models and re-run both measurements")
	return exitError
}

func rejectUnverifiedComparison(a, b *Result, issueA, issueB string) int {
	fmt.Printf("  %s  vs  %s\n\n", terminalText(a.Model), terminalText(b.Model))
	fmt.Println("  INCONCLUSIVE  a valid sealed evidence contract is required for comparison")
	for i, issue := range []string{issueA, issueB} {
		if issue == "" {
			continue
		}
		result := []*Result{a, b}[i]
		fmt.Printf("  %-12s %s\n", terminalText(result.Model)+":", terminalText(issue))
	}
	fmt.Println("  remedy       re-run both models with the current fitr version")
	return exitError
}

func rejectIncompatibleProvenance(a, b *Result, err error) int {
	fmt.Printf("  %s  vs  %s\n\n", terminalText(a.Model), terminalText(b.Model))
	fmt.Println("  INCONCLUSIVE  the runs used different task, profile, specification, scoring, or protocol provenance")
	fmt.Printf("  detail       %s\n", terminalText(err.Error()))
	fmt.Println("  remedy       compare runs produced by the same fitr battery and effective profile")
	return exitError
}

func validateComparisonKeys(a, b *Result) int {
	aKey, aKeyErr := a.ComparableDeviceKey()
	bKey, bKeyErr := b.ComparableDeviceKey()
	if aKeyErr != nil || bKeyErr != nil {
		return rejectUnverifiedContext(a, b, aKeyErr, bKeyErr)
	}
	if aKey == bKey {
		return exitOK
	}
	if a.DeviceV2 != nil && b.DeviceV2 != nil && reflect.DeepEqual(a.DeviceV2.Device, b.DeviceV2.Device) {
		return rejectDifferentComparisonContext(a, b)
	}
	// Naming the machine is misleading when both runs happened on it: the
	// usual cause is that its state moved between them, such as a model
	// resident from something else, so one run was partly offloaded and the
	// other was not. Fingerprint.Diff already knows exactly which fields moved,
	// so say them instead of sending the operator to re-measure blind.
	note := incomparableNote(a.Device.Diff(b.Device))
	errPrint("these results were not measured under the same conditions", note,
		"re-measure both with the machine in the same state, then compare")
	return exitError
}

func rejectUnverifiedContext(a, b *Result, aErr, bErr error) int {
	fmt.Printf("  %s  vs  %s\n\n", terminalText(a.Model), terminalText(b.Model))
	fmt.Println("  INCONCLUSIVE  verified effective context is required for comparison")
	for i, keyErr := range []error{aErr, bErr} {
		if keyErr == nil {
			continue
		}
		result := []*Result{a, b}[i]
		fmt.Printf("  %-12s %s\n", terminalText(result.Model)+":", terminalText(keyErr.Error()))
	}
	fmt.Println("  remedy       re-run both models on a runtime that reports its allocated context")
	return exitError
}

func rejectDifferentComparisonContext(a, b *Result) int {
	errPrint("these results used different request context",
		fmt.Sprintf("requested %d vs %d, effective %d vs %d; tok/s and quality both move with KV size",
			resultNumCtx(a), resultNumCtx(b), effectiveComparisonContext(a), effectiveComparisonContext(b)),
		"compare two runs at the same --ctx, or re-measure")
	return exitError
}

func effectiveComparisonContext(result *Result) int {
	if result.DeviceV2 == nil || result.DeviceV2.Context.EffectiveTokens == nil {
		return 0
	}
	return *result.DeviceV2.Context.EffectiveTokens
}

func writeThroughputComparison(a, b *Result) {
	// Throughput: Fieller's interval is the correct one for "how many times
	// faster". When it cannot be computed honestly (single observation, or
	// denominator not separated from zero), the ratio prints without an
	// interval and therefore without a verdict.
	for _, p := range []struct {
		label       string
		x, y        stats.Summary
		claimable   bool
		unclaimable string
	}{
		{label: "decode tok/s", x: a.DecodeSum, y: b.DecodeSum, claimable: true},
		{label: "prefill tok/s", x: a.PrefillSum, y: b.PrefillSum,
			claimable:   comparableUncachedPrefill(a) && comparableUncachedPrefill(b),
			unclaimable: "descriptive only - uncached prefill not proven"},
	} {
		writeThroughputRow(p.label, p.x, p.y, p.claimable, p.unclaimable)
	}
	fmt.Println()
}

func writeThroughputRow(label string, x, y stats.Summary, claimable bool, unclaimable string) {
	if !claimable {
		fmt.Printf("  %-21s %7.2f vs %7.2f  %s\n", label, x.Mean, y.Mean, unclaimable)
		return
	}
	lo, hi, ratio, ok := stats.FiellerRatio(x, y)
	if !ok {
		ratio, _, _ = stats.RatioWithError(x, y)
		fmt.Printf("  %-21s %7.2f vs %7.2f  %5.2fx  no interval - too little data\n",
			label, x.Mean, y.Mean, ratio)
		return
	}
	verdict := "cannot separate"
	if lo > 1 {
		verdict = "first is faster"
	} else if hi < 1 {
		verdict = "second is faster"
	}
	fmt.Printf("  %-21s %7.2f vs %7.2f  %5.2fx [%.2f-%.2f]  %s\n",
		label, x.Mean, y.Mean, ratio, lo, hi, verdict)
}

func writeBehaviorComparison(a, b *Result) {
	// Paired analysis: identical instances make item flips descriptive. A
	// significance claim aggregates those instances to one direction per
	// generated family because repeats inside a family are clustered.
	if a.SeedSet != "" && a.SeedSet == b.SeedSet {
		pairedCompare(a, b)
	} else if len(a.Checks) > 0 && len(b.Checks) > 0 {
		fmt.Println("  behavior  no winner claim: generated task families are clustered and")
		fmt.Println("            these runs faced different instances.")
		fmt.Println("  pair with fitr run <model> --seedset shared1  (both models)")
	}
}

// comparableUncachedPrefill requires every persisted probe to carry an
// explicit zero-cache receipt. Unknown or partial cache state can remain
// visible descriptively but cannot support a throughput winner claim.
func comparableUncachedPrefill(r *Result) bool {
	if r == nil || len(r.Speed) == 0 {
		return false
	}
	for _, sample := range r.Speed {
		if !sample.PrefillCacheReceiptValid() || sample.CachedPromptTok != 0 {
			return false
		}
	}
	return true
}

// pairedHang aligns the paired block's wrapped text under its own label.
const pairedHang = 11

// pairedCompare reports item flips, then runs a separate exact sign test for
// each need after every family contributes at most one direction. A claim
// requires both sealed plans to match and every planned pair to be scorable.
func pairedCompare(a, b *Result) {
	flips := eval.PairFlips(a.Checks, b.Checks)
	if flips.Shared == 0 {
		fmt.Println("  paired: seedsets match but no shared instances were found.")
		return
	}
	width := render.Width()
	render.Field(os.Stdout, "  paired", pairedHang, fmt.Sprintf(
		"on %d identical instances: %s alone passed %d, %s alone passed %d, agreed on %d",
		flips.Shared, terminalText(a.Model), flips.AOnly, terminalText(b.Model),
		flips.BOnly, flips.Agree), width)
	if flips.HidesDisagreement() {
		render.Field(os.Stdout, "  accuracy", pairedHang, fmt.Sprintf(
			"hid %d item-level flip(s) (%d/%d vs %d/%d) - rates match, the questions did not",
			flips.AOnly+flips.BOnly, flips.APass, flips.Shared, flips.BPass, flips.Shared), width)
	}
	if line, ok := quantDamageLine(a, b, flips); ok {
		fmt.Println("  " + terminalText(line))
	}
	if !completePairedPlan(a, b, flips) {
		render.Field(os.Stdout, "  behavior", pairedHang, fmt.Sprintf(
			"descriptive only: %d scorable shared instance(s); the sealed paired plan was incomplete or differed",
			flips.Shared), width)
		return
	}
	if flips.AOnly+flips.BOnly == 0 {
		fmt.Println("  identical outcomes on every shared instance - no evidence of any difference.")
		return
	}
	if !writePairedDirections(a, b, width) {
		fmt.Println("  item flips cancel within every need and family - no claimable direction.")
	}
}

func writePairedDirections(a, b *Result, width int) bool {
	anyDirection := false
	for _, stratum := range eval.NeedDirections(a.Checks, b.Checks) {
		discordantFamilies := stratum.AOnly + stratum.BOnly
		if discordantFamilies == 0 {
			continue
		}
		anyDirection = true
		label, code := pairedDirectionNames(stratum.Need)
		pExact, pMid, separable := stats.McNemarExact(stratum.AOnly, stratum.BOnly)
		if !separable {
			render.Field(os.Stdout, "  "+terminalText(code), pairedHang,
				fmt.Sprintf("%d discordant family direction(s); too few to separate at alpha=0.05",
					discordantFamilies), width)
			continue
		}
		writePairedDirection(a, b, stratum, label, code, pExact, pMid, width)
	}
	return anyDirection
}

func pairedDirectionNames(need string) (string, string) {
	label, code := need, need
	if named := score.NeedLabel[need]; named != "" {
		label = named
	}
	if named := score.NeedCode[need]; named != "" {
		code = named
	}
	return label, code
}

func writePairedDirection(a, b *Result, stratum eval.NeedDirectionStat, label, code string,
	pExact, pMid float64, width int) {
	winner := a.Model
	if stratum.BOnly > stratum.AOnly {
		winner = b.Model
	}
	if pExact < 0.05 {
		render.Field(os.Stdout, "  "+terminalText(code), pairedHang,
			fmt.Sprintf("%s separates %s family directions (exact sign p=%.3f, mid-p %.3f)",
				terminalText(winner), terminalText(label), pExact, pMid), width)
		return
	}
	render.Field(os.Stdout, "  "+terminalText(code), pairedHang,
		fmt.Sprintf("family directions do not separate them (exact sign p=%.3f, mid-p %.3f)",
			pExact, pMid), width)
}

func completePairedPlan(a, b *Result, flips eval.FlipReport) bool {
	if a == nil || b == nil || a.TaskPlan.CheckTrialsLimit <= 0 ||
		a.TaskPlan.CheckTrialsLimit != b.TaskPlan.CheckTrialsLimit ||
		a.TaskPlan.CheckPlanSHA256 == "" || a.TaskPlan.CheckPlanSHA256 != b.TaskPlan.CheckPlanSHA256 {
		return false
	}
	return flips.Shared == a.TaskPlan.CheckTrialsLimit
}
