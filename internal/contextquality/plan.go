package contextquality

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
)

// NewPlan is deterministic and copies its policy. The caller supplies one
// freshly generated 128-bit lowercase hexadecimal seed set, then reuses this
// exact plan across compared candidates. This pure API cannot attest freshness.
func NewPlan(policy Policy, seedSet string) (Plan, error) {
	if err := policy.Validate(); err != nil {
		return Plan{}, err
	}
	if len(seedSet) != 32 {
		return Plan{}, errors.New("context task seed set must be 32 lowercase hexadecimal characters")
	}
	seed, err := hex.DecodeString(seedSet)
	if err != nil || len(seed) != 16 || hex.EncodeToString(seed) != seedSet {
		return Plan{}, errors.New("context task seed set must be 32 lowercase hexadecimal characters")
	}
	policy.PayloadUTF8Bytes = append([]int(nil), policy.PayloadUTF8Bytes...)
	policyDigest, err := policy.Digest()
	if err != nil {
		return Plan{}, err
	}
	plan := Plan{Schema: PlanSchema, Policy: policy, PolicySHA256: policyDigest, SeedSet: seedSet, Cells: []Cell{}}
	for _, tier := range policy.PayloadUTF8Bytes {
		for _, family := range families() {
			for _, position := range positions() {
				task, err := generateTask(policyDigest, seedSet, len(plan.Cells)+1, tier, family, position)
				if err != nil {
					return Plan{}, err
				}
				plan.Cells = append(plan.Cells, task.Cell)
			}
		}
	}
	plan.PlanSHA256, err = hashValue(PlanSchema, plan)
	return plan, err
}

func (plan Plan) Validate() error {
	if plan.Schema != PlanSchema || len(plan.Cells) < 2*CellsPerTier || len(plan.Cells) > 4*CellsPerTier {
		return errors.New("invalid context task plan schema or cell bounds")
	}
	expected, err := NewPlan(plan.Policy, plan.SeedSet)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(plan, expected) {
		return errors.New("context task plan differs from its deterministic policy and cell identities")
	}
	return nil
}

func (plan Plan) Digest() (string, error) {
	if err := plan.Validate(); err != nil {
		return "", err
	}
	return plan.PlanSHA256, nil
}

func (plan Plan) JSON() ([]byte, error) {
	if err := plan.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(plan)
}

func DecodePlan(data []byte) (Plan, error) {
	var plan Plan
	if err := decodeExact(data, MaxPlanBytes, &plan); err != nil {
		return Plan{}, err
	}
	return plan, plan.Validate()
}

func Generate(plan Plan, sequence int) (Task, error) {
	if err := plan.Validate(); err != nil {
		return Task{}, err
	}
	return plannedTask(plan, sequence)
}

func plannedTask(plan Plan, sequence int) (Task, error) {
	if sequence < 1 || sequence > len(plan.Cells) {
		return Task{}, errors.New("context task cell sequence is outside the plan")
	}
	cell := plan.Cells[sequence-1]
	return generateTask(plan.PolicySHA256, plan.SeedSet, sequence, cell.PayloadUTF8Bytes, cell.Family, cell.Position)
}

func families() []Family    { return []Family{IndirectRetrieval, DistantDependency, InstructionRetention} }
func positions() []Position { return []Position{Beginning, Middle, End} }
