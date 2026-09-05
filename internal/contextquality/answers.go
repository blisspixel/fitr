package contextquality

import (
	"errors"
	"strings"
)

func retrievalAnswer(rows [][]string, station string) (map[string]string, error) {
	if len(rows) < 4 {
		return nil, errors.New("retrieval document lacks competing people")
	}
	var answer map[string]string
	people, assignments, codes := map[string]bool{}, map[string]bool{}, map[string]bool{}
	stationJobs := map[string]bool{}
	for _, row := range rows {
		if len(row) != 8 || row[0] != "PERSON" || row[2] != "AT" || row[4] != "JOB" || row[6] != "CODE" {
			return nil, errors.New("retrieval record is malformed")
		}
		assignment := row[3] + "/" + row[5]
		if people[row[1]] || assignments[assignment] || codes[row[7]] {
			return nil, errors.New("retrieval records are ambiguous")
		}
		people[row[1]], assignments[assignment], codes[row[7]] = true, true, true
		if row[5] != "clock_sync" && row[5] != "star_index" && row[5] != "dome_care" {
			return nil, errors.New("unknown retrieval job")
		}
		if row[3] == station {
			stationJobs[row[5]] = true
			if row[5] == "clock_sync" {
				answer = map[string]string{"entity": row[1], "value": row[7]}
			}
		}
	}
	if answer == nil || len(stationJobs) != 3 {
		return nil, errors.New("question station has an incomplete duty relationship")
	}
	return answer, nil
}

func documentRule(rows [][]string) (string, [][]string, error) {
	rule := ""
	var records [][]string
	for _, row := range rows {
		if row[0] == "RULE" {
			if rule != "" {
				return "", nil, errors.New("document repeats its priority rule")
			}
			rule = strings.Join(row, " ") + "\n"
		} else {
			records = append(records, row)
		}
	}
	if rule == "" {
		return "", nil, errors.New("document has no priority rule")
	}
	return rule, records, nil
}

func putRecord(records map[string]string, key, value string) error {
	if _, exists := records[key]; exists {
		return errors.New("document repeats a record identity")
	}
	records[key] = value
	return nil
}

func dependencyAnswer(rows [][]string, entity string) (map[string]string, error) {
	rule, rows, err := documentRule(rows)
	if err != nil {
		return nil, err
	}
	if rule != dependencyOverrideRule && rule != dependencyDefaultRule {
		return nil, errors.New("unsupported dependency rule")
	}
	requests, defaults, overrides := map[string]string{}, map[string]string{}, map[string]string{}
	for _, row := range rows {
		if len(row) != 4 {
			return nil, errors.New("dependency record is malformed")
		}
		var records map[string]string
		switch {
		case row[0] == "REQUEST" && row[2] == "ROUTE":
			records = requests
		case row[0] == "DEFAULT" && row[2] == "CODE":
			records = defaults
		case row[0] == "OVERRIDE" && row[2] == "CODE":
			records = overrides
		default:
			return nil, errors.New("unknown dependency record")
		}
		if err := putRecord(records, row[1], row[3]); err != nil {
			return nil, err
		}
	}
	if len(requests) < 2 {
		return nil, errors.New("dependency document lacks competing requests")
	}
	for _, route := range requests {
		if defaults[route] == "" || overrides[route] == "" || defaults[route] == overrides[route] {
			return nil, errors.New("dependency chain is incomplete or its rule cannot affect the result")
		}
	}
	route, exists := requests[entity]
	if !exists {
		return nil, errors.New("question entity has no route")
	}
	value := overrides[route]
	if rule == dependencyDefaultRule {
		value = defaults[route]
	}
	return map[string]string{"entity": entity, "value": value}, nil
}

type retentionRecords struct{ baseline, approved, unapproved, notes map[string]string }

func retentionAnswer(rows [][]string, entity string) (map[string]string, error) {
	rule, rows, err := documentRule(rows)
	if err != nil {
		return nil, err
	}
	useTicket, action, err := retentionPolicy(rule)
	if err != nil {
		return nil, err
	}
	records := retentionRecords{map[string]string{}, map[string]string{}, map[string]string{}, map[string]string{}}
	for _, row := range rows {
		if err := records.add(row); err != nil {
			return nil, err
		}
	}
	if len(records.approved) < 2 || len(records.baseline) != len(records.approved) || len(records.unapproved) != len(records.approved) {
		return nil, errors.New("retention document lacks complete competing service records")
	}
	for service, approved := range records.approved {
		if records.baseline[service] == "" || records.unapproved[service] == "" || records.notes[service] == "" ||
			approved == records.baseline[service] || approved == records.unapproved[service] || approved == records.notes[service] ||
			records.baseline[service] == records.unapproved[service] || records.baseline[service] == records.notes[service] {
			return nil, errors.New("retention priority chain is missing or ambiguous")
		}
	}
	value, exists := records.baseline[entity]
	if !exists {
		return nil, errors.New("question service has no baseline")
	}
	if useTicket {
		value = records.approved[entity]
	}
	return map[string]string{"entity": entity, "value": value, "action": action}, nil
}

func retentionPolicy(rule string) (bool, string, error) {
	for _, priority := range []string{retentionTicketRule, retentionBaselineRule} {
		for _, action := range []string{"retain_audit", "retain_backup"} {
			constraint := retainAuditRule
			if action == "retain_backup" {
				constraint = retainBackupRule
			}
			if rule == "RULE "+priority+" "+constraint+"\n" {
				return priority == retentionTicketRule, action, nil
			}
		}
	}
	return false, "", errors.New("unsupported retention priority or action constraint")
}

func (records retentionRecords) add(row []string) error {
	if len(row) == 4 && row[0] == "BASELINE" && row[2] == "CODE" {
		return putRecord(records.baseline, row[1], row[3])
	}
	if len(row) != 6 || row[2] != "CODE" {
		return errors.New("retention record is malformed")
	}
	switch {
	case row[0] == "TICKET" && row[4] == "APPROVED" && row[5] == "yes":
		return putRecord(records.approved, row[1], row[3])
	case row[0] == "TICKET" && row[4] == "APPROVED" && row[5] == "no":
		return putRecord(records.unapproved, row[1], row[3])
	case row[0] == "NOTE" && row[4] == "ERASE" && row[5] == "all":
		return putRecord(records.notes, row[1], row[3])
	default:
		return errors.New("unknown retention record")
	}
}
