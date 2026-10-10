package desktop

import (
	"context"
	"regexp"
	"strconv"
	"strings"

	"github.com/blisspixel/fitr/internal/analysis"
)

const BenchmarkSchema = "fitr.desktop.benchmark.v1"

const ambientRouteReason = "Ambient OpenAI configuration, or a runtime URL that is not loopback, is present, so the desktop will not start this run."

// BenchmarkPlan is the execution gate for one explicit measurement. Eligible
// is recomputed from the status document and the process environment. A caller
// must not start a run from a QML effect bit without this plan.
type BenchmarkPlan struct {
	Schema   string   `json:"schema"`
	Eligible bool     `json:"eligible"`
	Started  bool     `json:"started"`
	Argv     []string `json:"argv,omitempty"`
	Reason   string   `json:"reason"`
}

// PlanBenchmark allows one fitr run whose argv, model, protocol and
// environment all say the measurement stays on a local runtime. Anything else
// is reported and not started.
func PlanBenchmark(status Status, environ []string) BenchmarkPlan {
	plan := BenchmarkPlan{Schema: BenchmarkSchema, Reason: "The desktop did not start a measurement."}
	if status.Schema != Schema || !status.ReadOnly {
		plan.Reason = "The status document is not a desktop status, so the desktop did not start a measurement."
		return plan
	}
	if ambientRoute(environ) {
		plan.Reason = ambientRouteReason
		return plan
	}
	if status.Next.Effect != EffectExplicitLocal || !status.Next.LocalProven || !allowLocalRun(status.Next.Argv, status.Model) {
		plan.Reason = "No proven local measurement is available, so the desktop did not start one."
		return plan
	}
	plan.Eligible = true
	plan.Argv = append([]string(nil), status.Next.Argv...)
	plan.Reason = "The measurement is an allowlisted local run. It starts only when --confirm is set."
	return plan
}

// ExecuteBenchmark starts the plan only when confirm is set and the argv still
// matches the local allowlist. The runner receives that argv and nothing else.
func ExecuteBenchmark(ctx context.Context, plan BenchmarkPlan, confirm bool, run func(context.Context, []string) int) (bool, int) {
	if !confirm || !plan.Eligible || len(plan.Argv) < 3 || !allowLocalRun(plan.Argv, plan.Argv[2]) {
		return false, 0
	}
	if run == nil || (ctx != nil && ctx.Err() != nil) {
		return false, 0
	}
	argv := append([]string(nil), plan.Argv...)
	return true, run(ctx, argv)
}

// allowLocalRun accepts only the stock local measurement shape produced by
// analysis: fitr run, one resolved model, context, ollama or llama-server, a
// profile token, and an optional small repeat count. A slash would admit a
// Hugging Face alias, which fitr run can pull. Checked against cmd/fitr run
// flags on 2026-10-09: --pull is the only pull switch and it is not accepted
// here.
func allowLocalRun(argv []string, model string) bool {
	if !safeModel(model) || (len(argv) != 9 && len(argv) != 11) {
		return false
	}
	if argv[0] != "fitr" || argv[1] != "run" || argv[2] != model || argv[3] != "--ctx" || !positiveInt(argv[4], 1, 16777216) {
		return false
	}
	if argv[5] != "--backend" || (argv[6] != "ollama" && argv[6] != "llama-server") || argv[7] != "--profile" || !safeProfile(argv[8]) {
		return false
	}
	if len(argv) == 11 && (argv[9] != "-k" || !positiveInt(argv[10], 1, 99)) {
		return false
	}
	for _, arg := range argv {
		if arg == analysis.CurrentModelPlaceholder || strings.Contains(arg, "://") || strings.Contains(strings.ToLower(arg), "http") {
			return false
		}
		if arg == "--pull" || arg == "--endpoint" || strings.HasPrefix(arg, "--endpoint=") {
			return false
		}
	}
	return true
}

var modelToken = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
var profileToken = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

func safeModel(model string) bool {
	if !modelToken.MatchString(model) || strings.Contains(model, "/") || strings.Contains(model, `\`) || strings.Contains(model, "..") {
		return false
	}
	lower := strings.ToLower(model)
	return !strings.Contains(lower, "http") && !strings.Contains(model, "://")
}

func safeProfile(profile string) bool {
	return profileToken.MatchString(profile) && !strings.ContainsAny(profile, `/\:`)
}

func positiveInt(token string, minimum, maximum int) bool {
	if token == "" || token[0] < '1' || token[0] > '9' {
		return false
	}
	value, err := strconv.Atoi(token)
	if err != nil || value < minimum || value > maximum {
		return false
	}
	return strconv.Itoa(value) == token
}

func formatArgv(argv []string) string {
	if len(argv) == 0 {
		return ""
	}
	parts := make([]string, len(argv))
	for index, arg := range argv {
		if arg == "" || strings.ContainsAny(arg, " \t\n\"'\\") {
			parts[index] = strconv.Quote(arg)
			continue
		}
		parts[index] = arg
	}
	return strings.Join(parts, " ")
}
