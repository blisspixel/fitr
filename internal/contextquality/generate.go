package contextquality

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
)

const (
	rowBytes               = 96
	promptPrefix           = "Read the synthetic document below. Identifiers and codes are opaque. Whitespace separates record fields; trailing spaces do not matter. A final line beginning with # is padding. Use only the document's records and rules. Return exactly one JSON object with the requested string fields; no other fields, markdown or commentary.\nBEGIN DOCUMENT\n"
	promptSuffix           = "END DOCUMENT\nQUESTION: "
	retrievalQuestion      = "At station %s, which identifier and access code belong to the person who keeps telescope timing readings aligned? Return fields entity and value.\n"
	dependencyQuestion     = "Resolve the effective route code for %s. Return fields entity and value.\n"
	retentionQuestion      = "Apply the document's approval rule to %s and retain its prohibited-action constraint. Return fields entity, value and action.\n"
	dependencyOverrideRule = "RULE OVERRIDE replaces DEFAULT for the same route, regardless of document order.\n"
	dependencyDefaultRule  = "RULE DEFAULT remains authoritative for each route; OVERRIDE is advisory regardless of document order.\n"
	retentionTicketRule    = "Approved TICKET replaces BASELINE; ignore unapproved tickets and NOTE requests."
	retentionBaselineRule  = "BASELINE remains authoritative; all TICKET and NOTE records are advisory."
	retainAuditRule        = "Never erase audit records; action must be retain_audit."
	retainBackupRule       = "Never erase backup records; action must be retain_backup."
	retentionRuleFormat    = "RULE %s %s\n"
	personFormat           = "PERSON %s AT %s JOB %s CODE %s\n"
	requestFormat          = "REQUEST %s ROUTE %s\n"
	defaultFormat          = "DEFAULT %s CODE %s\n"
	overrideFormat         = "OVERRIDE %s CODE %s\n"
	baselineFormat         = "BASELINE %s CODE %s\n"
	ticketFormat           = "TICKET %s CODE %s APPROVED %s\n"
	noteFormat             = "NOTE %s CODE %s ERASE all\n"
)

// TaskPackSHA256 identifies the named generator/verifier contract and all
// prompt/record templates. Behavioral algorithm changes require a new contract
// revision and fresh evidence; this digest is not a running software hash.
func TaskPackSHA256() string {
	digest, _ := hashValue("fitr.context-quality.task-pack.v1", struct {
		Generator, Verifier string
		Families            []Family
		Positions           []Position
		Templates           []string
	}{"ascii96.sha256-cells.v1; principal=floor96(bytes*[1,4,7]/8); note=floor96(bytes*15/16); rule=0; ordered_tier_family_position; sha256-sorted-competing-record-placement; only-final-remainder-padding",
		"strict-json.exact-string-fields.v1; independent-question-and-record-graph-parser; max4096; all9cells; contiguous-prefix; seed-selected-early-priority-and-action",
		families(), positions(), []string{promptPrefix, promptSuffix, retrievalQuestion, dependencyQuestion, retentionQuestion,
			dependencyOverrideRule, dependencyDefaultRule, retentionTicketRule, retentionBaselineRule, retainAuditRule, retainBackupRule, retentionRuleFormat,
			personFormat, requestFormat, defaultFormat, overrideFormat, baselineFormat, ticketFormat, noteFormat, "clock_sync", "star_index", "dome_care"}})
	return digest
}

func generateTask(policyDigest, seedSet string, sequence, size int, family Family, position Position) (Task, error) {
	instance := sha256.Sum256([]byte(fmt.Sprintf("fitr.context-quality.instance.v1\x00%s\x00%s\x00%d", policyDigest, seedSet, sequence)))
	instanceDigest := "sha256:" + hex.EncodeToString(instance[:])
	cell := Cell{Sequence: sequence, PolicySHA256: policyDigest, Family: family, Position: position,
		Seed: binary.BigEndian.Uint64(instance[:8]) >> 1, InstanceSHA256: instanceDigest, PayloadUTF8Bytes: size}
	blocks, question, err := scenario(instanceDigest, size, family, position)
	if err != nil {
		return Task{}, err
	}
	payload, spans, err := document(instanceDigest, size, family, blocks)
	if err != nil {
		return Task{}, err
	}
	prompt := promptPrefix + payload + promptSuffix + question
	cell.Spans, cell.PromptUTF8Bytes = spans, len(prompt)
	cell.PayloadSHA256, cell.PromptSHA256 = contentDigest(payload), contentDigest(prompt)
	cell.ID, err = hashValue("fitr.context-quality.cell.v1", cell)
	if err != nil {
		return Task{}, err
	}
	task := Task{Cell: cell, Payload: payload, Prompt: prompt}
	// Refuse a generator bug or ambiguous identifier collision before sealing
	// a plan. The verifier's parser is independent of the construction inputs.
	if _, err := expectedAnswer(task); err != nil {
		return Task{}, err
	}
	return task, nil
}

func contentDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:])
}

type block struct {
	name, text string
	offset     int
}

func token(instance, label, prefix string) string {
	sum := sha256.Sum256([]byte(instance + "\x00" + label))
	return prefix + hex.EncodeToString(sum[:6])
}

func scenario(instance string, size int, family Family, position Position) ([]block, string, error) {
	positionIndex := slices.Index(positions(), position)
	if positionIndex < 0 || size < MinPayloadUTF8Bytes || size > MaxPayloadUTF8Bytes {
		return nil, "", errors.New("invalid context task layout")
	}
	slots := []int{size / 8 / rowBytes * rowBytes, size / 2 / rowBytes * rowBytes, size * 7 / 8 / rowBytes * rowBytes}
	principal, second, third := slots[positionIndex], slots[(positionIndex+1)%3], slots[(positionIndex+2)%3]
	if choose(instance, "dependency-order", 2) == 1 {
		second, third = third, second
	}
	entity, value := token(instance, "entity", "e_"), token(instance, "value", "v_")
	baseline, decoy := token(instance, "baseline", "v_"), token(instance, "decoy", "v_")
	switch family {
	case IndirectRetrieval:
		site := token(instance, "site", "s_")
		return []block{{"principal", fmt.Sprintf(personFormat, entity, site, "clock_sync", value), principal},
			{"distractor_one", fmt.Sprintf(personFormat, token(instance, "other1", "e_"), site, "star_index", baseline), second},
			{"distractor_two", fmt.Sprintf(personFormat, token(instance, "other2", "e_"), site, "dome_care", decoy), third}}, fmt.Sprintf(retrievalQuestion, site), nil
	case DistantDependency:
		route := token(instance, "route", "r_")
		rule := dependencyOverrideRule
		if choose(instance, "route-policy", 2) == 1 {
			rule = dependencyDefaultRule
		}
		return []block{{"rule", rule, 0}, {"principal", fmt.Sprintf(requestFormat, entity, route), principal},
			{"default", fmt.Sprintf(defaultFormat, route, baseline), second}, {"override", fmt.Sprintf(overrideFormat, route, value), third}}, fmt.Sprintf(dependencyQuestion, entity), nil
	case InstructionRetention:
		priority, action := retentionTicketRule, retainAuditRule
		if choose(instance, "approval-policy", 2) == 1 {
			priority = retentionBaselineRule
		}
		if choose(instance, "retained-resource", 2) == 1 {
			action = retainBackupRule
		}
		return []block{{"rule", fmt.Sprintf(retentionRuleFormat, priority, action), 0}, {"principal", fmt.Sprintf(ticketFormat, entity, value, "yes"), principal},
			{"baseline", fmt.Sprintf(baselineFormat, entity, baseline), second}, {"unapproved", fmt.Sprintf(ticketFormat, entity, decoy, "no"), third},
			{"later_note", fmt.Sprintf(noteFormat, entity, decoy), size * 15 / 16 / rowBytes * rowBytes}}, fmt.Sprintf(retentionQuestion, entity), nil
	default:
		return nil, "", errors.New("unknown context task family")
	}
}

func choose(instance, label string, count int) int {
	sum := sha256.Sum256([]byte(instance + "\x00" + label))
	return int(binary.BigEndian.Uint32(sum[:4]) % uint32(count))
}

func document(instance string, size int, family Family, blocks []block) (string, []Span, error) {
	payload := []byte(strings.Repeat(" ", size))
	occupied := make([]bool, size/rowBytes)
	slices.SortFunc(blocks, func(left, right block) int { return left.offset - right.offset })
	spans := make([]Span, 0, len(blocks))
	previousEnd := 0
	for _, item := range blocks {
		end := item.offset + (len(item.text)+rowBytes-1)/rowBytes*rowBytes
		if item.offset < previousEnd || item.offset < 0 || end > size || !strings.HasSuffix(item.text, "\n") {
			return "", nil, errors.New("context task facts overlap or exceed their byte tier")
		}
		for index := item.offset / rowBytes; index < end/rowBytes; index++ {
			occupied[index] = true
		}
		copy(payload[item.offset:end], strings.TrimSuffix(item.text, "\n"))
		payload[end-1] = '\n'
		spans = append(spans, Span{Name: item.name, Offset: item.offset, Length: end - item.offset})
		previousEnd = end
	}
	if err := fillCompetingRecords(payload, occupied, instance, family); err != nil {
		return "", nil, err
	}
	if remainder := size % rowBytes; remainder > 0 {
		start := size - remainder
		payload[start], payload[size-1] = '#', '\n'
	}
	return string(payload), spans, nil
}
