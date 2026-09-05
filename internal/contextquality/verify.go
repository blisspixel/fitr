package contextquality

import (
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/blisspixel/fitr/internal/strictjson"
)

// Verify independently parses the generated document's semantic records and
// compares the answer's exact fields. Invalid model JSON is a failed task;
// an invalid plan or malformed generated task is an API/integrity error.
func Verify(plan Plan, sequence int, answer []byte) (Verification, error) {
	if err := plan.Validate(); err != nil {
		return Verification{}, err
	}
	task, err := plannedTask(plan, sequence)
	if err != nil {
		return Verification{}, err
	}
	return verifyTask(task, answer)
}

func verifyTask(task Task, answer []byte) (Verification, error) {
	result := Verification{Schema: VerificationSchema, CellID: task.Cell.ID, Outcome: Fail}
	expected, err := expectedAnswer(task)
	if err != nil {
		return Verification{}, err
	}
	result.Reason = answerReason(answer, expected)
	if result.Reason == "matched" {
		result.Outcome = Pass
	}
	return result, nil
}

// Malformed model output is a task result, not an execution error.
func answerReason(answer []byte, expected map[string]string) string {
	if len(answer) > MaxAnswerBytes {
		return "answer_too_large"
	}
	if !utf8.Valid(answer) || strictjson.Validate(answer) != nil {
		return "invalid_json"
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(answer, &fields); err != nil || len(fields) != len(expected) {
		return "answer_fields"
	}
	for _, key := range []string{"entity", "value", "action"} {
		want, required := expected[key]
		if !required {
			continue
		}
		raw, exists := fields[key]
		if !exists {
			return "answer_fields"
		}
		var actual string
		if string(raw) == "null" || json.Unmarshal(raw, &actual) != nil {
			return "answer_types"
		}
		if actual != want {
			return "answer_mismatch"
		}
	}
	return "matched"
}

func expectedAnswer(task Task) (map[string]string, error) {
	var rows [][]string
	for _, line := range strings.Split(task.Payload, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		rows = append(rows, strings.Fields(line))
	}
	switch task.Cell.Family {
	case IndirectRetrieval:
		query, err := questionEntity(task, retrievalQuestion)
		if err != nil {
			return nil, err
		}
		return retrievalAnswer(rows, query)
	case DistantDependency:
		query, err := questionEntity(task, dependencyQuestion)
		if err != nil {
			return nil, err
		}
		return dependencyAnswer(rows, query)
	case InstructionRetention:
		query, err := questionEntity(task, retentionQuestion)
		if err != nil {
			return nil, err
		}
		return retentionAnswer(rows, query)
	default:
		return nil, errors.New("unsupported generated task family")
	}
}

func questionEntity(task Task, format string) (string, error) {
	_, question, ok := strings.Cut(task.Prompt, promptSuffix)
	prefix, suffix, placeholder := strings.Cut(format, "%s")
	entity, prefixOK := strings.CutPrefix(question, prefix)
	entity, suffixOK := strings.CutSuffix(entity, suffix)
	if !ok || !placeholder || !prefixOK || !suffixOK || len(entity) == 0 || len(entity) > 64 || strings.ContainsAny(entity, " \t\r\n") {
		return "", errors.New("generated task question has an invalid reference")
	}
	return entity, nil
}
