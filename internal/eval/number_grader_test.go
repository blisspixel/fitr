package eval

import "testing"

// A grader that rejects a correctly formatted answer measures formatting, not
// capability. "1,234" failed to parse while parseCents, in the same file,
// already stripped separators. Rule-based verifiers lose real recall to
// exactly this class of mismatch.
func TestNumberAnswersAcceptCommonFormatting(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want float64
	}{
		{"1234", 1234},
		{"1,234", 1234},
		{"1,234.50", 1234.50},
		{"$1,234", 1234},
		{"1234.5", 1234.5},
		{" 42 ", 42},
		{"1_000", 1000},
		{"12%", 12},
		{"-1,500", -1500},
	} {
		got, err := parseAnswerNumber(tc.in)
		if err != nil {
			t.Fatalf("parseAnswerNumber(%q) failed: %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("parseAnswerNumber(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// Leniency has a limit: anything that could turn a wrong answer into a right
// one must still fail.
func TestNumberAnswersStillRejectNonNumbers(t *testing.T) {
	for _, in := range []string{"", "  ", "twelve", "1.2.3", "12abc", "N/A", "1,2,3.4.5"} {
		if got, err := parseAnswerNumber(in); err == nil {
			t.Fatalf("parseAnswerNumber(%q) accepted it as %v", in, got)
		}
	}
}
