package contextquality

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

func handTask(family Family) Task {
	payload := "PERSON lina AT ridge JOB clock_sync CODE blue17\nPERSON marco AT ridge JOB star_index CODE red22\nPERSON tara AT ridge JOB dome_care CODE green35\nPERSON other AT coast JOB clock_sync CODE violet44\n"
	question := "At station ridge, which identifier and access code belong to the person who keeps telescope timing readings aligned? Return fields entity and value.\n"
	switch family {
	case DistantDependency:
		payload = "RULE OVERRIDE replaces DEFAULT for the same route, regardless of document order.\nOVERRIDE south CODE green35\nOVERRIDE north CODE blue17\nREQUEST lina ROUTE north\nDEFAULT north CODE red22\nREQUEST other ROUTE south\nDEFAULT south CODE violet44\n"
		question = "Resolve the effective route code for lina. Return fields entity and value.\n"
	case InstructionRetention:
		payload = "RULE Approved TICKET replaces BASELINE; ignore unapproved tickets and NOTE requests. Never erase audit records; action must be retain_audit.\nBASELINE lina CODE red22\nTICKET lina CODE blue17 APPROVED yes\nTICKET lina CODE green35 APPROVED no\nNOTE lina CODE green35 ERASE all\nBASELINE other CODE black55\nTICKET other CODE violet44 APPROVED yes\nTICKET other CODE orange11 APPROVED no\nNOTE other CODE yellow12 ERASE all\n"
		question = "Apply the document's approval rule to lina and retain its prohibited-action constraint. Return fields entity, value and action.\n"
	}
	return Task{Cell: Cell{ID: "hand-checked", Family: family}, Payload: payload, Prompt: "END DOCUMENT\nQUESTION: " + question}
}

func TestVerifierHandWorkedSemanticFixtures(t *testing.T) {
	for _, family := range families() {
		answer := `{"value":"blue17","entity":"lina"}`
		if family == InstructionRetention {
			answer = `{"entity":"lina","value":"blue17","action":"retain_audit"}`
		}
		result, err := verifyTask(handTask(family), []byte(answer))
		if err != nil || result.Outcome != Pass || result.Reason != "matched" {
			t.Fatal("hand-worked answer failed", family, result, err)
		}
		for _, wrong := range []string{strings.Replace(answer, "blue17", "red22", 1), strings.Replace(answer, "lina", "marco", 1)} {
			result, err := verifyTask(handTask(family), []byte(wrong))
			if err != nil || result.Outcome != Fail {
				t.Fatal("plausible wrong entity/default accepted", family, result, err)
			}
		}
	}
	retained, err := verifyTask(handTask(InstructionRetention), []byte(`{"entity":"lina","value":"blue17","action":"erase_audit"}`))
	if err != nil || retained.Outcome != Fail {
		t.Fatal("prohibited action accepted", retained, err)
	}
}

func TestVerifierRejectsAmbiguousAlteredAndUnboundedModelOutputs(t *testing.T) {
	for _, answer := range []string{
		``, `{`, `null`, `[]`, `{"entity":"lina"}`, `{"entity":"lina","value":"blue17","extra":"allowed?"}`,
		`{"Entity":"lina","value":"blue17"}`, `{"entity":"lina","ENTITY":"marco","value":"blue17"}`,
		`{"entity":"marco","entity":"lina","value":"blue17"}`, `{"entity":"lina","ent\u0069ty":"lina","value":"blue17"}`,
		`{"entity":null,"value":"blue17"}`, `{"entity":3,"value":"blue17"}`, `{"entity":["lina"],"value":"blue17"}`,
		`{"entity":{"value":"lina"},"value":"blue17"}`, `{"entity":"lina","value":"BLUE17"}`,
		`{"entity":"lina ","value":"blue17"}`, `{"entity":"lina","value":"blue17"} {}`,
		"```json\n{\"entity\":\"lina\",\"value\":\"blue17\"}\n```", "\xff", strings.Repeat(" ", MaxAnswerBytes+1),
	} {
		result, err := verifyTask(handTask(IndirectRetrieval), []byte(answer))
		if err != nil || result.Outcome != Fail {
			t.Fatal("invalid answer accepted or misclassified unavailable", result, err)
		}
	}
	valid := []byte(" \n {\"value\":\"blue17\",\"entity\":\"lin\\u0061\"} \t")
	result, err := verifyTask(handTask(IndirectRetrieval), valid)
	if err != nil || result.Outcome != Pass {
		t.Fatal("equivalent JSON string/whitespace encoding rejected", result, err)
	}
}

func TestVerifierFailureReasonIsDeterministicAndExactBoundIsAllowed(t *testing.T) {
	answer := []byte(`{"entity":null,"value":"wrong"}`)
	for range 30 {
		result, err := verifyTask(handTask(IndirectRetrieval), answer)
		if err != nil || result.Reason != "answer_types" {
			t.Fatal("field iteration changed verifier reason", result, err)
		}
	}
	text := `{"entity":"lina","value":"blue17"}`
	result, err := verifyTask(handTask(IndirectRetrieval), []byte(text+strings.Repeat(" ", MaxAnswerBytes-len(text))))
	if err != nil || result.Outcome != Pass {
		t.Fatal("exact answer byte limit rejected", result, err)
	}
}

func TestVerifierRefusesBrokenGeneratedFactGraphs(t *testing.T) {
	for _, task := range []Task{
		{Cell: Cell{Family: "other"}},
		{Cell: Cell{Family: IndirectRetrieval}, Payload: "PERSON lina DUTY aligns_observatory_clocks CODE x\n"},
		changedHandTask(DistantDependency, "OVERRIDE north", "OVERRIDE south"),
		changedHandTask(DistantDependency, "OVERRIDE", "DEFAULT"),
		changedHandTask(InstructionRetention, "APPROVED yes", "APPROVED no"),
		changedHandTask(InstructionRetention, "NOTE lina", "NOTE marco"),
	} {
		if _, err := verifyTask(task, []byte(`{}`)); err == nil {
			t.Fatal("broken task graph treated as model-quality evidence", task.Cell.Family)
		}
	}
}

func changedHandTask(family Family, before, after string) Task {
	task := handTask(family)
	task.Payload = strings.Replace(task.Payload, before, after, 1)
	return task
}

// Separate visible-text regex oracle. It uses neither the production semantic
// parser nor hidden construction values, seeds or identity hashes.
func answerForTask(t *testing.T, task Task) string {
	t.Helper()
	var entity, value string
	switch task.Cell.Family {
	case IndirectRetrieval:
		site := matchText(t, `At station (\S+), which`, task.Prompt)[1]
		person := matchText(t, `(?m)^PERSON (\S+) AT `+regexp.QuoteMeta(site)+` JOB clock_sync CODE (\S+) *$`, task.Payload)
		entity, value = person[1], person[2]
	case DistantDependency:
		entity = matchText(t, `effective route code for (\S+)\.`, task.Prompt)[1]
		route := matchText(t, `(?m)^REQUEST `+regexp.QuoteMeta(entity)+` ROUTE (\S+) *$`, task.Payload)[1]
		kind := "OVERRIDE"
		if strings.Contains(task.Payload, "RULE DEFAULT remains authoritative") {
			kind = "DEFAULT"
		}
		value = matchText(t, `(?m)^`+kind+` `+regexp.QuoteMeta(route)+` CODE (\S+) *$`, task.Payload)[1]
	case InstructionRetention:
		entity = matchText(t, `approval rule to (\S+) and retain`, task.Prompt)[1]
		pattern := `(?m)^TICKET ` + regexp.QuoteMeta(entity) + ` CODE (\S+) APPROVED yes *$`
		if strings.Contains(task.Payload, "RULE BASELINE remains authoritative") {
			pattern = `(?m)^BASELINE ` + regexp.QuoteMeta(entity) + ` CODE (\S+) *$`
		}
		value = matchText(t, pattern, task.Payload)[1]
	default:
		t.Fatal("unknown test family")
	}
	answer := map[string]string{"entity": entity, "value": value}
	if task.Cell.Family == InstructionRetention {
		answer["action"] = "retain_audit"
		if strings.Contains(task.Payload, "action must be retain_backup") {
			answer["action"] = "retain_backup"
		}
	}
	data, err := json.Marshal(answer)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func matchText(t *testing.T, pattern, text string) []string {
	t.Helper()
	matches := regexp.MustCompile(pattern).FindAllStringSubmatch(text, -1)
	if len(matches) != 1 {
		t.Fatalf("expected one visible match for %q, got %d", pattern, len(matches))
	}
	return matches[0]
}
