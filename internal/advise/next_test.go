package advise

import "testing"

func TestResultNextGrammar(t *testing.T) {
	cases := []struct {
		repeats int
		ctx     int
		level   string
		blocked bool
		want    string
	}{
		{3, 8192, "default", true, "fitr diag m"},
		{1, 8192, "default", false, "fitr run m -k 3"},
		{3, 4096, "default", false, "fitr apply m"},
		{3, 8192, "quick", false, "fitr run m"},
		{3, 8192, "default", false, "fitr view m"},
	}
	for _, cs := range cases {
		if got := ResultNext("m", cs.repeats, cs.ctx, cs.level, cs.blocked); got != cs.want {
			t.Fatalf("ResultNext(%d,%d,%q,%v) = %q, want %q",
				cs.repeats, cs.ctx, cs.level, cs.blocked, got, cs.want)
		}
	}
}

func TestAdviseNextGrammar(t *testing.T) {
	if got := AdviseNext("m", Compatible, 0); got != "fitr run m" {
		t.Fatalf("compatible = %q", got)
	}
	if got := AdviseNext("m", LowMemory, 4096); got != "fitr run m --ctx 4096" {
		t.Fatalf("low memory = %q", got)
	}
	if got := AdviseNext("m", Incompatible, 0); got != "" {
		t.Fatalf("incompatible must not invent a run: %q", got)
	}
}
