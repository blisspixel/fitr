package modelref

import "testing"

func TestSameServedDoesNotGuess(t *testing.T) {
	tests := []struct {
		want, have string
		match      bool
	}{
		{want: "qwen3:30b", have: "qwen3:30b", match: true},
		{want: "qwen3:30b", have: "qwen3:30b:latest", match: true},
		{want: "qwen3:30b:LATEST", have: "qwen3:30b", match: true},
		{want: "qwen3:30b:Latest", have: "qwen3:30b", match: true},
		{want: "qwen3:30b", have: "qwen3:8b", match: false},
		{want: "qwen3:30b", have: "llama3:8b", match: false},
		{want: "qwen", have: "qwen-coder:latest", match: false},
		{want: "qwe", have: "qwen:latest", match: false},
	}
	for _, test := range tests {
		if got := SameServed(test.want, test.have); got != test.match {
			t.Errorf("SameServed(%q, %q) = %v, want %v", test.want, test.have, got, test.match)
		}
	}
}
