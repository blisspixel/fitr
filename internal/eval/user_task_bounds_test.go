package eval

import (
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeUserTask(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func csvTask(rows string) string {
	return `{"id":"probe","kind":"check","family":"csv_strict","need":"structured_output",` +
		`"num_predict":64,"params":{"rows":` + rows + `}}`
}

// A task file is user input, and its count knobs index fixed pools. A file
// asking for -1 rows, or more rows than the pool holds, crashed generation:
// make with a negative length, and a permutation sliced past its end. fitr
// panicked on a file the user dropped in ~/.fitr/tasks.
func TestUserTaskCountsAreRejectedNotCrashed(t *testing.T) {
	for _, rows := range []string{"-1", "0", "9999", "1e18", "-1e18"} {
		t.Run("rows="+rows, func(t *testing.T) {
			dir := t.TempDir()
			writeUserTask(t, dir, "probe.json", csvTask(rows))
			checks, err := LoadUserChecks(dir)
			if err == nil {
				t.Fatalf("rows=%s was accepted as %d check(s); it cannot be satisfied", rows, len(checks))
			}
			if !strings.Contains(err.Error(), "params.rows") {
				t.Fatalf("diagnostic does not name the offending field: %v", err)
			}
		})
	}
}

func TestUserTaskWithARealCountStillLoads(t *testing.T) {
	dir := t.TempDir()
	writeUserTask(t, dir, "probe.json", csvTask("4"))
	checks, err := LoadUserChecks(dir)
	if err != nil || len(checks) != 1 {
		t.Fatalf("a valid task did not load: %d checks, err=%v", len(checks), err)
	}
	inst := Generate(checks[0], 1)
	if inst.Prompt == "" || inst.Canon == "" {
		t.Fatal("valid task generated an empty instance")
	}
	if n := strings.Count(inst.Canon, "\n"); n != 5 { // header + 4 rows
		t.Fatalf("canon has %d lines, want header plus 4 rows:\n%s", n, inst.Canon)
	}
}

// Generation must not panic even when validation is bypassed, because pick is
// shared by every family and the pool is the only real bound.
func TestGenerateSurvivesOutOfRangeCountsDirectly(t *testing.T) {
	for _, rows := range []any{-1, 0, 9999, float64(1e18), float64(-1e18)} {
		cs := CheckSpec{ID: "direct", Kind: "check", Family: "csv_strict",
			Need: "structured_output", NumPredict: 64,
			Params: map[string]any{"rows": rows}}
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("rows=%v panicked: %v", rows, r)
				}
			}()
			_ = Generate(cs, 1)
		}()
	}
}

func TestPickIsBoundedByItsPool(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	pool := []string{"a", "b", "c"}
	for _, k := range []int{-5, 0, 1, 3, 4, 1 << 30} {
		got := pick(rng, pool, k)
		if len(got) > len(pool) {
			t.Fatalf("pick(k=%d) returned %d items from a pool of %d", k, len(got), len(pool))
		}
		if k > 0 && k <= len(pool) && len(got) != k {
			t.Fatalf("pick(k=%d) returned %d items", k, len(got))
		}
		if k <= 0 && len(got) != 0 {
			t.Fatalf("pick(k=%d) returned %d items, want none", k, len(got))
		}
	}
	if got := pick(rng, []string(nil), 3); len(got) != 0 {
		t.Fatalf("picking from an empty pool returned %d items", len(got))
	}
}

func TestPIntClampsRatherThanConvertingOutOfRange(t *testing.T) {
	for _, tc := range []struct {
		in   any
		want int
	}{
		{float64(1e18), 1 << 31 &^ 1}, // clamped to MaxInt32, not wrapped
		{float64(-1e18), -(1 << 31)},
		{float64(4), 4},
		{float64(-1), -1},
	} {
		got := pInt(map[string]any{"k": tc.in}, "k", 99)
		if (tc.in == float64(1e18) && got <= 0) || (tc.in == float64(-1e18) && got >= 0) {
			t.Fatalf("pInt(%v) = %d, sign flipped by an undefined conversion", tc.in, got)
		}
		if tc.in == float64(4) && got != 4 {
			t.Fatalf("pInt(4) = %d", got)
		}
	}
	if got := pInt(map[string]any{}, "missing", 7); got != 7 {
		t.Fatalf("absent parameter = %d, want the default 7", got)
	}
}
