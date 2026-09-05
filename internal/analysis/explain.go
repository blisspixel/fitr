package analysis

import (
	"strconv"
	"strings"
)

// DiagnosisSupportLabel is the renderer-neutral name of a diagnosis support
// class. It is a vocabulary token, not a quality score or confidence number.
func DiagnosisSupportLabel(support DiagnosisSupport) string {
	switch support {
	case DiagnosisDirect, "":
		return "direct"
	case DiagnosisInterventionSupported:
		return "intervention-supported"
	case DiagnosisSuggestive:
		return "suggestive"
	case DiagnosisContradicted:
		return "contradicted"
	case DiagnosisBlocked:
		return "blocked"
	default:
		return string(support)
	}
}

// DiagnosisPresentation is the wording every surface must use for one diagnosis.
type DiagnosisPresentation struct {
	Support        string
	Label          string
	Headline       string
	Missing        []string
	Contradictions []string
	NextReason     string
	NextArgv       []string
}

// PresentDiagnosis projects a diagnosis into renderer-neutral lines. Callers
// may wrap or clip; they must not relabel support as "observed" or invent a
// confidence score.
func PresentDiagnosis(diagnosis Diagnosis) DiagnosisPresentation {
	support := DiagnosisSupportLabel(diagnosis.Support)
	label := DiagnosisLabel(diagnosis.Code)
	presented := DiagnosisPresentation{
		Support:        support,
		Label:          label,
		Headline:       label + ": " + diagnosis.Statement,
		Missing:        append([]string(nil), diagnosis.Missing...),
		Contradictions: append([]string(nil), diagnosis.Contradictions...),
	}
	if diagnosis.NextExperiment != nil {
		presented.NextReason = diagnosis.NextExperiment.Reason
		presented.NextArgv = append([]string(nil), diagnosis.NextExperiment.Argv...)
	}
	return presented
}

// FormatArgv joins an argv template. CurrentModelPlaceholder is replaced when
// model is non-empty; otherwise the placeholder is left intact.
func FormatArgv(argv []string, model string) string {
	parts := make([]string, 0, len(argv))
	for _, arg := range argv {
		if arg == CurrentModelPlaceholder && strings.TrimSpace(model) != "" {
			arg = model
		}
		parts = append(parts, quoteArgIfNeeded(arg))
	}
	return strings.Join(parts, " ")
}

func quoteArgIfNeeded(arg string) string {
	if arg == CurrentModelPlaceholder {
		return arg
	}
	if arg == "" {
		return `""`
	}
	if strings.ContainsAny(arg, " \t\n\r\"'\\$;&|<>*?`") {
		return strconv.Quote(arg)
	}
	return arg
}

// ShortDigest is a display prefix for a content digest. It never implies the
// truncated form is the identity used for comparison.
func ShortDigest(digest string) string {
	value := strings.TrimSpace(digest)
	value = strings.TrimPrefix(value, "sha256:")
	value = strings.TrimPrefix(value, "SHA256:")
	runes := []rune(value)
	if len(runes) <= 12 {
		return value
	}
	return string(runes[:12])
}
