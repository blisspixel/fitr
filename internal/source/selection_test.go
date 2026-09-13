package source

import (
	"testing"
)

func TestSelectedGGUFGroupRequiresOneCompleteModel(t *testing.T) {
	size := int64(100)
	file := func(name string) FileMetadata {
		return FileMetadata{Path: name, State: "present", SizeBytes: &size, DeclaredSHA256: "declared"}
	}
	for _, test := range []struct {
		files []FileMetadata
		valid bool
	}{
		{nil, false},
		{[]FileMetadata{file("model.gguf")}, true},
		{[]FileMetadata{file("model.gguf"), file("model.gguf")}, false},
		{[]FileMetadata{file("a.gguf"), file("b.gguf")}, false},
		{[]FileMetadata{file("model-00001-of-00002.gguf")}, false},
		{[]FileMetadata{file("model-00002-of-00002.gguf"), file("model-00001-of-00002.gguf")}, true},
		{[]FileMetadata{file("model-00001-of-00002.gguf"), file("different-00002-of-00002.gguf")}, false},
		{[]FileMetadata{file("model-00000-of-00001.gguf")}, false},
		{[]FileMetadata{file("mmproj.gguf")}, false},
		{[]FileMetadata{file("model.bin")}, false},
		{[]FileMetadata{{Path: "model.gguf", State: "missing"}}, false},
	} {
		group, reason := SelectedGGUFGroup(test.files)
		if (reason == "") != test.valid || (len(group) > 0) != test.valid {
			t.Errorf("%v: group %v, reason %q", test.files, group, reason)
		}
	}
}
