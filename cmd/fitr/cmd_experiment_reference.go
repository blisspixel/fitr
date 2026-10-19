package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/blisspixel/fitr/internal/atomicfile"
	"github.com/blisspixel/fitr/internal/isolate"
	"github.com/blisspixel/fitr/internal/reference"
	"github.com/blisspixel/fitr/internal/render"
)

const referenceUsage = `usage:
  fitr experiment reference plan --images digests.json --out plan.json
  fitr experiment reference run --plan plan.json --verifier verifier.json --conditions conditions.json --out result.json
  fitr experiment reference <result.json> [result.json]

plan seals the frozen SWE-rebench instance ids before any verifier file is read.
run refuses drift from that plan. It also refuses, before it reads the verifier,
when the worker probe is unavailable, the sealed estimate does not fit the
declared budget, served context is below the cap, the GPU is declared busy, or
another model is declared resident. This command does not launch a harness, a
container, or a model. A recorded verifier file is not fresh evidence.
`

func cmdExperimentReference(args []string) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		writeAskedHelp(os.Stdout, referenceUsage)
		return exitOK
	}
	switch args[0] {
	case "plan":
		return cmdReferencePlan(args[1:])
	case "run":
		return cmdReferenceRun(args[1:])
	default:
		return cmdReferenceFiles(args)
	}
}

func cmdReferencePlan(args []string) int {
	fs := commandFlags("experiment reference plan")
	images := fs.String("images", "", "sha256 digests for the frozen instance ids")
	name := fs.String("harness-name", "", "harness identifier, such as mini-swe-agent")
	version := fs.String("harness-version", "", "harness version being sealed")
	config := fs.String("harness-config", "", "sha256 of the harness config")
	steps := fs.Int("steps", 0, "step cap")
	taskWall := fs.Int("task-wall", 0, "per-task wall cap in seconds")
	ctxLimit := fs.Int("ctx", 0, "context cap in tokens")
	repeats := fs.Int("k", 0, "repeats, chosen before outcomes")
	seed := fs.String("seedset", "", "32 lowercase hex seed set")
	wall := fs.Int64("wall-budget", 0, "wall budget in seconds")
	disk := fs.Int64("disk-budget", 0, "disk budget in bytes")
	required := fs.Int64("required-disk", 0, "declared image bytes")
	prefill := fs.Int64("prefill", 0, "measured prefill tokens per second")
	decode := fs.Int64("decode", 0, "measured decode tokens per second")
	prompt := fs.Int64("prompt-tokens", 0, "sealed prompt-token estimate")
	completion := fs.Int64("completion-tokens", 0, "sealed completion-token estimate")
	artifact := fs.String("artifact", "", "sha256 of the artifact")
	release := fs.String("artifact-release", "", "declared artifact release date")
	quant := fs.String("quant", "", "quant identifier")
	runtimeName := fs.String("runtime", "", "runtime identifier")
	runtimeBuild := fs.String("runtime-build", "", "runtime build identifier")
	deviceKey := fs.String("device-key", "", "sha256 comparability key")
	out := fs.String("out", "", "plan path")
	display := fs.String("display", "auto", "auto|rich|plain|json|none")
	if code, ok := parseCommandFlags(fs, args); !ok {
		return code
	}
	if fs.NArg() != 0 || *images == "" || *out == "" {
		errPrint("reference plan needs --images and --out", "image digests are supplied, not fetched",
			"fitr experiment reference plan --images digests.json --out plan.json")
		return exitUsage
	}
	if !render.ValidMode(*display) {
		errPrint("invalid display mode", *display, "use auto, rich, plain, json, or none")
		return exitUsage
	}
	return sealReferencePlan(referencePlanInput{
		images: *images, name: *name, version: *version, config: *config, seed: *seed, out: *out,
		display: *display, artifact: *artifact, release: *release, quant: *quant, runtimeName: *runtimeName,
		runtimeBuild: *runtimeBuild, deviceKey: *deviceKey, steps: *steps, taskWall: *taskWall,
		ctxLimit: *ctxLimit, repeats: *repeats, wall: *wall, disk: *disk, required: *required,
		prefill: *prefill, decode: *decode, prompt: *prompt, completion: *completion,
	})
}

type referencePlanInput struct {
	images, name, version, config, seed, out, display              string
	artifact, release, quant, runtimeName, runtimeBuild, deviceKey string
	steps, taskWall, ctxLimit, repeats                             int
	wall, disk, required, prefill, decode, prompt, completion      int64
}

func sealReferencePlan(input referencePlanInput) int {
	instances, err := readReferenceImages(input.images)
	if err != nil {
		errPrint("could not read image digests: "+err.Error(), input.images, "pass one sha256 digest per frozen instance")
		return exitError
	}
	plan, err := reference.NewPlan(planDraft(input, instances))
	if err != nil {
		errPrint("could not seal reference plan: "+err.Error(), "", "fix the declared bounds and retry")
		return exitError
	}
	if err := writeReferenceJSON(input.out, plan); err != nil {
		errPrint("could not write reference plan: "+err.Error(), input.out, "")
		return exitError
	}
	return renderReferencePlan(plan, input.display, input.out)
}

func planDraft(input referencePlanInput, instances []reference.Instance) reference.Plan {
	return reference.Plan{
		Dataset: reference.DatasetID, DatasetRevision: reference.DatasetRevision,
		DatasetSplit: reference.DatasetSplit, SubsetID: reference.SubsetID, Instances: instances,
		HarnessName: input.name, HarnessVersion: input.version, HarnessConfigSHA256: input.config,
		StepLimit: input.steps, TaskWallSeconds: input.taskWall, ContextCap: input.ctxLimit, K: input.repeats,
		SeedSet: input.seed, WallBudgetSeconds: input.wall, DiskBudgetBytes: input.disk,
		RequiredDiskBytes: input.required, PrefillTokensPerSecond: input.prefill,
		DecodeTokensPerSecond: input.decode, PromptTokens: input.prompt, CompletionTokens: input.completion,
		ArtifactSHA256: input.artifact, ArtifactRelease: input.release, Quant: input.quant,
		Runtime: input.runtimeName, RuntimeBuild: input.runtimeBuild, DeviceKey: input.deviceKey,
	}
}

func readReferenceImages(path string) ([]reference.Instance, error) {
	data, err := readBounded(path, reference.MaxImagesBytes)
	if err != nil {
		return nil, err
	}
	return reference.ApplyImages(data)
}

func readBounded(path string, limit int) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Size() > int64(limit) {
		return nil, fmt.Errorf("file exceeds %d bytes", limit)
	}
	return os.ReadFile(path)
}

func writeReferenceJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return atomicfile.Write(path, data, 0o600)
}

func renderReferencePlan(plan reference.Plan, display, path string) int {
	if render.Resolve(display) == "none" {
		return exitOK
	}
	if render.Resolve(display) == "json" {
		return writeJSONValue(plan)
	}
	width := render.Width()
	render.Field(os.Stdout, "  plan", 16, plan.PlanSHA256, width)
	render.Field(os.Stdout, "  manifest", 16, plan.ManifestSHA256, width)
	render.Field(os.Stdout, "  instances", 16, strconv.Itoa(len(plan.Instances)), width)
	render.Field(os.Stdout, "  file", 16, path, width)
	fmt.Fprintln(os.Stdout, "  note: this plan does not launch a harness or a container")
	return exitOK
}

func writeJSONValue(value any) int {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		errPrint("could not render external reference: "+err.Error(), "", "")
		return exitError
	}
	return exitOK
}

func liveProbe(conditions reference.RunConditions) reference.RunConditions {
	probe := isolate.Probe()
	conditions.WorkerProbeAvailable = probe.Available
	conditions.WorkerProbeReason = probe.Reason
	return conditions
}

func referenceField(label, value string) {
	render.Field(os.Stdout, "  "+label, 18, value, render.Width())
}

func shortReference(value string) string {
	const prefix = "sha256:"
	if strings.HasPrefix(value, prefix) && len(value) >= len(prefix)+12 {
		return value[len(prefix) : len(prefix)+12]
	}
	return value
}
