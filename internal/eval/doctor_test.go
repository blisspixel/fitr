package eval

import "testing"

func TestDivergence(t *testing.T) {
	if id, d, _ := Divergence([]string{"abc", "abc", "abc"}); !id || d != 1 {
		t.Fatalf("identical runs: got identical=%v distinct=%d", id, d)
	}
	id, d, first := Divergence([]string{"abcdef", "abcXef", "abcdef"})
	if id || d != 2 {
		t.Fatalf("got identical=%v distinct=%d", id, d)
	}
	if first != 3 {
		t.Fatalf("first divergence = %d, want 3", first)
	}
	// Differ only by length: divergence is at the shorter length.
	if _, _, first := Divergence([]string{"abc", "abcdef"}); first != 3 {
		t.Fatalf("length-only divergence = %d, want 3", first)
	}
}

func TestReportDeterminismStates(t *testing.T) {
	var r DoctorResult
	if ok := reportDeterminism(&r, "x", []string{"a", "a"}, "why"); !ok {
		t.Fatal("identical outputs must report deterministic")
	}
	if r.Checks[0].State != "PASS" {
		t.Fatalf("state = %s, want PASS", r.Checks[0].State)
	}
	if ok := reportDeterminism(&r, "y", []string{"a", "b"}, "why"); ok {
		t.Fatal("diverging outputs must not report deterministic")
	}
	// Nondeterminism is a WARN, never a FAIL: repeats-and-intervals survive
	// it. A FAIL would tell people their box is broken when it is merely noisy.
	if r.Checks[1].State != "WARN" {
		t.Fatalf("state = %s, want WARN", r.Checks[1].State)
	}
}
