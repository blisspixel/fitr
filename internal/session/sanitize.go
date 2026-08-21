package session

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// SanitizeText turns untrusted text into one terminal-safe presentation line.
// It strips ANSI CSI, OSC, DCS, SOS, PM, APC, C0/C1 controls, bidi controls,
// and zero-width direction markers. Whitespace is collapsed deliberately so
// metadata cannot alter layout.
func SanitizeText(input string) string {
	s := strings.NewReplacer(
		"\u0090", "\x1bP",
		"\u0098", "\x1bX",
		"\u009b", "\x1b[",
		"\u009c", "\x1b\\",
		"\u009d", "\x1b]",
		"\u009e", "\x1b^",
		"\u009f", "\x1b_",
	).Replace(input)
	s = strings.ToValidUTF8(s, "\uFFFD")

	var out strings.Builder
	space := false
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			i = consumeEscape(s, i)
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		if unsafeFormatRune(r) {
			continue
		}
		if unicode.IsSpace(r) {
			if out.Len() > 0 {
				space = true
			}
			continue
		}
		if unicode.IsControl(r) {
			continue
		}
		if space {
			out.WriteByte(' ')
			space = false
		}
		out.WriteRune(r)
	}
	return strings.TrimSpace(out.String())
}

func consumeEscape(s string, start int) int {
	i := start + 1
	if i >= len(s) {
		return i
	}
	switch s[i] {
	case '[':
		return consumeCSI(s, i+1)
	case ']':
		return consumeStringControl(s, i+1, true)
	case 'P', 'X', '^', '_':
		return consumeStringControl(s, i+1, false)
	case '\\':
		return i + 1
	}
	for i < len(s) && s[i] >= 0x20 && s[i] <= 0x2f {
		i++
	}
	if i < len(s) {
		i++
	}
	return i
}

func consumeCSI(s string, i int) int {
	for i < len(s) {
		b := s[i]
		i++
		if b >= 0x40 && b <= 0x7e {
			break
		}
	}
	return i
}

func consumeStringControl(s string, i int, bellTerminates bool) int {
	for i < len(s) {
		if bellTerminates && s[i] == '\a' {
			return i + 1
		}
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '\\' {
			return i + 2
		}
		i++
	}
	return i
}

func unsafeFormatRune(r rune) bool {
	switch {
	case r >= 0x200b && r <= 0x200f:
		return true
	case r >= 0x202a && r <= 0x202e:
		return true
	case r >= 0x2060 && r <= 0x2069:
		return true
	case r == 0xfeff:
		return true
	default:
		return false
	}
}

func safeText(input string, limit int) string {
	clean := SanitizeText(input)
	if limit <= 0 {
		return ""
	}
	runes := []rune(clean)
	if len(runes) <= limit {
		return clean
	}
	if limit <= 3 {
		return string(runes[:limit])
	}
	return string(runes[:limit-3]) + "..."
}
