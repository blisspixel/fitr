package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/blisspixel/fitr/internal/reference"
	"github.com/blisspixel/fitr/internal/render"
)

func cmdReferenceRun(args []string) int {
	fs := commandFlags("experiment reference run")
	planPath := fs.String("plan", "", "sealed plan")
	verifierPath := fs.String("verifier", "", "recorded verifier output")
	conditionsPath := fs.String("conditions", "", "operator declarations")
	out := fs.String("out", "", "optional copy of the result")
	display := fs.String("display", "auto", "auto|rich|plain|json|none")
	if code, ok := parseCommandFlags(fs, args); !ok {
		return code
	}
	if fs.NArg() != 0 || *planPath == "" || *verifierPath == "" || *conditionsPath == "" {
		errPrint("reference run needs --plan, --verifier, and --conditions",
			"the verifier is recorded output, not a command to launch",
			"fitr experiment reference run --plan plan.json --verifier verifier.json --conditions conditions.json")
		return exitUsage
	}
	if !render.ValidMode(*display) {
		errPrint("invalid display mode", *display, "use auto, rich, plain, json, or none")
		return exitUsage
	}
	return finishReferenceRun(*planPath, *verifierPath, *conditionsPath, *out, *display)
}

func finishReferenceRun(planPath, verifierPath, conditionsPath, out, display string) int {
	plan, conditions, code := loadReferenceRun(planPath, conditionsPath)
	if code != exitOK {
		return code
	}
	probed := liveProbe(conditions)
	if err := reference.Preflight(plan, probed); err != nil {
		errPrint("reference preflight refused: "+err.Error(), "no verifier file was accepted",
			"record a worker probe, an idle device, a served context at the cap, and a budget the sealed estimate fits")
		return exitError
	}
	result, err := acceptReferenceVerifier(plan, probed, verifierPath)
	if err != nil {
		errPrint("could not accept verifier output: "+err.Error(), verifierPath,
			"the file must match the sealed instance ids and repeats")
		return exitError
	}
	saved, err := saveReferenceResult(result, out)
	if err != nil {
		errPrint("could not write reference result: "+err.Error(), "", "")
		return exitError
	}
	return renderReferenceResult(result, display, saved)
}

func loadReferenceRun(planPath, conditionsPath string) (reference.Plan, reference.RunConditions, int) {
	planData, err := readBounded(planPath, reference.MaxPlanBytes)
	if err != nil {
		errPrint("could not read reference plan: "+err.Error(), planPath, "")
		return reference.Plan{}, reference.RunConditions{}, exitError
	}
	plan, err := reference.DecodePlan(planData)
	if err != nil {
		errPrint("reference plan is not sealed: "+err.Error(), planPath, "")
		return reference.Plan{}, reference.RunConditions{}, exitError
	}
	conditionData, err := readBounded(conditionsPath, reference.MaxConditionsBytes)
	if err != nil {
		errPrint("could not read reference conditions: "+err.Error(), conditionsPath, "")
		return reference.Plan{}, reference.RunConditions{}, exitError
	}
	declared, err := reference.DecodeConditions(conditionData)
	if err != nil {
		errPrint("reference conditions are incomplete: "+err.Error(), conditionsPath, "")
		return reference.Plan{}, reference.RunConditions{}, exitError
	}
	return plan, declared.AsRun(), exitOK
}

func acceptReferenceVerifier(plan reference.Plan, conditions reference.RunConditions, path string) (reference.Result, error) {
	data, err := readBounded(path, reference.MaxVerifierBytes)
	if err != nil {
		return reference.Result{}, err
	}
	rows, err := reference.DecodeVerifier(data)
	if err != nil {
		return reference.Result{}, err
	}
	return reference.Analyze(plan, conditions, rows)
}

func saveReferenceResult(result reference.Result, out string) (string, error) {
	digest, err := reference.ObservationDigest(result.Observations)
	if err != nil {
		return "", err
	}
	directory := filepath.Join(resultsDir(), ".experiments")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", err
	}
	name := "reference-" + shortReference(result.Plan.PlanSHA256) + "-" + shortReference(digest) + ".json"
	path := filepath.Join(directory, name)
	if err := writeReferenceJSON(path, result); err != nil {
		return "", err
	}
	if out != "" {
		if err := writeReferenceJSON(out, result); err != nil {
			return "", err
		}
	}
	return path, nil
}

func renderReferenceResult(result reference.Result, display, path string) int {
	if render.Resolve(display) == "none" {
		return exitOK
	}
	if render.Resolve(display) == "json" {
		return writeJSONValue(result)
	}
	referenceField("evidence", result.EvidenceClass)
	referenceField("fresh", "false")
	referenceField("clean", rateText(result.CleanRate))
	referenceField("contaminated", rateText(result.ContaminatedRate))
	referenceField("errors", errorText(result.Clean, result.Contaminated, result.Unmeasured))
	referenceField("file", path)
	referenceField("note", result.Note)
	return exitOK
}

func rateText(rate reference.Rate) string {
	if rate.Status != "measured" || rate.Estimate == nil || rate.Lo == nil || rate.Hi == nil {
		return "unmeasured"
	}
	return fmt.Sprintf("%d/%d resolved, Wilson [%.4f, %.4f]", rate.Resolved, rate.Denominator, *rate.Lo, *rate.Hi)
}

func errorText(parts ...reference.Counts) string {
	var harness, environment, timeout int
	for _, part := range parts {
		harness += part.HarnessError
		environment += part.EnvironmentError
		timeout += part.Timeout
	}
	return fmt.Sprintf("harness %d, environment %d, timeout %d; excluded from the rate", harness, environment, timeout)
}

func cmdReferenceFiles(args []string) int {
	display := "auto"
	paths := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		if args[i] == "--display" {
			if i+1 >= len(args) {
				errPrint("missing display mode", "", "use auto, rich, plain, json, or none")
				return exitUsage
			}
			display = args[i+1]
			i++
			continue
		}
		if strings.HasPrefix(args[i], "-") {
			errPrint("unknown reference flag", args[i], "fitr experiment reference --help")
			return exitUsage
		}
		paths = append(paths, args[i])
	}
	if !render.ValidMode(display) || len(paths) < 1 || len(paths) > 2 {
		errPrint("reference comparison needs one or two result files", "", "fitr experiment reference <result.json> [result.json]")
		return exitUsage
	}
	return renderReferenceFiles(paths, display)
}

func renderReferenceFiles(paths []string, display string) int {
	results := make([]reference.Result, 0, len(paths))
	for _, path := range paths {
		result, code := readReferenceResult(path)
		if code != exitOK {
			return code
		}
		results = append(results, result)
	}
	if len(results) == 1 {
		return renderReferenceResult(results[0], display, paths[0])
	}
	pair, err := reference.Compare(results[0], results[1])
	if err != nil {
		errPrint("could not compare reference results: "+err.Error(), "", "")
		return exitError
	}
	if render.Resolve(display) == "json" {
		return writeJSONValue(pair)
	}
	if render.Resolve(display) == "none" {
		return exitOK
	}
	referenceField("paired", reference.FormatPair(pair))
	return exitOK
}

func readReferenceResult(path string) (reference.Result, int) {
	data, err := readBounded(path, reference.MaxResultBytes)
	if err != nil {
		errPrint("could not read reference result: "+err.Error(), path, "")
		return reference.Result{}, exitError
	}
	result, err := reference.DecodeResult(data)
	if err != nil {
		errPrint("reference result does not match its observations: "+err.Error(), path, "")
		return reference.Result{}, exitError
	}
	return result, exitOK
}

func referenceBoardNotes() []string {
	matches, err := filepath.Glob(filepath.Join(resultsDir(), ".experiments", "reference-*.json"))
	if err != nil || len(matches) == 0 {
		return nil
	}
	var results []reference.Result
	unread := 0
	for _, path := range matches {
		result, ok := quietReferenceResult(path)
		if !ok {
			unread++
			continue
		}
		results = append(results, result)
	}
	notes := reference.SeriesNotes(results)
	if unread > 0 {
		notes = append(notes, fmt.Sprintf("external reference: %d file(s) could not be read", unread))
	}
	return notes
}

func quietReferenceResult(path string) (reference.Result, bool) {
	info, err := os.Stat(path)
	if err != nil || info.Size() > reference.MaxResultBytes {
		return reference.Result{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return reference.Result{}, false
	}
	result, err := reference.DecodeResult(data)
	if err != nil {
		return reference.Result{}, false
	}
	return result, true
}

func writeReferenceNoteLines(notes []string) {
	for _, note := range notes {
		referenceField("reference", note)
	}
}
