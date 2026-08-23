package strictjson

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestValidateRejectsAmbiguousAndConcatenatedJSON(t *testing.T) {
	for _, tc := range []struct {
		name string
		data string
		want string
	}{
		{"duplicate root name", `{"model":"a","model":"b"}`, `duplicate JSON object name "model"`},
		{"duplicate nested name", `{"usage":{"tokens":1,"tokens":2}}`, `duplicate JSON object name "tokens"`},
		{"duplicate escaped name", `{"model":1,"mod\u0065l":2}`, `duplicate JSON object name "model"`},
		{"duplicate in array", `[{"id":1,"id":2}]`, `duplicate JSON object name "id"`},
		{"concatenated", `{} []`, "content after"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := Validate([]byte(tc.data)); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestValidateAcceptsSameNameInSeparateObjects(t *testing.T) {
	data := []byte(`{"left":{"id":1},"right":{"id":2},"items":[{"id":3},{"id":4}]}`)
	if err := Validate(data); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
}

func FuzzValidate(f *testing.F) {
	for _, seed := range []string{`null`, `{"a":1}`, `{"a":1,"a":2}`, `[{"x":true}]`, `{`} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if Validate(data) == nil && !json.Valid(data) {
			t.Fatal("strict validation accepted JSON rejected by encoding/json")
		}
	})
}
