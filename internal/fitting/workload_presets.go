package fitting

import (
	"fmt"
	"strings"
)

type presetSpec struct {
	outcomes     []string
	populated    bool
	requirements []Requirement
}

func knownPreset(name string) bool {
	_, ok := presetByName(name)
	return ok
}

func presetOutcomes(name string) ([]string, error) {
	if name == "" {
		return nil, nil
	}
	spec, ok := presetByName(name)
	if !ok {
		return nil, fmt.Errorf("unknown workload preset %q", name)
	}
	return append([]string(nil), spec.outcomes...), nil
}

func presetRequirements(name, scope string) []Requirement {
	spec, ok := presetByName(name)
	if !ok {
		return nil
	}
	out := append([]Requirement(nil), spec.requirements...)
	if name == "agentic-coding" && scope == ScopeScreen {
		out = append(out, Requirement{
			Class: ClassMandatory, Name: "coding",
			Detail: "Isolated acceptance checks remain unresolved. Screening does not establish them, and tool-call syntax is not autonomous competence.",
		})
	}
	return out
}

func presetByName(name string) (presetSpec, bool) {
	switch name {
	case "repository-planning":
		return presetSpec{
			outcomes:  []string{"instruction_precision", "structured_output", "tool_restraint"},
			populated: true,
			requirements: []Requirement{
				{Class: ClassMandatory, Name: "populated-context", Detail: "Token-accounted input with constraints at different positions. Byte-sized document tiers are not this evidence. The short battery cannot establish it."},
				{Class: ClassMandatory, Name: "outcomes", Detail: "Instruction following, structured output, and tool restraint have to be shown. The preset name is not that evidence."},
				{Class: ClassRecommendation, Name: "desired-context", Detail: "Set --desired-context to the working context you need. This preset does not substitute 32768 or 65536."},
			},
		}, true
	case "agentic-coding":
		return presetSpec{
			outcomes: []string{"coding", "tool_calling", "tool_restraint"},
			requirements: []Requirement{
				{Class: ClassMandatory, Name: "coding", Detail: "Isolated execution of acceptance checks. Tool-call syntax is not broad autonomous competence."},
				{Class: ClassMandatory, Name: "tool-channel", Detail: "Native tool calls and tool restraint, including clean withdrawal. tool_args grades JSON written as text and is a different finding."},
				{Class: ClassRecommendation, Name: "desired-context", Detail: "Set --desired-context for the working window. A passing short battery does not establish a larger workflow."},
			},
		}, true
	case "code-review":
		return presetSpec{
			outcomes: []string{"instruction_precision", "structured_output"},
			requirements: []Requirement{
				{Class: ClassMandatory, Name: "review", Detail: "Instruction following and structured review output have to pass."},
				{Class: ClassRecommendation, Name: "populated-context", Detail: "A repository review wants populated context. A short prompt is not that review. This preset does not make that requirement mandatory."},
			},
		}, true
	case "document-research":
		return presetSpec{
			outcomes:  []string{"instruction_precision", "structured_output"},
			populated: true,
			requirements: []Requirement{
				{Class: ClassMandatory, Name: "populated-context", Detail: "Token-accounted documents with constraints at different positions and an independent check of the final answer. Document tiers in bytes are not token qualification."},
				{Class: ClassRecommendation, Name: "desired-context", Detail: "Set --desired-context. This preset does not choose a window."},
			},
		}, true
	case "structured-extraction":
		return presetSpec{
			outcomes: []string{"structured_output"},
			requirements: []Requirement{
				{Class: ClassMandatory, Name: "structured-output", Detail: "Structured output has to pass. The preset does not add a hidden context floor."},
				{Class: ClassPreference, Name: "context", Detail: "Context stays the window passed with --ctx unless --desired-context is set."},
			},
		}, true
	default:
		return presetSpec{}, false
	}
}

func modelCardsFor(candidates []string, explicit string) []ModelCardNote {
	if !namesQwen36(candidates, explicit) {
		return nil
	}
	const source = "https://huggingface.co/Qwen/Qwen3.6-27B checked 2026-10-06"
	return []ModelCardNote{
		{
			Model: "Qwen3.6-27B", Source: source, Kind: "capability", Context: Qwen36NativeContext,
			Claim: "the card lists 262144 native context and extension up to 1010000 tokens with the appropriate setup. This is not a measurement of a quantized local build.",
		},
		{
			Model: "Qwen3.6-27B", Source: source, Kind: "recommendation", Context: Qwen36ThinkingContext,
			Claim: "the card advises at least 128K tokens (131072) to preserve thinking. That is a vendor recommendation, not a harness floor and not local proof.",
		},
	}
}

func namesQwen36(candidates []string, explicit string) bool {
	if strings.EqualFold(explicit, "qwen3.6-27b") {
		return true
	}
	for _, name := range candidates {
		if strings.Contains(strings.ToLower(name), "qwen3.6") {
			return true
		}
	}
	return false
}
