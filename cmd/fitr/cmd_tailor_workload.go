package main

import (
	"flag"
	"strconv"
	"strings"

	"github.com/blisspixel/fitr/internal/fitting"
)

type tokenList []string

func (v *tokenList) String() string { return strings.Join(*v, ",") }
func (v *tokenList) Set(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return errToken
	}
	*v = append(*v, value)
	return nil
}

var errToken = errString("empty flag value")

type errString string

func (e errString) Error() string { return string(e) }

func bindTailorWorkloadFlags(fs *flag.FlagSet, flags *tailorPlanFlags) {
	fs.StringVar(&flags.workload, "workload", "", "preset that expands into visible requirements")
	fs.IntVar(&flags.desired, "desired-context", 0, "desired context in tokens")
	fs.IntVar(&flags.minimum, "minimum-context", 0, "minimum acceptable context in tokens")
	fs.StringVar(&flags.meaning, "context-meaning", "", "total-window or usable-input")
	fs.StringVar(&flags.harness, "harness", "", "harness name; hermes keeps a 64000 token floor")
	fs.StringVar(&flags.harnessVersion, "harness-version", "", "harness version, when the operator knows it")
	fs.IntVar(&flags.harnessMin, "harness-min-context", 0, "harness minimum context in tokens")
	fs.IntVar(&flags.reserveSystem, "reserve-system-tokens", 0, "reserved system and tool tokens")
	fs.IntVar(&flags.reserveOutput, "reserve-output-tokens", 0, "reserved output tokens")
	fs.IntVar(&flags.reserveReasoning, "reserve-reasoning-tokens", 0, "reserved reasoning tokens")
	fs.StringVar(&flags.weightQuant, "weight-quant", "", "declared weight quantization, such as Q4_K_M")
	fs.StringVar(&flags.weightPreference, "weight-preference", "", "weight preference, such as Q4-or-better")
	fs.StringVar(&flags.weightMinimum, "weight-minimum", "", "hard minimum weight quantization")
	fs.Var(&flags.permittedKV, "kv-permitted", "KV cache type this test may use; repeat for each")
	fs.Var(&flags.alternatives, "alternative-ctx", "predeclared alternative context; repeat for each")
	fs.StringVar(&flags.modelCard, "model-card", "", "labeled model card to show, such as qwen3.6-27b")
	fs.StringVar(&flags.projected, "projected-resident-gb", "", "projected resident GiB to compare with the policy")
	fs.IntVar(&flags.concurrency, "concurrency", 0, "declared concurrency; it is not measured here")
	fs.BoolVar(&flags.populated, "require-populated-context", false, "require populated token-accounted context")
}

func tailorContextMeaning(flags tailorPlanFlags) (int, bool) {
	goal := flags.desired > 0 || flags.minimum > 0 || flags.reserveSystem > 0 || flags.reserveOutput > 0 || flags.reserveReasoning > 0
	if !goal {
		return exitOK, true
	}
	if flags.meaning == fitting.ContextTotalWindow || flags.meaning == fitting.ContextUsableInput {
		return exitOK, true
	}
	return tailorQuestion("Does this context mean the total window or the usable input?", "A harness floor, a vendor recommendation, and the desired working context are different numbers.", "pass --context-meaning total-window or --context-meaning usable-input"), false
}

func applyTailorWorkload(flags tailorPlanFlags, req *fitting.Request) error {
	var projected int64
	if flags.projected != "" {
		parsed, err := tailorGiB(flags.projected, true)
		if err != nil {
			return err
		}
		projected = parsed
	}
	alternatives, err := tokenInts(flags.alternatives, "alternative context")
	if err != nil {
		return err
	}
	req.ModelCard = flags.modelCard
	req.AlternativeContexts = alternatives
	req.KVClientEnv = ""
	req.KVServerReport = ""
	req.Workload = fitting.Workload{
		Preset: flags.workload, DesiredContextTokens: flags.desired, MinimumContextTokens: flags.minimum,
		ContextMeaning: flags.meaning, Harness: flags.harness, HarnessVersion: flags.harnessVersion,
		HarnessMinContextTokens: flags.harnessMin, ReservedSystemTokens: flags.reserveSystem,
		ReservedOutputTokens: flags.reserveOutput, ReservedReasoningTokens: flags.reserveReasoning,
		WeightQuant: flags.weightQuant, WeightPreference: flags.weightPreference, WeightMinimum: flags.weightMinimum,
		KVPermitted: []string(flags.permittedKV), RequirePopulatedContext: flags.populated,
		Concurrency: flags.concurrency, ProjectedResidentBytes: projected,
	}
	return nil
}

func tokenInts(values tokenList, label string) ([]int, error) {
	if len(values) == 0 {
		return nil, nil
	}
	out := make([]int, len(values))
	for i, value := range values {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed <= 0 {
			return nil, errString(label + " must be a positive token count")
		}
		out[i] = parsed
	}
	return out, nil
}
