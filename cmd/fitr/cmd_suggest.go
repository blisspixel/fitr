package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/blisspixel/fitr/internal/device"
	"github.com/blisspixel/fitr/internal/isolate"
	"github.com/blisspixel/fitr/internal/render"
	"github.com/blisspixel/fitr/internal/suggest"
)

func cmdSuggest(ctx context.Context, args []string) int {
	fs := commandFlags("suggest")
	sizeGB := fs.Float64("size-gb", 0, "project this weight size instead of asking the registry")
	mode := fs.String("display", "auto", "auto|rich|plain|json|none")
	if code, ok := parseCommandFlags(fs, args); !ok {
		return code
	}
	if !render.ValidMode(*mode) {
		errPrint("invalid display mode", *mode, "use auto, rich, plain, json, or none")
		return exitUsage
	}
	if fs.NArg() > 1 {
		errPrint("unexpected argument", fs.Arg(1), "fitr suggest [model] [--size-gb N]")
		return exitUsage
	}
	budget, source := suggestionBudget(ctx)
	if fs.NArg() == 1 {
		return suggestOne(ctx, fs.Arg(0), *sizeGB, budget, source)
	}
	if *sizeGB > 0 {
		errPrint("--size-gb needs a model name", "", "fitr suggest <model> --size-gb N")
		return exitUsage
	}
	return suggestShortlist(ctx, budget, source)
}

func suggestionBudget(ctx context.Context) (float64, string) {
	fp := device.Detect(ctx, nil)
	if fp.VRAMGb > 0 {
		return fp.VRAMGb, fp.VRAMSource
	}
	if fp.RAMGb > 0 {
		return fp.RAMGb, "system RAM, because GPU memory was not measured"
	}
	return 0, ""
}

func suggestOne(ctx context.Context, name string, sizeGB, budget float64, source string) int {
	var bytes int64
	var sizeSource string
	if sizeGB > 0 {
		bytes = int64(sizeGB * 1024 * 1024 * 1024)
		sizeSource = "--size-gb"
	} else {
		got, err := fetchManifestBytes(ctx, name)
		if err != nil {
			errPrint("could not read the registry manifest", err.Error(), "pass --size-gb when the size is already known")
			return exitError
		}
		bytes, sizeSource = got, "registry manifest layer sizes"
	}
	project := suggest.ProjectWeights(budget, bytes)
	fmt.Printf("%s\n", name)
	fmt.Printf("  size source  %s\n", sizeSource)
	if source != "" {
		fmt.Printf("  budget       %.1f GiB (%s)\n", budget, source)
	}
	fmt.Printf("  %s\n", project.Note)
	if project.Known && project.WeightsFit {
		fmt.Printf("  pull         ollama pull %s\n", name)
	}
	return exitOK
}

func suggestShortlist(ctx context.Context, budget float64, source string) int {
	fp := device.Detect(ctx, nil)
	items := suggest.Shortlist()
	prefer := suggest.PreferMoE(fp.GPU, fp.GPUBackend, fp.VRAMSource)
	observed := make([]observedWeight, len(items))
	weightBytes := make([]int64, len(items))
	for i, item := range items {
		got, err := fetchManifestBytes(ctx, item.Name)
		observed[i] = observedWeight{bytes: got, err: err}
		if err == nil {
			weightBytes[i] = got
		}
	}
	fmt.Println("projection, not a measurement. Sizes come from the registry manifest. KV cache is not estimated.")
	if source != "" {
		fmt.Printf("budget %.1f GiB (%s)\n", budget, source)
	}
	index := make(map[string]int, len(items))
	for i, item := range items {
		index[item.Name] = i
	}
	for _, group := range suggest.Partition(items, weightBytes, budget, prefer) {
		if label := suggestGroupLabel(group.Class); label != "" {
			fmt.Println(label)
		}
		for _, item := range group.Items {
			fmt.Println(formatSuggestCandidate(item, observed[index[item.Name]], budget, group.Class))
		}
	}
	return exitOK
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

func formatSuggestCandidate(item suggest.Candidate, observed observedWeight, budget float64, class string) string {
	label := item.Use
	if item.CatalogMoE {
		label += ", catalog MoE (active parameters were not read from the manifest)"
	}
	if observed.err != nil {
		return fmt.Sprintf("%s  %s  size unobserved: %s", item.Name, label, observed.err.Error())
	}
	project := suggest.ProjectWeights(budget, observed.bytes)
	line := item.Name + "  " + label + "  " + project.Note
	if class == suggest.WeightFits && project.Known && project.WeightsFit {
		line += "  pull: ollama pull " + item.Name
	}
	return line
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
