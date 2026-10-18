// Package suggest projects whether a model's weights fit a machine before
// anything is downloaded. A projection is not a measurement: registry layer
// sizes do not include KV cache or runtime overhead, and a missing figure
// stays missing.
package suggest

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const bytesPerGiB = 1024 * 1024 * 1024

// Candidate is a catalog name to look up. CatalogMoE orders CPU and APU
// shortlists. It is not a measured active-parameter count.
type Candidate struct {
	Name       string
	Use        string
	CatalogMoE bool
}

// Shortlist is the names fitr will ask the registry about. Sizes are not
// stored here.
func Shortlist() []Candidate {
	return []Candidate{
		{Name: "qwen2.5-coder:1.5b", Use: "coding"},
		{Name: "qwen2.5-coder:7b", Use: "coding"},
		{Name: "llama3.2:3b", Use: "general"},
		{Name: "deepseek-coder:6.7b", Use: "coding"},
		{Name: "mixtral:8x7b", Use: "agent", CatalogMoE: true},
		{Name: "qwen2.5-coder:14b", Use: "coding"},
		{Name: "qwen2.5-coder:32b", Use: "coding"},
	}
}

const (
	// WeightFits is the group whose registry weight bytes are within the
	// budget. Weight fit is not resident fit.
	WeightFits = "fits"
	// WeightOverBudget is known weight that does not fit. It is labeled and
	// is not offered ahead of a name that does.
	WeightOverBudget = "over_budget"
	// WeightUnmeasured means the size or the budget was missing. It is not
	// a fit and not a rejection.
	WeightUnmeasured = "unmeasured"
)

// Group is one labeled slice of a shortlist.
type Group struct {
	Class string
	Items []Candidate
}

// Partition splits candidates by weight bytes against a budget in GiB.
// Catalog MoE preference applies only inside WeightFits. An unknown budget
// or a length mismatch leaves every name WeightUnmeasured, in the input
// order, because there is nothing to classify. KV is not estimated.
func Partition(items []Candidate, weightBytes []int64, budgetGiB float64, cpuOrAPU bool) []Group {
	if len(weightBytes) != len(items) || budgetGiB <= 0 {
		if len(items) == 0 {
			return nil
		}
		return []Group{{Class: WeightUnmeasured, Items: append([]Candidate(nil), items...)}}
	}
	var fits, over, missing []Candidate
	for i, item := range items {
		size := weightBytes[i]
		if size <= 0 {
			missing = append(missing, item)
			continue
		}
		project := ProjectWeights(budgetGiB, size)
		switch {
		case !project.Known:
			missing = append(missing, item)
		case project.WeightsFit:
			fits = append(fits, item)
		default:
			over = append(over, item)
		}
	}
	var groups []Group
	if len(fits) > 0 {
		groups = append(groups, Group{Class: WeightFits, Items: Order(fits, cpuOrAPU)})
	}
	if len(over) > 0 {
		groups = append(groups, Group{Class: WeightOverBudget, Items: over})
	}
	if len(missing) > 0 {
		groups = append(groups, Group{Class: WeightUnmeasured, Items: missing})
	}
	return groups
}

// Order puts catalog MoE coding and agent names first on a CPU or APU.
// Every other machine keeps the shortlist order. Callers that know a budget
// use Partition; this order is only the preference inside the fitting group.
func Order(items []Candidate, cpuOrAPU bool) []Candidate {
	out := append([]Candidate(nil), items...)
	if !cpuOrAPU {
		return out
	}
	var preferred, rest []Candidate
	for _, item := range out {
		if item.CatalogMoE && (item.Use == "coding" || item.Use == "agent") {
			preferred = append(preferred, item)
			continue
		}
		rest = append(rest, item)
	}
	return append(preferred, rest...)
}

// PreferMoE reports whether this machine should see MoE names first.
// A discrete NVIDIA or Apple GPU does not. An unnamed accelerator does.
func PreferMoE(gpu, backend, memorySource string) bool {
	text := strings.ToLower(gpu + " " + backend + " " + memorySource)
	switch {
	case strings.Contains(text, "nvidia"), strings.Contains(text, "geforce"),
		strings.Contains(text, "cuda"), strings.Contains(text, "apple"),
		strings.Contains(text, "metal"):
		return false
	default:
		return true
	}
}

// Unit is one installed service definition. Active is a local fact.
type Unit struct {
	User   bool
	Exists bool
	Active bool
}

// StartCommand is the exact command that starts an installed, stopped Ollama.
// A running unit produces no start command. No binary and no unit produces none.
func StartCommand(binary bool, user, system Unit) string {
	if (user.Exists && user.Active) || (system.Exists && system.Active) {
		return ""
	}
	switch {
	case user.Exists:
		return "systemctl --user start ollama"
	case system.Exists:
		return "sudo systemctl start ollama"
	case binary:
		return "ollama serve"
	default:
		return ""
	}
}

// AcceleratorPackage names the Arch packages that supply a GPU backend when
// the Ollama library directory contains only CPU ggml builds. A vulkan, rocm,
// or cuda entry means the directory is not CPU-only. No GPU name means there
// is nothing to accelerate.
func AcceleratorPackage(gpu string, entries []string) string {
	if strings.TrimSpace(gpu) == "" {
		return ""
	}
	cpu := false
	for _, entry := range entries {
		name := strings.ToLower(entry)
		switch {
		case strings.Contains(name, "vulkan"), strings.Contains(name, "rocm"),
			strings.Contains(name, "cuda"), strings.Contains(name, "hip"):
			return ""
		case strings.Contains(name, "cpu"):
			cpu = true
		}
	}
	if !cpu {
		return ""
	}
	return "ollama-vulkan or ollama-rocm"
}

// Projection is weights against a memory budget. Known is false when either
// input was missing. WeightsFit is never inferred from a name.
type Projection struct {
	WeightsGiB float64
	BudgetGiB  float64
	WeightsFit bool
	Known      bool
	Note       string
}

// ProjectWeights compares a byte size with a budget in GiB. KV is not estimated.
func ProjectWeights(budgetGiB float64, weightBytes int64) Projection {
	if budgetGiB <= 0 || weightBytes <= 0 {
		return Projection{Note: "weights-versus-budget was not projected: the budget or the artifact size is unmeasured"}
	}
	weights := float64(weightBytes) / bytesPerGiB
	fit := weights <= budgetGiB
	relation := "exceed"
	if fit {
		relation = "fit inside"
	}
	return Projection{
		WeightsGiB: weights,
		BudgetGiB:  budgetGiB,
		WeightsFit: fit,
		Known:      true,
		Note: fmt.Sprintf(
			"projection, not a measurement: %.1f GiB of weights %s a %.1f GiB budget; KV cache and runtime overhead were not calculated",
			weights, relation, budgetGiB),
	}
}

// ManifestBytes sums the config blob and layer sizes from an Ollama registry
// manifest. A document without layers is not a size.
func ManifestBytes(body []byte) (int64, error) {
	var doc struct {
		Config struct {
			Size *int64 `json:"size"`
		} `json:"config"`
		Layers []struct {
			Size *int64 `json:"size"`
		} `json:"layers"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return 0, errors.New("registry manifest is not JSON")
	}
	if len(doc.Layers) == 0 {
		return 0, errors.New("registry manifest has no layers")
	}
	var total int64
	if doc.Config.Size != nil {
		if *doc.Config.Size < 0 {
			return 0, errors.New("registry manifest size is negative")
		}
		total += *doc.Config.Size
	}
	for _, layer := range doc.Layers {
		if layer.Size == nil || *layer.Size < 0 {
			return 0, errors.New("registry manifest layer size is missing")
		}
		if total > int64(^uint64(0)>>1)-*layer.Size {
			return 0, errors.New("registry manifest size overflows")
		}
		total += *layer.Size
	}
	if total <= 0 {
		return 0, errors.New("registry manifest size is zero")
	}
	return total, nil
}

// SplitName separates repo and tag. The tag defaults to latest only when the
// caller wrote a bare name; the shortlist always includes a tag.
func SplitName(name string) (repo, tag string, err error) {
	name = strings.TrimSpace(name)
	if name == "" || strings.Contains(name, " ") || strings.Contains(name, "..") {
		return "", "", errors.New("model name is empty")
	}
	repo, tag, ok := strings.Cut(name, ":")
	if !ok || tag == "" {
		tag = "latest"
	}
	if repo == "" || strings.Contains(repo, "/") {
		return "", "", errors.New("model name must be a library repo")
	}
	return repo, tag, nil
}
