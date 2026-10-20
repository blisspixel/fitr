package record

import (
	"fmt"
	"reflect"
)

// A planned receipt is sealed before the manifest and derived again on load.
// One binder serves every schema. The caller supplies the field pointers and
// the qualify function, so a token account cannot be stored as a condition
// receipt and the two seal paths cannot drift apart.

type cloned[T any] interface {
	Clone() T
}

type sealedPlan[T any] interface {
	Clone() T
	Digest() (string, error)
}

type receiptBinder[P sealedPlan[P], O cloned[O], R cloned[R]] struct {
	r        *Record
	noun     string
	claim    string
	cases    *int
	digest   *string
	present  func() bool
	qualify  func(P, O) (R, error)
	put      func(P, O, R)
	clear    func()
	snapshot func() (P, O, R, int)
}

func (b receiptBinder[P, O, R]) plan(cases int, sum func() (string, error)) error {
	if b.r == nil {
		return fmt.Errorf("cannot plan %s on a nil record", b.noun)
	}
	return planReceipt(b.r, b.noun, cases, func() (int, string) {
		return *b.cases, *b.digest
	}, func(count int, digest string) {
		*b.cases = count
		*b.digest = digest
	}, sum)
}

func (b receiptBinder[P, O, R]) attach(plan P, observed O) error {
	storedPlan, storedObserved, report, err := attachReceipt(b.r, b.noun, b.r != nil && b.present(), plan, observed, b.qualify)
	if err != nil {
		return err
	}
	b.put(storedPlan, storedObserved, report)
	if err := b.validate(); err != nil {
		b.clear()
		return err
	}
	return nil
}

func (b receiptBinder[P, O, R]) validate() error {
	var plan P
	var observed O
	var report R
	caseCount := 0
	cases, digest := 0, ""
	attached := b.r != nil && b.present()
	if attached {
		plan, observed, report, caseCount = b.snapshot()
	}
	if b.r != nil {
		cases, digest = *b.cases, *b.digest
	}
	return validateReceipt(attached, plan, observed, report, cases, digest, caseCount, b.qualify, b.claim)
}

func planReceipt(r *Record, noun string, cases int, current func() (int, string), store func(int, string), sum func() (string, error)) error {
	if r.Manifest != nil {
		return fmt.Errorf("%s plan must be sealed before the run manifest", noun)
	}
	have, digest := current()
	if have != 0 || digest != "" {
		return fmt.Errorf("%s plan is already sealed", noun)
	}
	value, err := sum()
	if err != nil {
		return err
	}
	store(cases, value)
	return nil
}

func attachReceipt[P cloned[P], O cloned[O], R cloned[R]](
	r *Record, noun string, already bool, plan P, observed O, qualify func(P, O) (R, error),
) (P, O, R, error) {
	var zeroP P
	var zeroO O
	var zeroR R
	if r == nil {
		return zeroP, zeroO, zeroR, fmt.Errorf("cannot attach %s to a nil record", noun)
	}
	if already {
		return zeroP, zeroO, zeroR, fmt.Errorf("%s evidence is already attached", noun)
	}
	report, err := qualify(plan, observed)
	if err != nil {
		return zeroP, zeroO, zeroR, err
	}
	return plan.Clone(), observed.Clone(), report.Clone(), nil
}

func validateReceipt[P interface {
	Digest() (string, error)
}, O any, R any](
	attached bool, plan P, observed O, report R, cases int, digest string, caseCount int,
	qualify func(P, O) (R, error), noun string,
) error {
	planned := cases != 0 || digest != ""
	if !attached {
		if planned {
			return fmt.Errorf("sealed %s has no attached observations", noun)
		}
		return nil
	}
	if !planned {
		return fmt.Errorf("%s observations have no sealed plan", noun)
	}
	sum, err := plan.Digest()
	if err != nil {
		return err
	}
	if !sha256Digest.MatchString(digest) || sum != digest {
		return fmt.Errorf("%s observations do not match the sealed plan", noun)
	}
	if caseCount != cases {
		return fmt.Errorf("%s cases do not match the sealed plan", noun)
	}
	again, err := qualify(plan, observed)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(again, report) {
		return fmt.Errorf("persisted %s report does not match its observations", noun)
	}
	return nil
}
