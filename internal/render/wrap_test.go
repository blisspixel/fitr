package render

import (
	"reflect"
	"testing"
)

func TestWrapDisplayCellsPreservesGraphemes(t *testing.T) {
	for _, tc := range []struct {
		text  string
		width int
		want  []string
	}{
		{"模型模型", 4, []string{"模型", "模型"}},
		{"e\u0301e\u0301e\u0301", 2, []string{"e\u0301e\u0301", "e\u0301"}},
		{"模型", 1, []string{"?", "?"}},
		{"a 模型", 4, []string{"a", "模型"}},
	} {
		if got := wrap(tc.text, tc.width); !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("wrap(%q, %d) = %q, want %q", tc.text, tc.width, got, tc.want)
		}
	}
}
