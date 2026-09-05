package contextquality

import (
	"crypto/sha256"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestGeneratedCellsHaveExactASCIIBytesAndIndependentAnswers(t *testing.T) {
	plan := testPlan(t, 2048, 2049, 65535, 65536)
	if len(plan.Cells) != 36 {
		t.Fatal("wrong fixed task denominator", len(plan.Cells))
	}
	seen := make(map[string]bool)
	for index, cell := range plan.Cells {
		task, err := plannedTask(plan, index+1)
		if err != nil || !reflect.DeepEqual(task.Cell, cell) {
			t.Fatal("cell regeneration changed identity", index, err)
		}
		assertExactTaskBytes(t, task)
		assertSemanticVolume(t, task)
		assertTaskSpans(t, task)
		key := fmt.Sprintf("%d/%s/%s", cell.PayloadUTF8Bytes, cell.Family, cell.Position)
		if seen[key] || seen[cell.ID] || seen[cell.InstanceSHA256] {
			t.Fatal("duplicate cell or task instance", key)
		}
		seen[key], seen[cell.ID], seen[cell.InstanceSHA256] = true, true, true
		result, err := verifyTask(task, []byte(answerForTask(t, task)))
		if err != nil || result.Outcome != Pass {
			t.Fatal("independent visible-document solution rejected", key, result, err)
		}
	}
}

func assertExactTaskBytes(t *testing.T, task Task) {
	t.Helper()
	if len(task.Payload) != task.Cell.PayloadUTF8Bytes || len(task.Prompt) != task.Cell.PromptUTF8Bytes {
		t.Fatal("declared byte lengths differ from the exact strings")
	}
	for _, character := range task.Prompt {
		if character > 126 || (character < 32 && character != '\n') {
			t.Fatal("task is not printable ASCII with LF", character)
		}
	}
	if strings.Count(task.Prompt, task.Payload) != 1 {
		t.Fatal("prompt does not contain one exact payload")
	}
	wantPayload := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(task.Payload)))
	wantPrompt := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(task.Prompt)))
	if task.Cell.PayloadSHA256 != wantPayload || task.Cell.PromptSHA256 != wantPrompt {
		t.Fatal("payload/prompt hashes are not hashes of their actual bytes")
	}
}

func assertSemanticVolume(t *testing.T, task Task) {
	t.Helper()
	padding := 0
	records := 0
	for _, line := range strings.SplitAfter(task.Payload, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			padding += len(line)
			continue
		}
		fields := strings.Fields(trimmed)
		switch fields[0] {
		case "PERSON", "REQUEST", "DEFAULT", "OVERRIDE", "BASELINE", "TICKET", "NOTE", "RULE":
			records++
		default:
			t.Fatal("non-domain filler record", trimmed)
		}
	}
	// Reserved principal rows can have trailing whitespace, but their bytes
	// still carry a domain record. Only the last short row may be inert.
	if padding > 95 || records < task.Cell.PayloadUTF8Bytes/96-2 {
		t.Fatal("semantic corpus replaced with sparse padding", padding, records)
	}
}

func assertTaskSpans(t *testing.T, task Task) {
	t.Helper()
	fraction := map[Position]int{Beginning: 1, Middle: 4, End: 7}[task.Cell.Position]
	wantPrincipal := task.Cell.PayloadUTF8Bytes * fraction / 8 / 96 * 96
	priorEnd := 0
	found := false
	for _, span := range task.Cell.Spans {
		if span.Offset < priorEnd || span.Length <= 0 || span.Offset+span.Length > len(task.Payload) {
			t.Fatal("overlapping or invalid fact span", span)
		}
		text := task.Payload[span.Offset : span.Offset+span.Length]
		if !strings.HasSuffix(text, "\n") || strings.HasPrefix(text, "#") {
			t.Fatal("span does not locate its actual fact", span)
		}
		if span.Name == "principal" {
			found = span.Offset == wantPrincipal
		}
		if span.Name == "later_note" && span.Offset <= wantPrincipal {
			t.Fatal("conflicting note is not later than the principal fact")
		}
		priorEnd = span.Offset + span.Length
	}
	if !found {
		t.Fatal("principal fact is not at its declared position", task.Cell.Position)
	}
}

func TestGenerationPairsExactPlansAndCopiesCallerState(t *testing.T) {
	policy := testPolicy(t)
	plan, err := NewPlan(policy, testSeed)
	if err != nil {
		t.Fatal(err)
	}
	paired, err := NewPlan(policy, testSeed)
	if err != nil || !reflect.DeepEqual(plan, paired) {
		t.Fatal("identical comparison inputs do not pair", err)
	}
	policy.PayloadUTF8Bytes[0] = 2049
	if plan.Policy.PayloadUTF8Bytes[0] != 2048 {
		t.Fatal("plan retains mutable policy input")
	}
	changedPolicy, err := NewPlan(policy, testSeed)
	if err != nil {
		t.Fatal(err)
	}
	changedSeed, err := NewPlan(plan.Policy, "1123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	for _, changed := range []Plan{changedPolicy, changedSeed} {
		if changed.PlanSHA256 == plan.PlanSHA256 || changed.Cells[0].ID == plan.Cells[0].ID || changed.Cells[0].PayloadSHA256 == plan.Cells[0].PayloadSHA256 {
			t.Fatal("changed corpus declaration reused sealed identity")
		}
	}
	task, err := Generate(plan, 1)
	if err != nil {
		t.Fatal(err)
	}
	task.Cell.Spans[0].Offset++
	if !reflect.DeepEqual(plan, paired) {
		t.Fatal("generated task aliases plan metadata")
	}
	answer := answerForTask(t, task)
	result, err := Verify(plan, 1, []byte(answer))
	if err != nil || result.Outcome != Pass {
		t.Fatal("public verifier failed deterministic task", result, err)
	}
}
