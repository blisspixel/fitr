package contextquality

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

func TestPlanStrictRoundTripAndBounds(t *testing.T) {
	plan := testPlan(t)
	data, err := plan.JSON()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodePlan(data)
	if err != nil || !reflect.DeepEqual(plan, decoded) {
		t.Fatal("plan round trip failed", err)
	}
	if digest, err := plan.Digest(); err != nil || digest != plan.PlanSHA256 {
		t.Fatal("valid plan lost its seal", err)
	}
	for _, invalid := range [][]byte{
		append(append([]byte(nil), data...), []byte(` {}`)...),
		bytes.Replace(data, []byte(`"schema":`), []byte(`"Schema":`), 1),
		bytes.Replace(data, []byte(`"sequence":`), []byte(`"SEQUENCE":`), 1),
		bytes.Replace(data, []byte(`"sequence":`), []byte(`"extra":1,"sequence":`), 1),
		bytes.Replace(data, []byte(`"sequence":`), []byte(`"sequence":1,"sequence":`), 1),
		[]byte(`null`), []byte(`{}`), bytes.Repeat([]byte(" "), MaxPlanBytes+1),
	} {
		if _, err := DecodePlan(invalid); err == nil {
			t.Fatal("ambiguous or unbounded plan accepted")
		}
	}
	for _, seed := range []string{"", strings.Repeat("0", 31), strings.Repeat("0", 33), strings.ToUpper(testSeed), strings.Repeat("g", 32)} {
		if _, err := NewPlan(plan.Policy, seed); err == nil {
			t.Fatal("noncanonical seed identity accepted", seed)
		}
	}
	for _, index := range []int{-1, 0, len(plan.Cells) + 1} {
		if _, err := Generate(plan, index); err == nil {
			t.Fatal("out-of-range sequence accepted", index)
		}
		if _, err := Verify(plan, index, nil); err == nil {
			t.Fatal("out-of-range verification accepted", index)
		}
	}
}

func TestRehashingCannotAuthorizeChangedPlanCells(t *testing.T) {
	plan := testPlan(t)
	for _, mutate := range []func(*Plan){
		func(p *Plan) { p.Schema = "other" },
		func(p *Plan) { p.Policy.Qualification = "majority" },
		func(p *Plan) { p.SeedSet = "bad" },
		func(p *Plan) { p.Cells = p.Cells[:17] },
		func(p *Plan) { p.Cells[0].Seed++ },
		func(p *Plan) { p.Cells[0].Spans[0].Offset++ },
		func(p *Plan) { p.Cells[0].PayloadSHA256 = p.Cells[1].PayloadSHA256 },
		func(p *Plan) { p.Cells[0].PromptUTF8Bytes++ },
		func(p *Plan) { p.Cells[0], p.Cells[1] = p.Cells[1], p.Cells[0] },
		func(p *Plan) { p.Cells[0] = p.Cells[1] },
		func(p *Plan) { p.PolicySHA256 = p.PlanSHA256 },
	} {
		changed := cloneTest(t, plan)
		mutate(&changed)
		changed.PlanSHA256 = ""
		changed.PlanSHA256, _ = hashValue(PlanSchema, changed)
		if changed.Validate() == nil {
			t.Fatal("resealed impossible plan accepted")
		}
		if _, err := changed.Digest(); err == nil {
			t.Fatal("invalid plan got an authoritative digest")
		}
		if _, err := changed.JSON(); err == nil {
			t.Fatal("invalid plan serialized")
		}
		if _, err := Generate(changed, 1); err == nil {
			t.Fatal("invalid plan generated a task")
		}
		if _, err := Verify(changed, 1, nil); err == nil {
			t.Fatal("invalid plan verified a task")
		}
	}
}
