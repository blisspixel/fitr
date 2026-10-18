package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/blisspixel/fitr/internal/device"
	"github.com/blisspixel/fitr/internal/isolate"
	"github.com/blisspixel/fitr/internal/llm"
	"github.com/blisspixel/fitr/internal/render"
	"github.com/blisspixel/fitr/internal/suggest"
)

const suggestUsageHint = "fitr suggest [model] [--size-gb N] [--ceiling-gb N]"

func cmdSuggest(ctx context.Context, args []string) int {
	fs := commandFlags("suggest")
	sizeGB := fs.Float64("size-gb", 0, "project this artifact size in GiB instead of asking the registry")
	ceilingGB := fs.Float64("ceiling-gb", 0, "operator ceiling in GiB when resident memory is shared or unmeasured")
	mode := fs.String("display", "auto", "auto|rich|plain|json|none")
	if code, ok := parseCommandFlags(fs, args); !ok {
		return code
	}
	if code := suggestUsage(fs, *mode, *sizeGB, *ceilingGB); code != 0 {
		return code
	}
	fp := device.Detect(ctx, nil)
	plan := planSuggestion(fp, *ceilingGB)
	runtime := observedLibraryRuntime(ctx)
	if fs.NArg() == 1 {
		return suggestOne(ctx, fs.Arg(0), *sizeGB, plan, runtime)
	}
	return suggestShortlist(ctx, fp, plan, runtime)
}

func suggestUsage(fs *flag.FlagSet, mode string, sizeGB, ceilingGB float64) int {
	switch {
	case !render.ValidMode(mode):
		errPrint("invalid display mode", mode, "use auto, rich, plain, json, or none")
		return exitUsage
	case invalidCeiling(ceilingGB):
		errPrint("invalid --ceiling-gb", "expected a positive number of GiB, or omit the flag", suggestUsageHint)
		return exitUsage
	case fs.NArg() > 1:
		errPrint("unexpected argument", fs.Arg(1), suggestUsageHint)
		return exitUsage
	case fs.NArg() == 0 && sizeGB > 0:
		errPrint("--size-gb needs a model name", "", "fitr suggest <model> --size-gb N")
		return exitUsage
	default:
		return 0
	}
}

func invalidCeiling(gb float64) bool {
	return math.IsNaN(gb) || math.IsInf(gb, 0) || gb < 0
}

// planSuggestion classifies weights only against a trusted discrete
// accelerator. Shared, empty, and zero selections stay unmeasured. An
// operator ceiling can project weights in that case, and the line says it
// is not a measured resident budget. A ceiling does not replace a discrete
// reading.
func planSuggestion(fp device.Fingerprint, ceilingGB float64) suggestionPlan {
	selected := device.SelectMemoryCapacity(fp)
	if acceleratorBudget(selected) {
		plan := suggestionPlan{Budget: selected.GB, Source: selected.Source, Kind: "accelerator"}
		if ceilingGB > 0 {
			plan.Note = "operator ceiling was not the classifier: a discrete accelerator budget was measured"
		}
		return plan
	}
	if ceilingGB > 0 {
		return suggestionPlan{
			Budget: ceilingGB,
			Source: "operator ceiling, not a measured resident budget",
			Kind:   "ceiling",
		}
	}
	source := strings.TrimSpace(selected.Source)
	if source == "" {
		source = "no measured memory budget"
	}
	return suggestionPlan{Source: source, Kind: "unmeasured"}
}

func acceleratorBudget(selected device.MemoryCapacity) bool {
	if selected.Shared || selected.GB <= 0 || math.IsNaN(selected.GB) || math.IsInf(selected.GB, 0) {
		return false
	}
	return strings.TrimSpace(selected.Source) != ""
}

func (plan suggestionPlan) budgetLine() string {
	switch plan.Kind {
	case "accelerator":
		line := fmt.Sprintf("budget %.1f GiB (%s)", plan.Budget, plan.Source)
		if plan.Note != "" {
			return line + "; " + plan.Note
		}
		return line
	case "ceiling":
		return fmt.Sprintf("budget %.1f GiB (%s)", plan.Budget, plan.Source)
	default:
		return "budget unmeasured (" + plan.Source + "); resident fit is unmeasured"
	}
}

type suggestionPlan struct {
	Budget float64
	Source string
	Kind   string
	Note   string
}

// reachabilityUnmeasured is a Discover error. An empty probe may still
// print an Ollama library recipe. A failed probe must not.
const reachabilityUnmeasured = "reachability-unmeasured"

func reachableLibraryRuntime(found []llm.Found) string {
	other := ""
	for _, item := range found {
		switch item.Kind {
		case "ollama":
			return "ollama"
		case "":
		default:
			if other == "" {
				other = item.Kind
			}
		}
	}
	return other
}

func observedLibraryRuntime(ctx context.Context) string {
	found, err := llm.Discover(ctx)
	if err != nil {
		return reachabilityUnmeasured
	}
	return reachableLibraryRuntime(found)
}

func suggestOne(ctx context.Context, name string, sizeGB float64, plan suggestionPlan, runtime string) int {
	bytes, sizeSource, code := suggestArtifactBytes(ctx, name, sizeGB)
	if code != 0 {
		return code
	}
	project := suggest.ProjectWeights(plan.Budget, bytes)
	fmt.Printf("%s\n", name)
	fmt.Printf("  size source  %s\n", sizeSource)
	fmt.Printf("  %s\n", plan.budgetLine())
	fmt.Printf("  %s\n", project.Note)
	if recipe := libraryRecipe(name, runtime, project.Known && project.WeightsFit); recipe != "" {
		fmt.Printf("  %s\n", recipe)
	}
	return exitOK
}

func suggestArtifactBytes(ctx context.Context, name string, sizeGB float64) (int64, string, int) {
	if sizeGB > 0 {
		return int64(sizeGB * 1024 * 1024 * 1024), "--size-gb", 0
	}
	got, err := fetchManifestBytes(ctx, name)
	if err != nil {
		errPrint("could not read the registry manifest", err.Error(), "pass --size-gb when the size is already known")
		return 0, "", exitError
	}
	return got, "registry manifest layer sizes", 0
}

func suggestShortlist(ctx context.Context, fp device.Fingerprint, plan suggestionPlan, runtime string) int {
	items := suggest.Shortlist()
	observed, weightBytes := observeShortlist(ctx, items)
	fmt.Printf("names are a fixed reviewed set (%s), not a search. A catalog tag is not a measured purpose.\n", suggest.ShortlistReviewed)
	fmt.Println("projection, not a measurement. Sizes come from the registry manifest. KV cache is not estimated.")
	fmt.Println(plan.budgetLine())
	prefer := suggest.PreferMoE(fp.GPU, fp.GPUBackend, fp.VRAMSource)
	index := make(map[string]int, len(items))
	for i, item := range items {
		index[item.Name] = i
	}
	for _, group := range suggest.Partition(items, weightBytes, plan.Budget, prefer) {
		if label := suggestGroupLabel(group.Class); label != "" {
			fmt.Println(label)
		}
		for _, item := range group.Items {
			fmt.Println(formatSuggestCandidate(item, observed[index[item.Name]], plan.Budget, group.Class, runtime))
		}
	}
	return exitOK
}

func observeShortlist(ctx context.Context, items []suggest.Candidate) ([]observedWeight, []int64) {
	observed := make([]observedWeight, len(items))
	weightBytes := make([]int64, len(items))
	for i, item := range items {
		got, err := fetchManifestBytes(ctx, item.Name)
		observed[i] = observedWeight{bytes: got, err: err}
		if err == nil {
			weightBytes[i] = got
		}
	}
	return observed, weightBytes
}

type observedWeight struct {
	bytes int64
	err   error
}

func suggestGroupLabel(class string) string {
	switch class {
	case suggest.WeightOverBudget:
		return "weights exceed the budget; this is not resident fit, and KV was not estimated"
	case suggest.WeightUnmeasured:
		return "size or budget unmeasured; these names are not a fit and not a rejection"
	default:
		return ""
	}
}

func formatSuggestCandidate(item suggest.Candidate, observed observedWeight, budget float64, class, runtime string) string {
	label := catalogLabel(item)
	if observed.err != nil {
		return item.Name + "  " + label + "  size unobserved: " + observed.err.Error()
	}
	project := suggest.ProjectWeights(budget, observed.bytes)
	line := item.Name + "  " + label + "  " + project.Note
	offer := class == suggest.WeightFits && project.Known && project.WeightsFit
	if recipe := libraryRecipe(item.Name, runtime, offer); recipe != "" {
		line += "  " + recipe
	}
	return line
}

func catalogLabel(item suggest.Candidate) string {
	label := "catalog tag " + item.Use
	if item.CatalogMoE {
		label += ", catalog MoE (active parameters were not read from the manifest)"
	}
	return label
}

func libraryRecipe(name, runtime string, offer bool) string {
	if !offer {
		return ""
	}
	switch runtime {
	case "", "ollama":
		return "Ollama library recipe, not run by fitr: ollama pull " + name
	case reachabilityUnmeasured:
		return "reachability was unmeasured"
	default:
		return "install command for these library names is unmeasured on this runtime"
	}
}

func fetchManifestBytes(ctx context.Context, name string) (int64, error) {
	repo, tag, err := suggest.SplitName(name)
	if err != nil {
		return 0, err
	}
	url := "https://registry.ollama.ai/v2/library/" + repo + "/manifests/" + tag
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/vnd.docker.distribution.manifest.v2+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, err
	}
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("registry returned %s", resp.Status)
	}
	return suggest.ManifestBytes(body)
}

func firstRunNotes(runtimeDown bool, gpu, backend, memorySource string) []string {
	var notes []string
	if runtimeDown {
		if command := localStartCommand(); command != "" {
			notes = append(notes, "start the installed runtime: "+command)
		}
		if pkg := localAcceleratorPackage(gpu); pkg != "" {
			notes = append(notes, "this GPU's Ollama directory has only CPU builds; install "+pkg)
		}
	}
	notes = append(notes, "fitr suggest names models for this machine before a download")
	status := isolate.Probe()
	if status.Available {
		notes = append(notes, status.Reason)
	} else if status.Reason != "" {
		notes = append(notes, status.Reason)
	}
	if suggest.PreferMoE(gpu, backend, memorySource) {
		notes = append(notes, "on a CPU or APU, catalog mixture-of-experts names come first only among weights that fit the budget")
	}
	return notes
}

func localStartCommand() string {
	_, binaryErr := exec.LookPath("ollama")
	home, _ := os.UserHomeDir()
	user := suggest.Unit{User: true, Exists: home != "" && fileExists(filepath.Join(home, ".config", "systemd", "user", "ollama.service"))}
	system := suggest.Unit{Exists: fileExists("/usr/lib/systemd/system/ollama.service") || fileExists("/etc/systemd/system/ollama.service")}
	user.Active = unitActive(true)
	system.Active = unitActive(false)
	return suggest.StartCommand(binaryErr == nil, user, system)
}

func unitActive(user bool) bool {
	args := []string{"is-active", "ollama"}
	if user {
		args = append([]string{"--user"}, args...)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "systemctl", args...).Output()
	return err == nil && strings.TrimSpace(string(out)) == "active"
}

func localAcceleratorPackage(gpu string) string {
	entries, err := os.ReadDir("/usr/lib/ollama")
	if err != nil {
		return ""
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return suggest.AcceleratorPackage(gpu, names)
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
