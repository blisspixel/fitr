package score

import "testing"

func TestNeedOrderLeadsWithBehaviorNotSpeed(t *testing.T) {
	order := SortedNeeds(map[string]Verdict{
		"fast_and_decent": {State: Pass},
		"coding":          {State: Skip},
		"output_health":   {State: Pass},
		"low_footprint":   {State: Pass},
	})
	index := map[string]int{}
	for i, need := range order {
		index[need] = i
	}
	if index["coding"] > index["output_health"] || index["output_health"] > index["low_footprint"] || index["low_footprint"] > index["fast_and_decent"] {
		t.Fatalf("display order = %v", order)
	}
}

func TestQualityMeasuredIgnoresASpeedPass(t *testing.T) {
	if QualityMeasured(map[string]Verdict{
		"fast_and_decent": {State: Pass},
		"coding":          {State: Skip},
		"low_footprint":   {State: Pass},
	}) {
		t.Fatal("a speed pass became a behavioral measurement")
	}
	if !QualityMeasured(map[string]Verdict{
		"coding":          {State: Fail},
		"fast_and_decent": {State: Pass},
	}) {
		t.Fatal("a failed coding gate was not a measurement")
	}
	if QualityMeasured(map[string]Verdict{
		"coding": {State: Inconclusive},
		"vision": {State: NA},
	}) {
		t.Fatal("inconclusive and n/a became a behavioral measurement")
	}
}
