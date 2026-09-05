package contextquality

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

func fillCompetingRecords(payload []byte, occupied []bool, instance string, family Family) error {
	var free []int
	for index, used := range occupied {
		if !used {
			free = append(free, index)
		}
	}
	// Independent placement breaks first/last and aligned-row shortcuts while
	// preserving exactly the same semantic document across paired candidates.
	slices.SortFunc(free, func(a, b int) int {
		left := token(instance, fmt.Sprintf("placement-%d", a), "")
		right := token(instance, fmt.Sprintf("placement-%d", b), "")
		if left == right {
			return a - b
		}
		return strings.Compare(left, right)
	})
	for index, slot := range free {
		line := competingRecord(instance, family, index, len(free))
		if line == "" || len(line) > rowBytes || !strings.HasSuffix(line, "\n") {
			return errors.New("competing context record exceeds its fixed row")
		}
		start, end := slot*rowBytes, (slot+1)*rowBytes
		copy(payload[start:end], strings.TrimSuffix(line, "\n"))
		payload[end-1] = '\n'
	}
	return nil
}

func competingRecord(instance string, family Family, index, total int) string {
	if family == IndirectRetrieval {
		label := fmt.Sprintf("person-%d", index)
		jobs := []string{"clock_sync", "star_index", "dome_care"}
		return fmt.Sprintf(personFormat, token(instance, label+"entity", "e_"), token(instance, label+"site", "s_"),
			jobs[choose(instance, label+"job", len(jobs))], token(instance, label+"value", "v_"))
	}
	groupSize := 3
	if family == InstructionRetention {
		groupSize = 4
	}
	group, kind := index/groupSize, index%groupSize
	label := fmt.Sprintf("group-%d", group)
	entity, route := token(instance, label+"entity", "e_"), token(instance, label+"route", "r_")
	base, changed := token(instance, label+"base", "v_"), token(instance, label+"changed", "v_")
	if family == DistantDependency {
		if index >= total-total%groupSize {
			if kind == 0 {
				return fmt.Sprintf(defaultFormat, route, base)
			}
			return fmt.Sprintf(overrideFormat, route, changed)
		}
		switch kind {
		case 0:
			return fmt.Sprintf(requestFormat, entity, route)
		case 1:
			return fmt.Sprintf(defaultFormat, route, base)
		default:
			return fmt.Sprintf(overrideFormat, route, changed)
		}
	}
	if family == InstructionRetention {
		if index >= total-total%groupSize {
			// Remaining complete rows are unrelated advisory notes, not padding.
			return fmt.Sprintf(noteFormat, token(instance, fmt.Sprintf("extra-%d", index), "e_"), base)
		}
		switch kind {
		case 0:
			return fmt.Sprintf(baselineFormat, entity, base)
		case 1:
			return fmt.Sprintf(ticketFormat, entity, changed, "yes")
		case 2:
			return fmt.Sprintf(ticketFormat, entity, token(instance, label+"rejected", "v_"), "no")
		default:
			return fmt.Sprintf(noteFormat, entity, token(instance, label+"note", "v_"))
		}
	}
	return ""
}
