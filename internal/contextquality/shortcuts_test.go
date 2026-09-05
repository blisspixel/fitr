package contextquality

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"
)

func TestCompetingRecordsDefeatPositionAndRuleBlindShortcuts(t *testing.T) {
	attempts := make(map[string]int)
	failures := make(map[string]int)
	policies := make(map[string]bool)
	for seed := range 8 {
		plan, err := NewPlan(testPolicy(t), fmt.Sprintf("%032x", seed))
		if err != nil {
			t.Fatal(err)
		}
		for index, cell := range plan.Cells {
			if cell.Family == IndirectRetrieval {
				continue
			}
			task, err := plannedTask(plan, index+1)
			if err != nil {
				t.Fatal(err)
			}
			answer := parsedTestAnswer(t, answerForTask(t, task))
			for name, candidate := range shortcutAnswers(t, task, answer) {
				data, _ := json.Marshal(candidate)
				result, err := verifyTask(task, data)
				if err != nil {
					t.Fatal("shortcut fixture broke task grammar", err)
				}
				attempts[name]++
				if result.Outcome == Fail {
					failures[name]++
				}
			}
			policies[strings.TrimSpace(strings.Split(task.Payload, "\n")[0])] = true
		}
	}
	for name, count := range attempts {
		if count != 48 || failures[name] == 0 {
			t.Fatal("shortcut solved every required cell", name, count, failures[name])
		}
	}
	if len(attempts) != 7 || len(policies) != 6 {
		t.Fatal("seeded corpus did not exercise every rule and shortcut", len(attempts), len(policies))
	}
}

func parsedTestAnswer(t *testing.T, answer string) map[string]string {
	t.Helper()
	var result map[string]string
	if err := json.Unmarshal([]byte(answer), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func shortcutAnswers(t *testing.T, task Task, correct map[string]string) map[string]map[string]string {
	t.Helper()
	pattern := `(?m)^OVERRIDE (\S+) CODE (\S+) *$`
	name := "override"
	if task.Cell.Family == InstructionRetention {
		pattern = `(?m)^TICKET (\S+) CODE (\S+) APPROVED yes *$`
		name = "approved"
	}
	rows := regexp.MustCompile(pattern).FindAllStringSubmatch(task.Payload, -1)
	if len(rows) < 3 {
		t.Fatal("task leaves a sole answer-bearing record", name, len(rows))
	}
	answers := make(map[string]map[string]string)
	for label, row := range map[string][]string{"first_": rows[0], "last_": rows[len(rows)-1]} {
		candidate := cloneTest(t, correct)
		candidate["value"] = row[2]
		answers[label+name] = candidate
	}
	// This stronger shortcut knows the right entity/route but ignores the
	// early precedence rule, which must still change some correct answers.
	blind := cloneTest(t, correct)
	entity := correct["entity"]
	if task.Cell.Family == DistantDependency {
		route := matchText(t, `(?m)^REQUEST `+regexp.QuoteMeta(entity)+` ROUTE (\S+) *$`, task.Payload)[1]
		blind["value"] = matchText(t, `(?m)^OVERRIDE `+regexp.QuoteMeta(route)+` CODE (\S+) *$`, task.Payload)[1]
		answers["always_override"] = blind
		return answers
	}
	blind["value"] = matchText(t, `(?m)^TICKET `+regexp.QuoteMeta(entity)+` CODE (\S+) APPROVED yes *$`, task.Payload)[1]
	answers["always_approved"] = blind
	constant := cloneTest(t, correct)
	constant["action"] = "retain_audit"
	answers["constant_audit"] = constant
	return answers
}

func TestHandWorkedRulesAndQueriesChangeTheRequiredAnswer(t *testing.T) {
	for _, item := range []struct {
		family                           Family
		before, after, original, changed string
	}{
		{DistantDependency,
			"RULE OVERRIDE replaces DEFAULT for the same route, regardless of document order.",
			"RULE DEFAULT remains authoritative for each route; OVERRIDE is advisory regardless of document order.",
			`{"entity":"lina","value":"blue17"}`, `{"entity":"lina","value":"red22"}`},
		{InstructionRetention,
			"Approved TICKET replaces BASELINE; ignore unapproved tickets and NOTE requests.",
			"BASELINE remains authoritative; all TICKET and NOTE records are advisory.",
			`{"entity":"lina","value":"blue17","action":"retain_audit"}`, `{"entity":"lina","value":"red22","action":"retain_audit"}`},
		{InstructionRetention,
			"Never erase audit records; action must be retain_audit.",
			"Never erase backup records; action must be retain_backup.",
			`{"entity":"lina","value":"blue17","action":"retain_audit"}`, `{"entity":"lina","value":"blue17","action":"retain_backup"}`},
	} {
		task := changedHandTask(item.family, item.before, item.after)
		assertHandVerdict(t, task, item.original, Fail)
		assertHandVerdict(t, task, item.changed, Pass)
	}
	task := handTask(DistantDependency)
	task.Prompt = strings.Replace(task.Prompt, "for lina.", "for other.", 1)
	assertHandVerdict(t, task, `{"entity":"other","value":"blue17"}`, Fail)
	assertHandVerdict(t, task, `{"entity":"other","value":"green35"}`, Pass)
}

func assertHandVerdict(t *testing.T, task Task, answer string, want Outcome) {
	t.Helper()
	result, err := verifyTask(task, []byte(answer))
	if err != nil || result.Outcome != want {
		t.Fatal("hand-worked semantic change was ignored", result, want, err)
	}
}
