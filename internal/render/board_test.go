package render

import (
	"strings"
	"testing"
)

func TestBoardPlainIsGraphicalPipeSafeAndHonest(t *testing.T) {
	var out strings.Builder
	WriteBoard(&out, Board{Results: 2, Groups: []BoardGroup{
		{
			GPU: "gpu", Driver: "driver", KV: "q8_0", NumCtx: 8192,
			Note: "this machine, current config",
			Rows: []BoardRow{
				{Model: "fast", ParamSize: "8B", Quant: "Q8_0", DecodeMean: 20, PrefillMean: 200, ResidentGB: 8, Repeats: 3, DecodeSeries: []float64{18, 20, 22}, Serves: []string{"fast", "code"}},
				{Model: "slow", ParamSize: "8B", Quant: "Q4_K_M", DecodeMean: 10, PrefillMean: 120, ResidentGB: 5, Repeats: 1, DecodeSeries: []float64{10}, Serves: []string{"small"}},
			},
		},
	}}, "plain")
	got := out.String()
	// The plain stream has no Unicode, so there is no sparkline to show. The
	// series is not flat, so it must not claim to be: absence, not a false
	// stability claim.
	for _, want := range []string{"FITR BOARD", "[########]", "[####....]", "not a rankable result"} {
		if !strings.Contains(got, want) {
			t.Fatalf("plain board missing %q:\n%s", want, got)
		}
	}
	if strings.ContainsRune(got, '\x1b') {
		t.Fatalf("plain board leaked terminal escapes: %q", got)
	}
}

func TestBoardRichUsesColorAndUnicode(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("FORCE_COLOR", "1")
	t.Setenv("FITR_UNICODE", "1")
	var out strings.Builder
	WriteBoard(&out, Board{Results: 1, Groups: []BoardGroup{{
		GPU: "gpu", Driver: "driver", NumCtx: 4096, Note: "this machine",
		Rows: []BoardRow{{Model: "m", DecodeMean: 1, Repeats: 3, DecodeSeries: []float64{1, 2}}},
	}}}, "rich")
	got := out.String()
	if !strings.ContainsRune(got, '\x1b') || !strings.Contains(got, "█") || !strings.Contains(got, "▁█") {
		t.Fatalf("rich board should carry ANSI and unicode graphs:\n%s", got)
	}
}

func TestBoardShowsVerifiedEffectiveContext(t *testing.T) {
	var out strings.Builder
	WriteBoard(&out, Board{Results: 1, Groups: []BoardGroup{{
		GPU: "gpu", Driver: "driver", NumCtx: 8192, EffectiveCtx: 4096,
		ContextState: "adjusted", Note: "verified",
		Rows: []BoardRow{{Model: "m", DecodeMean: 1, Repeats: 3}},
	}}}, "plain")
	if got := out.String(); !strings.Contains(got, "ctx 8192 -> 4096 effective") {
		t.Fatalf("board hid effective context:\n%s", got)
	}
}

func TestBoardSanitizesUntrustedText(t *testing.T) {
	var out strings.Builder
	WriteBoard(&out, Board{Results: 1, Groups: []BoardGroup{{
		GPU: "gpu\x1b[2J\u202e", Driver: "driver\r\nspoof", NumCtx: 8192,
		Note: "safe\x07\x1b]0;forged title\a",
		Rows: []BoardRow{{Model: "model\x1b[31mspoof\x1bPforged payload\x1b\\", DecodeMean: 1, Repeats: 3}},
	}}}, "plain")
	got := out.String()
	if strings.ContainsAny(got, "\x1b\x07\r\u202e") || strings.Contains(got, "forged title") ||
		strings.Contains(got, "forged payload") {
		t.Fatalf("board leaked control bytes: %q", got)
	}
}

func TestSparklineOnlyDrawsWhatItCanSupport(t *testing.T) {
	cases := []struct {
		name    string
		values  []float64
		unicode bool
		want    string
	}{
		// No perceptual ASCII ramp exists, so without Unicode there is no
		// sparkline. `.:-=+*#@` cannot be ordered by height by eye.
		{"ascii is refused", []float64{0, 1, 2, 3, 4}, false, ""},
		{"empty", nil, true, ""},
		{"single point has no shape", []float64{4}, true, ""},
		// The glyphs are normalised to the series min and max, so a run that
		// varied by 0.4% would otherwise render the same dramatic zigzag as one
		// that varied tenfold.
		{"identical values", []float64{4, 4, 4}, true, ""},
		{"spread below the floor", []float64{100, 100.5, 100.2}, true, ""},
		{"real spread", []float64{0, 2, 4}, true, "▁▅█"},
	}
	for _, tc := range cases {
		got, ok := sparkline(tc.values, 7, tc.unicode)
		if got != tc.want || ok != (tc.want != "") {
			t.Errorf("%s: sparkline = %q, %v; want %q", tc.name, got, ok, tc.want)
		}
	}
	if got, ok := sparkline([]float64{0, 1, 2, 3, 4}, 3, true); !ok || len([]rune(got)) != 3 {
		t.Fatalf("compressed sparkline = %q, %v; want 3 cells", got, ok)
	}
}

// The board's rule used to say 104 while a row carrying a full serves list
// reached 123, so the boundary was crossed by the table it bounded.
func TestBoardRowsStayInsideTheirRule(t *testing.T) {
	var out strings.Builder
	WriteBoard(&out, Board{Results: 1, Groups: []BoardGroup{{
		GPU: "AMD Radeon(TM) 780M", Driver: "32.0.31007.5012", NumCtx: 8192,
		Note: "measured on this device",
		Rows: []BoardRow{{
			Model: "a-very-long-model-name-that-overflows:30b-instruct-q4", ParamSize: "30.5B",
			Quant: "Q4_K_M", DecodeMean: 23.16, DecodeSD: 0.44, PrefillMean: 226.6,
			ResidentGB: 20.34, Repeats: 8,
			Serves: []string{"coding", "unattended_agentic", "structured_output", "uncensored"},
		}},
	}}}, "plain")
	for _, line := range strings.Split(out.String(), "\n") {
		if n := len([]rune(line)); n > boardWidth {
			t.Errorf("board line is %d cols against a %d rule: %q", n, boardWidth, line)
		}
	}
}
