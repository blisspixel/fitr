package role

import (
	"testing"
	"time"
)

func TestLifecycleAlternatingRollbacksRetainOriginalAdoptionTargets(t *testing.T) {
	first := newLifecycleFixture(t)
	first.issue(t)
	first.start(t)
	first.finish(t)
	first.adopt(t)
	firstAdoption := first.life.IncumbentSHA256
	second := nextLifecycleRound(t, first, first.timeNow.Add(time.Minute), true)
	second.issue(t)
	second.start(t)
	second.finish(t)
	second.adopt(t)
	secondAdoption := second.life.IncumbentSHA256
	life := second.life
	for index, target := range []lifecycleFixture{first, second, first} {
		for _, point := range target.points {
			roleReviewSave(t, target.records, point)
		}
		at := second.timeNow.Add(time.Duration(index+2) * time.Second)
		var err error
		life, err = target.roles.RollbackSelection(target.plan.Spec.Name, life.PreviousSHA256, target.records, life.Digest, at)
		if err != nil {
			t.Fatalf("rollback %d could not select the prior adoption: %v", index+1, err)
		}
		wantPrevious := secondAdoption
		if index == 1 {
			wantPrevious = firstAdoption
		}
		if life.PreviousSHA256 != wantPrevious {
			t.Fatalf("rollback %d points at a derived event rather than adoption: %s", index+1, life.PreviousSHA256)
		}
		if err := life.Validate(); err != nil {
			t.Fatal(err)
		}
		selected := life.incumbentAdoptionSHA256()
		if _, err := target.roles.RollbackSelection(target.plan.Spec.Name, selected, target.records, life.Digest, at); err == nil {
			t.Fatal("rollback to the already selected adoption appended another receipt")
		}
	}
}
