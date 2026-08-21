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
	for _, want := range []string{"FITR BOARD", "[########]", "[####....]", ".+@", "not a rankable result"} {
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

func TestSparklineBoundsAndCompression(t *testing.T) {
	if got := sparkline(nil, 7, false); got != "-" {
		t.Fatalf("empty sparkline = %q", got)
	}
	if got := sparkline([]float64{4, 4, 4}, 7, false); got != "===" {
		t.Fatalf("flat sparkline = %q", got)
	}
	if got := sparkline([]float64{0, 1, 2, 3, 4}, 3, false); got != ".+@" {
		t.Fatalf("compressed sparkline = %q", got)
	}
	if got := sparkline([]float64{1, 2}, 1, false); got != "=" {
		t.Fatalf("one-cell sparkline = %q", got)
	}
}
