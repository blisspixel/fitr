package advise

import "testing"

// These keys are llama.cpp's own extension and are absent from the GGUF
// specification, so a reader that does not know them silently falls back to a
// runtime default the author did not choose.
func TestAuthorSamplingIsReadFromTheArtifact(t *testing.T) {
	s := AuthorSamplingFromKVs(map[string]any{
		"general.sampling.temp":           float32(0.6),
		"general.sampling.top_k":          uint32(20),
		"general.sampling.top_p":          float32(0.95),
		"general.sampling.min_p":          float32(0),
		"general.sampling.penalty_repeat": float32(1.05),
		"general.sampling.sequence":       "penalties;top_k;top_p;temp",
	})
	if !s.Declared() {
		t.Fatal("declared settings were not read")
	}
	if s.Temp == nil || *s.Temp < 0.59 || *s.Temp > 0.61 {
		t.Fatalf("temp = %v", s.Temp)
	}
	if s.TopK == nil || *s.TopK != 20 {
		t.Fatalf("top_k = %v", s.TopK)
	}
	// A declared zero is a real choice and must survive as one.
	if s.MinP == nil || *s.MinP != 0 {
		t.Fatalf("a declared min_p of zero was dropped: %v", s.MinP)
	}
	if got := s.Summary(); got == "" {
		t.Fatal("declared settings produced no summary")
	}
}

// Most artifacts declare nothing. That is the answer, not a reason to print a
// runtime's default as though the author had chosen it.
func TestUndeclaredSamplingStaysAbsent(t *testing.T) {
	if s := AuthorSamplingFromKVs(map[string]any{"general.architecture": "llama"}); s != nil {
		t.Fatalf("an artifact declaring nothing produced %+v", s)
	}
	var absent *AuthorSampling
	if absent.Declared() {
		t.Fatal("a nil declaration reported as declared")
	}
	if absent.Summary() != "" {
		t.Fatalf("a nil declaration produced a summary: %q", absent.Summary())
	}
}

// A key present but of an unexpected type is not a declaration.
func TestUnreadableSamplingValuesAreNotInvented(t *testing.T) {
	s := AuthorSamplingFromKVs(map[string]any{
		"general.sampling.temp":  "hot",
		"general.sampling.top_k": []any{1, 2},
	})
	if s != nil {
		t.Fatalf("unreadable values became a declaration: %+v", s)
	}
}
