package eval

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"

	"github.com/blisspixel/fitr/internal/ollama"
)

const (
	RequestEnvelopeSchema    = "fitr.eval.request-envelope.v1"
	ContextProbeOutputTokens = 1
	SpeedWarmupOutputTokens  = 8
	MemoryProbeOutputTokens  = 4
	PlumbingOutputTokens     = 300
	maxEnvelopePhases        = 2 * maxUserChecks
)

// RequestEnvelopeOptions describes an already selected battery, with explicit
// check rounds rather than CLI defaults. Only the native Ollama request policy
// is supported. ContextProbe includes the runner's pre-battery load/context
// inference; metadata, unload and other control-plane calls are excluded.
type RequestEnvelopeOptions struct {
	Backend         string `json:"backend"`
	Level           string `json:"level"`
	Repeats         int    `json:"repeats"`
	CheckRepeats    int    `json:"check_repeats"`
	AllowUnsafeExec bool   `json:"allow_unsafe_exec"`
	ContextProbe    bool   `json:"context_probe"`
}

// PhaseRequestEnvelope bounds an operation's requested output and actual HTTP
// attempts, including application retries. A conditional operation reserves its
// maximum even when a particular model stops early or cannot use tools.
type PhaseRequestEnvelope struct {
	Phase                    string `json:"phase"`
	Kind                     string `json:"kind"`
	Calls                    int64  `json:"calls"`
	AttemptsPerCall          int64  `json:"attempts_per_call"`
	MaxOutputTokens          int64  `json:"max_output_tokens"`
	MaxRequests              int64  `json:"max_requests"`
	MaxRequestedOutputTokens int64  `json:"max_requested_output_tokens"`
}

// RequestEnvelope is an upper reservation, not observed usage or a guarantee
// that the model can finish successfully within a wall-time limit. The caller
// binds it to the immutable spec/build and protects a separate complete copy
// of the confirmation schedule before admitting exploration.
type RequestEnvelope struct {
	Schema                   string                 `json:"schema"`
	Options                  RequestEnvelopeOptions `json:"options"`
	Phases                   []PhaseRequestEnvelope `json:"phases"`
	MaxRequests              int64                  `json:"max_requests"`
	MaxRequestedOutputTokens int64                  `json:"max_requested_output_tokens"`
}

func (options RequestEnvelopeOptions) validate() error {
	if options.Backend != "ollama" {
		return errors.New("request envelope supports only native Ollama")
	}
	switch options.Level {
	case "quick", "default", "full", "checks":
	default:
		return errors.New("request envelope requires a known battery level")
	}
	if options.Repeats <= 0 || options.CheckRepeats <= 0 {
		return errors.New("request envelope requires positive explicit repeat counts")
	}
	return nil
}

// PlanRequestEnvelope derives the schedule from the same task specifications,
// execution gate, check generators and retry limit used by the actual runner.
// It neither performs inference nor writes fixtures. Optional admission probes
// outside this battery must receive additional reservations from the caller.
func PlanRequestEnvelope(spec *Spec, options RequestEnvelopeOptions) (RequestEnvelope, error) {
	if err := options.validate(); err != nil {
		return RequestEnvelope{}, err
	}
	if spec == nil || len(spec.Checks) > maxEnvelopePhases-32 {
		return RequestEnvelope{}, errors.New("request envelope requires a bounded task specification")
	}
	plan := RequestEnvelope{Schema: RequestEnvelopeSchema, Options: options, Phases: []PhaseRequestEnvelope{}}
	if options.ContextProbe {
		plan.add("context_probe", ollama.InferenceGenerate, 1, ContextProbeOutputTokens)
	}
	if options.Level != "checks" {
		plan.addStandard(spec)
	}
	if options.Level != "quick" {
		if err := plan.addChecks(spec.Checks); err != nil {
			return RequestEnvelope{}, err
		}
	}
	plan.addToolAndRefusal(spec)
	if err := plan.total(); err != nil {
		return RequestEnvelope{}, err
	}
	return plan, plan.Validate()
}

func (plan *RequestEnvelope) add(phase, kind string, calls int64, output int) {
	attempts := int64(1)
	if kind == ollama.InferenceChat {
		attempts = ollama.ChatMaxAttempts
	}
	plan.Phases = append(plan.Phases, PhaseRequestEnvelope{
		Phase: phase, Kind: kind, Calls: calls, AttemptsPerCall: attempts, MaxOutputTokens: int64(output),
	})
}

func (plan *RequestEnvelope) addStandard(spec *Spec) {
	repeats := int64(plan.Options.Repeats)
	plan.add("speed.warmup", ollama.InferenceGenerate, repeats, SpeedWarmupOutputTokens)
	plan.add("speed.decode", ollama.InferenceGenerate, repeats, spec.Speed.Decode.NumPredict)
	// Native Ollama supplies no cache receipt. RunSpeed therefore omits its
	// conditional replay; a backend that supplies one needs a different policy.
	plan.add("speed.prefill", ollama.InferenceGenerate, repeats, spec.Speed.Prefill.NumPredict)
	plan.add("memory", ollama.InferenceGenerate, 1, MemoryProbeOutputTokens)
	if plan.Options.AllowUnsafeExec {
		plan.add("coding.write", ollama.InferenceGenerate, repeats, spec.CodeWrite.NumPredict)
		plan.add("coding.fix", ollama.InferenceGenerate, repeats, spec.CodeFix.NumPredict)
	}
}

func (plan *RequestEnvelope) addChecks(checks []CheckSpec) error {
	for _, check := range checks {
		if err := ValidateCheck(check); err != nil {
			return err
		}
		// Channel selection is a property of each current generator, shared
		// with RunCheck through UsesToolChannel, not inferred from a task name.
		kind := ollama.InferenceGenerate
		if Generate(check, 0).UsesToolChannel() {
			kind = ollama.InferenceChat
		}
		plan.add("check."+check.ID, kind, int64(plan.Options.CheckRepeats), check.NumPredict)
	}
	return nil
}

func (plan *RequestEnvelope) addToolAndRefusal(spec *Spec) {
	if plan.Options.Level == "checks" {
		return
	}
	plan.add("plumbing.emit", ollama.InferenceChat, 1, PlumbingOutputTokens)
	plan.add("plumbing.roundtrip", ollama.InferenceChat, 1, PlumbingOutputTokens)
	plan.add("plumbing.irrelevance", ollama.InferenceChat, 1, PlumbingOutputTokens)
	plan.addLoop("tools", spec.Tools, int64(plan.Options.Repeats))
	if plan.Options.Level == "quick" {
		return
	}
	plan.addLoop("withdrawal", spec.Withdrawal, 1)
	for _, id := range RefusalPromptIDs(spec.Refusal) {
		plan.add("refusal."+id, ollama.InferenceGenerate, 1, spec.Refusal.NumPredict)
	}
	if plan.Options.Level == "full" {
		plan.addLoop("agentic", spec.Agentic, 1)
	}
}

func (plan *RequestEnvelope) addLoop(phase string, spec ToolLoopSpec, repeats int64) {
	if ToolLoopRequiresExecution(spec) && !plan.Options.AllowUnsafeExec {
		return
	}
	// Preserve a malformed/overflowing count as invalid for total/Validate.
	calls, err := envelopeProduct(repeats, int64(spec.MaxTurns))
	if err != nil {
		calls = 0
	}
	plan.add(phase, ollama.InferenceChat, calls, spec.NumPredict)
}

func envelopeProduct(a, b int64) (int64, error) {
	if a <= 0 || b <= 0 || a > math.MaxInt64/b {
		return 0, errors.New("request envelope has an invalid or overflowing bound")
	}
	return a * b, nil
}

func (phase PhaseRequestEnvelope) totals() (int64, int64, error) {
	requests, err := envelopeProduct(phase.Calls, phase.AttemptsPerCall)
	if err != nil {
		return 0, 0, err
	}
	tokens, err := envelopeProduct(requests, phase.MaxOutputTokens)
	return requests, tokens, err
}

func (plan *RequestEnvelope) total() error {
	plan.MaxRequests, plan.MaxRequestedOutputTokens = 0, 0
	for index := range plan.Phases {
		phase := &plan.Phases[index]
		requests, tokens, err := phase.totals()
		if err != nil {
			return fmt.Errorf("phase %s: %w", phase.Phase, err)
		}
		if plan.MaxRequests > math.MaxInt64-requests || plan.MaxRequestedOutputTokens > math.MaxInt64-tokens {
			return errors.New("request envelope totals overflow")
		}
		phase.MaxRequests, phase.MaxRequestedOutputTokens = requests, tokens
		plan.MaxRequests += requests
		plan.MaxRequestedOutputTokens += tokens
	}
	return nil
}

// Validate checks structure, retry policy and exact arithmetic. To bind an
// envelope to a spec, recompute it from that spec and compare its Digest.
func (plan RequestEnvelope) Validate() error {
	if plan.Schema != RequestEnvelopeSchema || len(plan.Phases) > maxEnvelopePhases {
		return errors.New("invalid request envelope schema or phase count")
	}
	if err := plan.Options.validate(); err != nil {
		return err
	}
	seen := make(map[string]bool, len(plan.Phases))
	for _, phase := range plan.Phases {
		if phase.Phase == "" || seen[phase.Phase] {
			return errors.New("request envelope has an empty or duplicate phase")
		}
		seen[phase.Phase] = true
		if err := phase.validate(); err != nil {
			return err
		}
	}
	copyPlan := plan
	copyPlan.Phases = append([]PhaseRequestEnvelope(nil), plan.Phases...)
	if err := copyPlan.total(); err != nil {
		return err
	}
	if copyPlan.MaxRequests != plan.MaxRequests || copyPlan.MaxRequestedOutputTokens != plan.MaxRequestedOutputTokens {
		return errors.New("request envelope totals differ from its phases")
	}
	return nil
}

func (phase PhaseRequestEnvelope) validate() error {
	attempts := int64(1)
	switch phase.Kind {
	case ollama.InferenceGenerate:
	case ollama.InferenceChat:
		attempts = ollama.ChatMaxAttempts
	default:
		return errors.New("request envelope has an unsupported request kind")
	}
	requests, tokens, err := phase.totals()
	if err != nil {
		return err
	}
	if phase.AttemptsPerCall != attempts || requests != phase.MaxRequests || tokens != phase.MaxRequestedOutputTokens {
		return errors.New("request envelope phase has inconsistent bounds")
	}
	return nil
}

// Digest binds the validated JSON envelope, including the reservation policy.
func (plan RequestEnvelope) Digest() (string, error) {
	if err := plan.Validate(); err != nil {
		return "", err
	}
	data, err := json.Marshal(plan)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}
