package analysis

import "testing"

func TestDiagnosisSupportLabelDoesNotSayObserved(t *testing.T) {
	tests := []struct {
		support DiagnosisSupport
		want    string
	}{
		{DiagnosisDirect, "direct"},
		{"", "direct"},
		{DiagnosisInterventionSupported, "intervention-supported"},
		{DiagnosisSuggestive, "suggestive"},
		{DiagnosisContradicted, "contradicted"},
		{DiagnosisBlocked, "blocked"},
	}
	for _, test := range tests {
		if got := DiagnosisSupportLabel(test.support); got != test.want {
			t.Fatalf("support %q = %q, want %q", test.support, got, test.want)
		}
		if got := DiagnosisSupportLabel(test.support); got == "observed" {
			t.Fatalf("support class must not collapse to observed: %q", test.support)
		}
	}
}

func TestPresentDiagnosisKeepsSupportMissingAndNextExperiment(t *testing.T) {
	presented := PresentDiagnosis(Diagnosis{
		Code:      DiagnosisPartialPlacement,
		Support:   DiagnosisDirect,
		Statement: "the runtime reported a partial accelerator share at the exact-context allocation point",
		Missing:   []string{"layer placement"},
		NextExperiment: &Action{
			Code: ActionOpenBoard, Argv: []string{"fitr", "board"},
			Reason: "compare only with compatible exact-context allocation receipts",
		},
	})
	if presented.Support != "direct" || presented.Label != "allocation attribution" {
		t.Fatalf("presentation identity = %+v", presented)
	}
	if presented.Headline != "allocation attribution: the runtime reported a partial accelerator share at the exact-context allocation point" {
		t.Fatalf("headline = %q", presented.Headline)
	}
	if len(presented.Missing) != 1 || presented.Missing[0] != "layer placement" {
		t.Fatalf("missing = %v", presented.Missing)
	}
	if presented.NextReason == "" || FormatArgv(presented.NextArgv, "qwen") != "fitr board" {
		t.Fatalf("next experiment = %+v", presented)
	}
}

func TestFormatArgvSubstitutesCurrentModel(t *testing.T) {
	argv := []string{"fitr", "run", CurrentModelPlaceholder, "--ctx", "8192"}
	if got := FormatArgv(argv, "qwen3:8b"); got != "fitr run qwen3:8b --ctx 8192" {
		t.Fatalf("substituted argv = %q", got)
	}
	if got := FormatArgv(argv, ""); got != "fitr run "+CurrentModelPlaceholder+" --ctx 8192" {
		t.Fatalf("placeholder argv = %q", got)
	}
}

func TestShortDigestIsDisplayOnly(t *testing.T) {
	if got := ShortDigest("sha256:abcdef0123456789ffff"); got != "abcdef012345" {
		t.Fatalf("short digest = %q", got)
	}
	if got := ShortDigest("abcd"); got != "abcd" {
		t.Fatalf("short digest of a short value = %q", got)
	}
}
