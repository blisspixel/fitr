package decision

import (
	"errors"
	"fmt"
	"regexp"
)

const (
	applicationUnobserved       = "application was not observed"
	effortComparisonUnqualified = "effort comparison is not qualified"
	reasoningActionCode         = "record_reasoning_application"
)

// templateDigest is a lowercase SHA-256. An empty digest is an explicit
// unknown, which is a different fact from a legacy record that omitted the
// field and from a digest that was actually computed.
var templateDigest = regexp.MustCompile(`^[0-9a-f]{64}$`)

// SealApplication accepts only unknown, and only with the provenance that
// application was not observed. observed, unsupported, and ignored are
// rejected here. No signal in this package is allowed to write them.
func SealApplication(application, provenance string) error {
	switch application {
	case "unknown":
		if provenance != applicationUnobserved {
			return errors.New("missing provenance: application was not observed")
		}
		return nil
	case "observed", "unsupported", "ignored":
		return fmt.Errorf("application %s is rejected at the seal", application)
	default:
		return fmt.Errorf("application %q is rejected at the seal", application)
	}
}

// ApplicationSignals is the set of observations that must not become an
// applied reasoning mode. The function takes them so a caller records that
// they were considered.
type ApplicationSignals struct {
	Elapsed        int
	OutputLength   int
	CapabilityFlag bool
	SelfReport     string
}

// ApplicationFromSignals returns unknown for every signal combination.
// Elapsed time, output length, a capability flag, and a self-report do not
// observe, reject, or ignore an effort.
func ApplicationFromSignals(ApplicationSignals) string {
	return "unknown"
}

// EffortRef is one side of an effort comparison. Set false is a legacy
// record whose field is absent. That absence is not an omitted effort and
// not an explicit token.
type EffortRef struct {
	Set     bool
	Omitted bool
	Value   string
}

// PoolEfforts refuses to treat an omitted effort and an explicit effort as
// one configuration. An absent field stays uncompared instead of becoming
// a default token.
func PoolEfforts(left, right EffortRef) error {
	if !left.Set || !right.Set {
		return errors.New("effort is absent and stays uncompared")
	}
	if left.Omitted != right.Omitted {
		return errors.New("an omitted effort does not pool with an explicit effort")
	}
	if left.Omitted {
		if left.Value != "" || right.Value != "" {
			return errors.New("an omitted effort was filled with a token")
		}
		return nil
	}
	if left.Value == "" || right.Value == "" || left.Value != right.Value {
		return errors.New("explicit efforts differ")
	}
	return nil
}

// TemplateRef is one side of a template comparison. Set false means the
// record did not carry a template field. A set empty digest is unknown.
type TemplateRef struct {
	Set    bool
	Digest string
}

// PoolTemplates refuses to pool an unknown template with a digested one.
// An absent field stays uncompared. Two unknown templates do not become a
// shared digest.
func PoolTemplates(left, right TemplateRef) error {
	if !left.Set || !right.Set {
		return errors.New("template is absent and stays uncompared")
	}
	leftDigested := templateDigest.MatchString(left.Digest)
	rightDigested := templateDigest.MatchString(right.Digest)
	switch {
	case left.Digest == "" && right.Digest == "":
		return errors.New("an unknown template stays uncompared")
	case leftDigested != rightDigested:
		return errors.New("an unknown template does not pool with a digested template")
	case !leftDigested:
		return errors.New("template digest is not lowercase sha256")
	case left.Digest != right.Digest:
		return errors.New("template digests differ")
	default:
		return nil
	}
}

// PairLeg is one side of a fixed-task comparison. Absent bounds stay
// absent. A declared zero is Set with a zero count, not an unset field.
type PairLeg struct {
	Task      TaskRef
	Reserves  Reserves
	Budget    Budget
	Outcome   string
	Fast      bool
	Mandatory bool
}

// TaskRef names the task the pair promised to hold fixed.
type TaskRef struct {
	Set bool
	ID  string
}

// Reserves are the user requirements and the three token bounds a faster
// variant is not allowed to shrink.
type Reserves struct {
	Set       bool
	User      string
	System    int
	Output    int
	Reasoning int
}

// Budget is the resource budget the pair promised to hold fixed.
type Budget struct {
	Set   bool
	Limit string
}

// PairQualification is the derived comparison. Qualified means the task,
// reserves, and budget matched. It does not mean the variant passed.
type PairQualification struct {
	Comparison string `json:"comparison"`
	Qualified  bool   `json:"qualified"`
	State      string `json:"state"`
	Missing    string `json:"missing,omitempty"`
	Action     string `json:"action,omitempty"`
}

var pairStates = map[string]bool{
	"pass": true, "quality-failed": true, "truncated": true, "no-progress": true,
	"incomplete": true, "refused": true, "transport-failed": true,
}

// QualifyPair reports the variant outcome when the task, reserves, and
// budget are the same on both legs. A drift is not a pass. A fast variant
// that failed a mandatory outcome stays quality-failed. Truncation and
// no-progress stay their own states.
func QualifyPair(base, variant PairLeg) PairQualification {
	if reason := pairDrift(base, variant); reason != "" {
		return PairQualification{
			Comparison: "paired task is not qualified",
			State:      "uncompared",
			Missing:    reason,
			Action:     pairAction(reason),
		}
	}
	state := variant.Outcome
	if !pairStates[state] {
		return PairQualification{
			Comparison: "paired task is not qualified",
			State:      "uncompared",
			Missing:    "outcome is not a known disposition",
			Action:     "record the variant outcome as its own disposition",
		}
	}
	return PairQualification{Comparison: "paired task", Qualified: true, State: state}
}

func pairDrift(base, variant PairLeg) string {
	if !base.Task.Set || !variant.Task.Set {
		return "task is absent and stays uncompared"
	}
	if base.Task.ID == "" || base.Task.ID != variant.Task.ID {
		return "task differs"
	}
	if reason := reservesDrift(base.Reserves, variant.Reserves); reason != "" {
		return reason
	}
	if !base.Budget.Set || !variant.Budget.Set {
		return "budget is absent and stays uncompared"
	}
	if base.Budget.Limit == "" || base.Budget.Limit != variant.Budget.Limit {
		return "budget differs"
	}
	return ""
}

func reservesDrift(base, variant Reserves) string {
	if !base.Set || !variant.Set {
		return "reserves are absent and stay uncompared"
	}
	if base.System < 0 || base.Output < 0 || base.Reasoning < 0 ||
		variant.System < 0 || variant.Output < 0 || variant.Reasoning < 0 {
		return "reserve is unmeasured"
	}
	if base.User != variant.User {
		return "user requirements differ"
	}
	if variant.System < base.System || variant.Output < base.Output || variant.Reasoning < base.Reasoning {
		return "reserves were reduced"
	}
	if variant.System != base.System || variant.Output != base.Output || variant.Reasoning != base.Reasoning {
		return "reserves differ"
	}
	return ""
}

func pairAction(reason string) string {
	switch reason {
	case "reserves were reduced":
		return "repeat the pair with the original reserves"
	case "task differs":
		return "repeat the pair on the same task"
	case "budget differs":
		return "repeat the pair with the same budget"
	default:
		return "repeat the pair with the task, reserves, and budget fixed"
	}
}
