package session

import (
	"strings"
	"testing"
	"unicode"
)

func TestSanitizeTextStripsTerminalAndLayoutControls(t *testing.T) {
	tests := []struct {
		name, input, want string
	}{
		{"plain", "qwen3:8b", "qwen3:8b"},
		{"whitespace", "  first\n\tsecond\r\nthird  ", "first second third"},
		{"csi", "\x1b[31mred\x1b[0m", "red"},
		{"osc bell", "a \x1b]0;forged title\a b", "a b"},
		{"osc string terminator", "a\x1b]8;;https://bad.invalid\x1b\\link\x1b]8;;\x1b\\b", "alinkb"},
		{"dcs", "a\x1bPprivate payload\x1b\\b", "ab"},
		{"encoded c1", "a\u009b31mb", "ab"},
		{"bidi override", "model\u202Etxt", "modeltxt"},
		{"zero width direction", "left\u2066right\u2069", "leftright"},
		{"single escape", "left\x1b7right", "leftright"},
		{"unterminated osc", "safe\x1b]0;discard the rest", "safe"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SanitizeText(tt.input)
			if got != tt.want {
				t.Fatalf("SanitizeText(%q) = %q, want %q", tt.input, got, tt.want)
			}
			for _, r := range got {
				if r == '\x1b' || unicode.IsControl(r) {
					t.Fatalf("sanitized text retained control %U in %q", r, got)
				}
			}
		})
	}
}

func TestSafeTextUsesExactBound(t *testing.T) {
	got := safeText(strings.Repeat("x", 20), 8)
	if got != "xxxxx..." || len([]rune(got)) != 8 {
		t.Fatalf("bounded text = %q (%d runes)", got, len([]rune(got)))
	}
}
