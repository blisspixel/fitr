// Check tasks: generated, harness-graded, no interpreter needed.
//
// A check is the scalable half of the battery. The classic tasks (code_write,
// tools, agentic...) each have bespoke runners; a check is one shape - generate
// an instance from a seed, prompt the model once, grade the reply in pure Go -
// so adding a task is adding a JSON file, and user tasks ride the same loader.
package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/blisspixel/fitr/internal/llm"
	"github.com/blisspixel/fitr/internal/ollama"
)

// Needs a check may feed. A user task defaults to the user_tasks pool but may
// opt into a built-in pool - the built-ins are defaults, the user's work is
// the point.
var checkNeeds = map[string]bool{
	"structured_output": true, "instruction_precision": true,
	"reasoning": true, "user_tasks": true,
}

type CheckSpec struct {
	ID         string         `json:"id"`
	Kind       string         `json:"kind"` // "check"
	Why        string         `json:"why"`
	Need       string         `json:"need"`
	Family     string         `json:"family"`
	Params     map[string]any `json:"params,omitempty"`
	NumPredict int            `json:"num_predict"`

	// Origin is "builtin" or "user"; set by the loader, never by the file.
	Origin string `json:"-"`
}

// ValidateCheck rejects a malformed check before anything runs. Same principle
// as LoadSpec: silently running a partial battery would produce a scorecard
// that looks complete and is not.
func ValidateCheck(cs CheckSpec) error {
	switch {
	case cs.ID == "":
		return fmt.Errorf("check has no id")
	case cs.Kind != "check":
		return fmt.Errorf("check %s: kind must be \"check\", got %q", cs.ID, cs.Kind)
	case !FamilyKnown(cs.Family):
		return fmt.Errorf("check %s: unknown family %q", cs.ID, cs.Family)
	case !checkNeeds[cs.Need]:
		return fmt.Errorf("check %s: need must be one of structured_output, "+
			"instruction_precision, reasoning, user_tasks; got %q", cs.ID, cs.Need)
	case cs.NumPredict <= 0:
		return fmt.Errorf("check %s: num_predict must be positive", cs.ID)
	}
	if cs.Family == "static" {
		if pString(cs.Params, "prompt", "") == "" {
			return fmt.Errorf("check %s: static family needs params.prompt", cs.ID)
		}
		g, _ := cs.Params["grader"].(map[string]any)
		switch pString(g, "type", "") {
		case "exact", "contains", "regex", "json_object", "number":
		default:
			return fmt.Errorf("check %s: grader.type must be one of exact, contains, "+
				"regex, json_object, number", cs.ID)
		}
	}
	return nil
}

// InstanceSeed derives the per-instance RNG seed. It folds in the run ID and
// the repeat index so every repeat is a fresh instantiation (an independent
// trial, not a re-measurement of one memorable string), while staying
// recordable: the seed is saved with the outcome, so any instance can be
// regenerated exactly.
func InstanceSeed(runID, taskID string, rep int) uint64 {
	h := fnv.New64a()
	fmt.Fprintf(h, "%s|%s|%d", runID, taskID, rep)
	return h.Sum64()
}

// Generate materializes the instance a seed names.
func Generate(cs CheckSpec, seed uint64) Instance {
	rng := rand.New(rand.NewPCG(seed, 0x9E3779B97F4A7C15))
	return families[cs.Family](cs.Params, rng)
}

type CheckOutcome struct {
	TaskID    string `json:"task"`
	Family    string `json:"family"`
	Need      string `json:"need"`
	Origin    string `json:"origin"`
	Seed      uint64 `json:"seed"`
	Pass      bool   `json:"pass"`
	Detail    string `json:"detail"`
	Truncated bool   `json:"truncated,omitempty"`
}

// RunCheck generates the instance, prompts the model once, and grades the
// reply in pure Go. Pass/fail is programmatic, never a model's opinion.
func RunCheck(ctx context.Context, c llm.Backend, model string, cs CheckSpec, seed uint64) (CheckOutcome, error) {
	out := CheckOutcome{TaskID: cs.ID, Family: cs.Family, Need: cs.Need, Origin: cs.Origin, Seed: seed}
	inst := Generate(cs, seed)
	samp := ollama.Deterministic(cs.NumPredict, NumCtx)
	text, m, err := c.Generate(ctx, model, inst.Prompt, samp)
	if err != nil {
		return out, err
	}
	out.Pass, out.Detail = inst.Grade(text)
	out.Truncated = m.Truncated
	if m.Truncated && !out.Pass {
		out.Detail += " (output hit the token cap)"
	}
	return out, nil
}

// loadChecks reads every embedded tasks/checks/*.json, validated and sorted by
// id for a stable phase order.
func loadChecks() ([]CheckSpec, error) {
	entries, err := tasksFS.ReadDir("tasks/checks")
	if err != nil {
		return nil, nil // no checks shipped; not an error
	}
	var out []CheckSpec
	ids := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := tasksFS.ReadFile("tasks/checks/" + e.Name())
		if err != nil {
			return nil, fmt.Errorf("check %s: %w", e.Name(), err)
		}
		var cs CheckSpec
		if err := json.Unmarshal(b, &cs); err != nil {
			return nil, fmt.Errorf("check %s: %w", e.Name(), err)
		}
		cs.Origin = "builtin"
		if err := ValidateCheck(cs); err != nil {
			return nil, err
		}
		if ids[cs.ID] {
			return nil, fmt.Errorf("check id %q appears twice", cs.ID)
		}
		ids[cs.ID] = true
		out = append(out, cs)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// UserTasksDir is where user checks live: $FITR_TASKS, else ~/.fitr/tasks.
func UserTasksDir() string {
	if d := os.Getenv("FITR_TASKS"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".fitr", "tasks")
}

// LoadUserChecks reads *.json from dir. A missing directory means no user
// tasks; a malformed file is a hard error with the filename in it - a typo
// that silently dropped your own task would defeat the point of having one.
//
// Security stance: a user task is DECLARATIVE. It can prompt and grade; it
// cannot name an interpreter, write files, or execute anything. Exec-style
// user tasks stay out until they can be sandboxed honestly.
func LoadUserChecks(dir string) ([]CheckSpec, error) {
	if dir == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []CheckSpec
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("user task %s: %w", p, err)
		}
		var cs CheckSpec
		if err := json.Unmarshal(b, &cs); err != nil {
			return nil, fmt.Errorf("user task %s: %w", p, err)
		}
		if cs.Need == "" {
			cs.Need = "user_tasks"
		}
		cs.Origin = "user"
		if err := ValidateCheck(cs); err != nil {
			return nil, fmt.Errorf("user task %s: %w", p, err)
		}
		out = append(out, cs)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// MergeChecks appends user checks to the built-ins, rejecting id collisions -
// two results under one id would silently pool into the same Wilson interval.
func MergeChecks(builtin, user []CheckSpec) ([]CheckSpec, error) {
	seen := map[string]bool{}
	for _, cs := range builtin {
		seen[cs.ID] = true
	}
	out := append([]CheckSpec(nil), builtin...)
	for _, cs := range user {
		if seen[cs.ID] {
			return nil, fmt.Errorf("user task id %q collides with an existing task", cs.ID)
		}
		seen[cs.ID] = true
		out = append(out, cs)
	}
	return out, nil
}
