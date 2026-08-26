package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
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
	fs := flag.NewFlagSet("view", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	viewFull := fs.Bool("full", false, "with --display json, emit the complete sealed result record instead of the scorecard")
	mode := fs.String("display", "auto", "auto|rich|plain|json|none")
	if code, ok := parseCommandFlags(fs, args); !ok {
		return code
	}
	if !render.ValidMode(*mode) {
		errPrint("invalid display mode", *mode, "use auto, rich, plain, json, or none")
		return exitUsage
	}
	if fs.NArg() > 1 {
		errPrint("too many arguments", "view accepts one model or result path", "fitr view [model|result.json]")
		return exitUsage
	}

	var selected *Result
	if fs.NArg() == 1 {
		candidate := fs.Arg(0)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			r, err := record.NewStore(resultsDir()).Read(candidate)
			if err != nil {
				errPrint("not a fitr result", "expected a saved run JSON with a model", candidate)
				return exitError
			}
			selected = r
		} else {
			results, err := loadResults()
			if err != nil || len(results) == 0 {
				errPrint("no results yet", "", "fitr run "+normalizeModelRef(candidate))
				return exitError
			}
			selected = latestNamed(results, normalizeModelRef(candidate))
			if selected == nil {
				errPrint(fmt.Sprintf("no stored result for %q", candidate), "", "fitr board")
				return exitError
			}
		}
	} else {
		results, err := loadResults()
		if err != nil || len(results) == 0 {
			errPrint("no results yet", "", "run one first: fitr run <model>")
			return exitError
		}
		for _, result := range results {
			if selected == nil || startedAfter(result.StartedAt, selected.StartedAt) {
				selected = result
			}
		}
	}

	switch render.Resolve(*mode) {
	case "none":
		return exitOK
	case "json":
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		// The sealed record carries per-trial evidence and the run manifest,
		// which is what `--full` is for. What a reader of `view` wants is the
		// scorecard the command prints.
		var payload any = selected
		if !*viewFull {
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
	scorecard := selected.Scorecard
	meta := resultMeta(selected, selected.Profile)
	if artifact, err := artifactFrom(selected); err == nil {
		scorecard, meta = artifact.Scorecard, artifact.Meta
	} else {
		scorecard = score.ExcludeEvidence(scorecard,
			"the scoring profile is unavailable, so the stored verdict cannot be reproduced")
	}
	display := render.New(*mode)
	defer display.Close()
	display.Result(scorecard, meta)
	return exitOK
}

// ---------------------------------------------------------------- board
func cmdBoard(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("board", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	current := fs.Bool("current", false, "only this machine, including its measured context variants")
	full := fs.Bool("full", false, "with --display json, emit the complete sealed result records instead of the reported columns")
	mode := fs.String("display", "auto", "auto|rich|plain|json|none")
	if code, ok := parseCommandFlags(fs, args); !ok {
		return code
	}
	if !render.ValidMode(*mode) {
		errPrint("invalid display mode", *mode, "use auto, rich, plain, json, or none")
		return exitUsage
	}
	if fs.NArg() != 0 {
		errPrint("unexpected argument", fs.Arg(0), "fitr board [--current] [--display MODE]")
		return exitUsage
	}
	results, err := loadResults()
	if err != nil || len(results) == 0 {
		errPrint("no results yet", "", "run one first: fitr run <model>")
		return exitError
	}
	curDevice := device.Detect(ctx, probeBackend(ctx))
	cur := curDevice.Key()

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
		if *current && !samePhysicalMachine(r.Device, curDevice) {
			continue
		}
		if _, ok := groups[key]; !ok {
			order = append(order, key)
		}
		groups[key] = append(groups[key], r)
	}
	sort.Strings(order)

	board := render.Board{}
	excludedContaminated := 0
	excludedUnverified := 0
	unscoredProfiles := 0
	visible := map[string][]*Result{}
	for _, key := range order {
		rows := groups[key]
		clean := make([]*Result, 0, len(rows))
		for _, result := range rows {
			if len(result.Contamination) > 0 {
				excludedContaminated++
				continue
			}
			if result.EvidenceIntegrityIssue() != "" {
				excludedUnverified++
				continue
			}
			clean = append(clean, result)
		}
		if len(clean) == 0 {
			continue
		}
		rows = clean
		g := rows[len(rows)-1]
		nctx := resultNumCtx(g)
		note := "different hardware/config or effective context; not comparable to other blocks"
		if samePhysicalMachine(g.Device, curDevice) {
			if nctx != eval.NumCtx {
				note = fmt.Sprintf("this machine, requested num_ctx=%d; effective context is part of this block", nctx)
			} else {
				note = "this machine, verified effective context and current config"
			}
		}
		sort.Slice(rows, func(i, j int) bool {
			return rows[i].DecodeSum.Mean > rows[j].DecodeSum.Mean
		})
		visible[key] = rows
		group := render.BoardGroup{
			GPU: g.Device.GPU, Driver: g.Device.GPUDriver,
			KV: g.Device.Config["OLLAMA_KV_CACHE_TYPE"], NumCtx: nctx, Note: note,
		}
		if g.DeviceV2 != nil {
			group.ContextState = string(g.DeviceV2.Context.State())
			if g.DeviceV2.Context.EffectiveTokens != nil {
				group.EffectiveCtx = *g.DeviceV2.Context.EffectiveTokens
			}
		}
		for _, r := range rows {
			var codes []string
			scorecard := r.Scorecard
			if artifact, err := artifactFrom(r); err == nil {
				scorecard = artifact.Scorecard
			} else {
				scorecard = score.ExcludeEvidence(scorecard,
					"the scoring profile is unavailable, so the stored verdict cannot be reproduced")
				unscoredProfiles++
			}
			for _, s := range scorecard.Serves {
				if code := score.NeedCode[s]; code != "" {
					codes = append(codes, code)
				}
			}
			var decodes []float64
			for _, sample := range r.Speed {
				decodes = append(decodes, sample.DecodeTPS)
			}
			group.Rows = append(group.Rows, render.BoardRow{
				Model: r.Model, ParamSize: r.ModelMeta.Details.ParameterSize,
				Quant:      r.ModelMeta.Details.QuantizationLevel,
				DecodeMean: r.DecodeSum.Mean, DecodeSD: r.DecodeSum.SD,
				PrefillMean: r.PrefillSum.Mean, ResidentGB: r.Memory.ResidentGB,
				DecodeSeries: decodes, Repeats: r.Repeats, Serves: codes,
			})
		}
		board.Results += len(rows)
		board.Groups = append(board.Groups, group)
	}
	if excludedContaminated > 0 {
		fmt.Fprintf(os.Stderr, "! INCONCLUSIVE: excluded %d contaminated result(s) from board ranking and claims\n",
			excludedContaminated)
	}
	if excludedUnverified > 0 {
		fmt.Fprintf(os.Stderr, "! INCONCLUSIVE: excluded %d result(s) without a valid evidence contract from board ranking and claims\n",
			excludedUnverified)
	}
	if excludedContext > 0 {
		fmt.Fprintf(os.Stderr, "! INCONCLUSIVE: excluded %d result(s) without verified effective context from board ranking and claims\n",
			excludedContext)
	}
	if unscoredProfiles > 0 {
		fmt.Fprintf(os.Stderr, "! INCONCLUSIVE: %d board row(s) have no reproducible qualification because their scoring profile is unavailable\n",
			unscoredProfiles)
	}
	if len(board.Groups) == 0 {
		if excludedContaminated > 0 || excludedUnverified > 0 || excludedContext > 0 {
			detail := "all matching results lacked claimable evidence"
			if excludedContaminated > 0 && excludedUnverified == 0 && excludedContext == 0 {
				detail = "all matching results were contaminated"
			} else if excludedUnverified > 0 && excludedContaminated == 0 && excludedContext == 0 {
				detail = "all matching results lacked a valid evidence contract"
			} else if excludedContext > 0 && excludedContaminated == 0 && excludedUnverified == 0 {
				detail = "all matching results lacked verified effective context"
			}
			errPrint("no conclusive results for this machine", detail,
				"re-run with the current fitr version after unloading all models")
		} else {
			errPrint("no results for this machine", "", "run fitr run <model>")
		}
		return exitError
	}
	if render.Resolve(*mode) == "json" {
		// Default to what the board reports, not the sealed records behind it.
		// The records are an order of magnitude larger, grow without bound in
		// the number of models, and none of the extra fields appear on screen.
		payload := map[string]any{
			"schema": "fitr.board.v1", "current": cur,
			"groups": board.Groups, "results": board.Results,
		}
		if *full {
			payload["groups"] = visible
			payload["schema"] = "fitr.board.full.v1"
		}
		if excludedContaminated > 0 {
			payload["inconclusive_excluded"] = excludedContaminated
		}
		if excludedUnverified > 0 {
			payload["unverified_excluded"] = excludedUnverified
		}
		if excludedContext > 0 {
			payload["context_unverified_excluded"] = excludedContext
		}
		b, _ := json.Marshal(payload)
		fmt.Println(string(b))
		return exitOK
	}
	render.WriteBoard(os.Stdout, board, *mode)
	return exitOK
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
	results, err := loadResults()
	if err != nil {
		errPrint("no results", "", "fitr run <model>")
		return exitError
	}
	a, b := latestNamed(results, args[0]), latestNamed(results, args[1])
	for i, r := range []*Result{a, b} {
		if r == nil {
			errPrint(fmt.Sprintf("no stored result for %q", args[i]), "",
				"fitr run "+args[i])
			return exitError
		}
	}
	if len(a.Contamination) > 0 || len(b.Contamination) > 0 {
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
	if issueA, issueB := a.EvidenceIntegrityIssue(), b.EvidenceIntegrityIssue(); issueA != "" || issueB != "" {
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
	if err := record.ProvenanceCompatibilityError(a, b); err != nil {
		fmt.Printf("  %s  vs  %s\n\n", terminalText(a.Model), terminalText(b.Model))
		fmt.Println("  INCONCLUSIVE  the runs used different task, profile, specification, scoring, or protocol provenance")
		fmt.Printf("  detail       %s\n", terminalText(err.Error()))
		fmt.Println("  remedy       compare runs produced by the same fitr battery and effective profile")
		return exitError
	}
	aKey, aKeyErr := a.ComparableDeviceKey()
	bKey, bKeyErr := b.ComparableDeviceKey()
	if aKeyErr != nil || bKeyErr != nil {
		fmt.Printf("  %s  vs  %s\n\n", terminalText(a.Model), terminalText(b.Model))
		fmt.Println("  INCONCLUSIVE  verified effective context is required for comparison")
		for i, keyErr := range []error{aKeyErr, bKeyErr} {
			if keyErr == nil {
				continue
			}
			result := []*Result{a, b}[i]
			fmt.Printf("  %-12s %s\n", terminalText(result.Model)+":", terminalText(keyErr.Error()))
		}
		fmt.Println("  remedy       re-run both models on a runtime that reports its allocated context")
		return exitError
	}
	if aKey != bKey {
		if a.DeviceV2 != nil && b.DeviceV2 != nil && reflect.DeepEqual(a.DeviceV2.Device, b.DeviceV2.Device) {
			aEffective, bEffective := 0, 0
			if a.DeviceV2.Context.EffectiveTokens != nil {
				aEffective = *a.DeviceV2.Context.EffectiveTokens
			}
			if b.DeviceV2.Context.EffectiveTokens != nil {
				bEffective = *b.DeviceV2.Context.EffectiveTokens
			}
			errPrint("these results used different request context",
				fmt.Sprintf("requested %d vs %d, effective %d vs %d; tok/s and quality both move with KV size",
					resultNumCtx(a), resultNumCtx(b), aEffective, bEffective),
				"compare two runs at the same --ctx, or re-measure")
			return exitError
		}
		// Naming the machine is misleading when both runs happened on it: the
		// usual cause is that its STATE moved between them -- a model resident
		// from something else, so one run was partly offloaded and the other
		// was not. Fingerprint.Diff already knows exactly which fields moved,
		// so say them instead of sending the operator to re-measure blind.
		note := incomparableNote(a.Device.Diff(b.Device))
		errPrint("these results were not measured under the same conditions", note,
			"re-measure both with the machine in the same state, then compare")
		return exitError
	}

	fmt.Printf("  %s  vs  %s\n\n", terminalText(a.Model), terminalText(b.Model))

	// Throughput: Fieller's interval is the correct one for "how many times
	// faster". When it cannot be computed honestly (single observation, or
	// denominator not separated from zero), the ratio prints without an
	// interval and therefore without a verdict.
	for _, p := range []struct {
		label string
		x, y  stats.Summary
	}{
		{"decode tok/s", a.DecodeSum, b.DecodeSum},
		{"prefill tok/s", a.PrefillSum, b.PrefillSum},
	} {
		lo, hi, ratio, ok := stats.FiellerRatio(p.x, p.y)
		if ok {
			verdict := "cannot separate"
			if lo > 1 {
				verdict = "first is faster"
			} else if hi < 1 {
				verdict = "second is faster"
			}
			fmt.Printf("  %-21s %7.2f vs %7.2f  %5.2fx [%.2f-%.2f]  %s\n",
				p.label, p.x.Mean, p.y.Mean, ratio, lo, hi, verdict)
		} else {
			ratio, _, _ := stats.RatioWithError(p.x, p.y)
			fmt.Printf("  %-21s %7.2f vs %7.2f  %5.2fx  no interval - too little data\n",
				p.label, p.x.Mean, p.y.Mean, ratio)
		}
	}
	fmt.Println()

	// Pass rates: the Newcombe difference interval is the sole arbiter. A
	// difference is claimed if and only if the interval excludes zero.
	for _, need := range []string{"coding", "structured_output", "instruction_precision", "user_tasks"} {
		pa, pb := poolOf(a, need), poolOf(b, need)
		if pa.N == 0 || pb.N == 0 {
			continue
		}
		lo, hi, ok := stats.NewcombeDiff(pa.Passes, pa.N, pb.Passes, pb.N)
		if !ok {
			continue
		}
		d := float64(pa.Passes)/float64(pa.N) - float64(pb.Passes)/float64(pb.N)
		verdict := "cannot separate"
		if lo > 0 {
			verdict = "first is better"
		} else if hi < 0 {
			verdict = "second is better"
		}
		fmt.Printf("  %-21s %d/%d vs %d/%d  %+.2f [%+.2f..%+.2f]  %s\n",
			need, pa.Passes, pa.N, pb.Passes, pb.N, d, lo, hi, verdict)
	}

	// Paired analysis: when both runs faced IDENTICAL generated instances,
	// the item-level flips carry far more information than the two rates.
	fmt.Println()
	if a.SeedSet != "" && a.SeedSet == b.SeedSet {
		pairedCompare(a, b)
	} else if len(a.Checks) > 0 && len(b.Checks) > 0 {
		fmt.Println("  unpaired: the runs faced different generated instances.")
		fmt.Printf("  for a sharper paired test:  fitr run <model> --seedset shared1  (both models)\n")
	}

	fmt.Println("\n  note  a difference is claimed only when its 95% interval excludes zero;")
	fmt.Println("        \"cannot separate\" is a real answer, not a missing one.")
	return exitOK
}

// pairedHang aligns the paired block's wrapped text under its own label.
const pairedHang = 11

// pairedCompare runs McNemar's exact test on instances both models faced.
// Concordant instances carry no information about the difference; only the
// flips decide, and with fewer than six of them no split can reach p<0.05 -
// which is reported as exactly that, never as a near-miss.
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
	switch flips.AOnly + flips.BOnly {
	case 0:
		fmt.Println("  identical outcomes on every shared instance - no evidence of any difference.")
	default:
		pExact, pMid, separable := stats.McNemarExact(flips.AOnly, flips.BOnly)
		if !separable {
			fmt.Printf("  %d discordant instance(s) - too few to separate at alpha=0.05 regardless of split.\n",
				flips.AOnly+flips.BOnly)
			return
		}
		winner := a.Model
		if flips.BOnly > flips.AOnly {
			winner = b.Model
		}
		if pExact < 0.05 {
			fmt.Printf("  %s wins the flips (McNemar exact p=%.3f, mid-p %.3f)\n", terminalText(winner), pExact, pMid)
		} else {
			fmt.Printf("  ~ the flips do not separate them (McNemar exact p=%.3f, mid-p %.3f)\n", pExact, pMid)
		}
	}
}

// poolOf rebuilds the per-need trial pools from a stored result. coding pools
// the executed code tasks with the generated reasoning checks, mirroring the
// scorecard.
func poolOf(r *Result, need string) (p score.Pool) {
	if need == "coding" {
		for _, x := range append(append([]eval.ExecResult{}, r.CodeWrite...), r.CodeFix...) {
			pass, measured := eval.MeasuredOutcome(x.Outcome, x.Pass)
			if !measured {
				continue
			}
			p.N++
			if pass {
				p.Passes++
			}
		}
	}
	for _, ck := range r.Checks {
		match := ck.Need == need || (need == "coding" && ck.Need == "reasoning")
		if !match {
			continue
		}
		pass, measured := eval.MeasuredOutcome(ck.Outcome, ck.Pass)
		if !measured {
			continue
		}
		p.N++
		if pass {
			p.Passes++
		}
	}
	return p
}
