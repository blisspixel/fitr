// Package eval executes the language-neutral task spec against a model.
//
// The spec (spec/tasks/*.json) is generated from a reference implementation and
// embedded here, so the harness language is decoupled from the task language:
// a task names the interpreter it needs in `runner`, and evaluating a model's
// Rust ability would need cargo, not a rewrite of this package.
package eval

import (
	"embed"
	"encoding/json"
	"fmt"
	"path"

	"github.com/blisspixel/fitr/internal/ollama"
)

//go:embed all:tasks
var tasksFS embed.FS

type Sampling struct {
	Temperature float64 `json:"temperature"`
	TopK        int     `json:"top_k"`
	Seed        int     `json:"seed"`
}

type SpeedSpec struct {
	ID     string `json:"id"`
	Why    string `json:"why"`
	Decode struct {
		Prompt     string `json:"prompt"`
		NumPredict int    `json:"num_predict"`
	} `json:"decode"`
	Prefill struct {
		PromptTemplate     string `json:"prompt_template"`
		ApproxPromptTokens int    `json:"approx_prompt_tokens"`
		NumPredict         int    `json:"num_predict"`
		NonceRequired      bool   `json:"nonce_required"`
		NonceWhy           string `json:"nonce_why"`
	} `json:"prefill"`
	Sampling Sampling `json:"sampling"`
}

type ExecSpec struct {
	ID       string            `json:"id"`
	Why      string            `json:"why"`
	Language string            `json:"language"`
	Entry    string            `json:"entry"`
	Runner   []string          `json:"runner"`
	Prompt   string            `json:"prompt"`
	Files    map[string]string `json:"files"`
	Editable []string          `json:"editable_files"`
	Extract  struct {
		Strategy         string `json:"strategy"`
		PreferContaining string `json:"prefer_containing"`
		DefaultFile      string `json:"default_file"`
	} `json:"extract"`
	PassIfStdoutContains string `json:"pass_if_stdout_contains"`
	NumPredict           int    `json:"num_predict"`
}

type ToolLoopSpec struct {
	ID         string            `json:"id"`
	Why        string            `json:"why"`
	Prompt     string            `json:"prompt"`
	Tools      []ollama.Tool     `json:"tools"`
	Files      map[string]string `json:"files"`
	MaxTurns   int               `json:"max_turns"`
	Budget     int               `json:"time_budget_s"`
	NumPredict int               `json:"num_predict"`
	Verify     struct {
		Runner               []string `json:"runner"`
		PassIfStdoutContains string   `json:"pass_if_stdout_contains"`
	} `json:"verify"`
	ExpectedSequence string `json:"expected_sequence_regex"`
	// WithdrawTool names a tool that disappears from the tools list once
	// WithdrawAfter turns have run - the "tool vanished, stop calling it"
	// scenario every long-lived agent session eventually faces.
	WithdrawTool  string `json:"withdraw_tool,omitempty"`
	WithdrawAfter int    `json:"withdraw_after_turns,omitempty"`
}

type RefusalSpec struct {
	ID             string            `json:"id"`
	Why            string            `json:"why"`
	Prompts        map[string]string `json:"prompts"`
	RefusalMarkers []string          `json:"refusal_markers"`
	NumPredict     int               `json:"num_predict"`
}

type PlumbingSpec struct {
	ID    string        `json:"id"`
	Why   string        `json:"why"`
	Tools []ollama.Tool `json:"tools"`
	Rungs []struct {
		ID     string `json:"id"`
		Check  string `json:"check"`
		Prompt string `json:"prompt"`
		Result string `json:"tool_result"`
		Why    string `json:"why"`
	} `json:"rungs"`
}

type Spec struct {
	Speed      SpeedSpec
	CodeWrite  ExecSpec
	CodeFix    ExecSpec
	Tools      ToolLoopSpec
	Agentic    ToolLoopSpec
	Withdrawal ToolLoopSpec
	Refusal    RefusalSpec
	Plumbing   PlumbingSpec
	Checks     []CheckSpec
	Version    struct {
		SpecVersion         int    `json:"spec_version"`
		ResultSchemaVersion int    `json:"result_schema_version"`
		Note                string `json:"note"`
	}
}

func load(name string, into any) error {
	b, err := tasksFS.ReadFile(path.Join("tasks", name+".json"))
	if err != nil {
		return fmt.Errorf("spec %s: %w", name, err)
	}
	if err := json.Unmarshal(b, into); err != nil {
		return fmt.Errorf("spec %s: %w", name, err)
	}
	return nil
}

// LoadSpec reads the embedded task definitions. A malformed spec is a hard
// failure: silently running a partial battery would produce a scorecard that
// looks complete and is not.
func LoadSpec() (*Spec, error) {
	s := &Spec{}
	for _, step := range []struct {
		name string
		into any
	}{
		{"speed", &s.Speed}, {"code_write", &s.CodeWrite}, {"code_fix", &s.CodeFix},
		{"tools", &s.Tools}, {"agentic", &s.Agentic}, {"tool_withdrawal", &s.Withdrawal},
		{"refusal", &s.Refusal}, {"tool_plumbing", &s.Plumbing},
	} {
		if err := load(step.name, step.into); err != nil {
			return nil, err
		}
	}
	checks, err := loadChecks()
	if err != nil {
		return nil, err
	}
	s.Checks = checks
	b, err := tasksFS.ReadFile("tasks/version.json")
	if err == nil {
		json.Unmarshal(b, &s.Version)
	}
	return s, nil
}
