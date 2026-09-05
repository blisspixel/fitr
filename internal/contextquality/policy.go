// Package contextquality constructs and verifies a finite synthetic document
// task set. It performs no inference and establishes no runtime, tokenizer,
// capacity, workflow or statistical generalization claim.
package contextquality

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"

	"github.com/blisspixel/fitr/internal/strictjson"
)

const (
	PolicySchema             = "fitr.context-quality.policy.v1"
	PlanSchema               = "fitr.context-quality.plan.v1"
	ReportSchema             = "fitr.context-quality.report.v1"
	VerificationSchema       = "fitr.context-quality.verification.v1"
	QualificationPolicy      = "required_cells_all_pass.v1"
	RequestPolicy            = "owned_ollama.generate.no_truncate_no_shift.v1"
	MinPayloadUTF8Bytes      = 2 << 10
	MaxPayloadUTF8Bytes      = 64 << 10
	OutputReserveTokens      = 128
	MaxOperatingWindowTokens = 16 << 20
	MaxAnswerBytes           = 4 << 10
	MaxPolicyBytes           = 4 << 10
	MaxPlanBytes             = 64 << 10
	CellsPerTier             = 9
)

type Policy struct {
	Schema                string `json:"schema"`
	OperatingWindowTokens int    `json:"operating_window_tokens"`
	PayloadUTF8Bytes      []int  `json:"payload_utf8_bytes"`
	OutputReserveTokens   int    `json:"output_reserve_tokens"`
	TaskPackSHA256        string `json:"task_pack_sha256"`
	Qualification         string `json:"qualification"`
	RequestPolicy         string `json:"request_policy"`
}

func NewPolicy(window int, tiers []int) (Policy, error) {
	policy := Policy{Schema: PolicySchema, OperatingWindowTokens: window, PayloadUTF8Bytes: append([]int(nil), tiers...),
		OutputReserveTokens: OutputReserveTokens, TaskPackSHA256: TaskPackSHA256(), Qualification: QualificationPolicy, RequestPolicy: RequestPolicy}
	return policy, policy.Validate()
}

func (policy Policy) Validate() error {
	if policy.Schema != PolicySchema || policy.TaskPackSHA256 != TaskPackSHA256() || policy.Qualification != QualificationPolicy || policy.RequestPolicy != RequestPolicy {
		return errors.New("unsupported context task policy")
	}
	// This is a declared operating window, not a token estimate for the bytes.
	if policy.OperatingWindowTokens <= OutputReserveTokens || policy.OperatingWindowTokens > MaxOperatingWindowTokens || policy.OutputReserveTokens != OutputReserveTokens {
		return errors.New("context task window or output reserve is invalid")
	}
	if len(policy.PayloadUTF8Bytes) < 2 || len(policy.PayloadUTF8Bytes) > 4 {
		return errors.New("context task policy requires two to four byte tiers")
	}
	previous := 0
	for _, tier := range policy.PayloadUTF8Bytes {
		if tier < MinPayloadUTF8Bytes || tier > MaxPayloadUTF8Bytes || tier <= previous {
			return errors.New("context task byte tiers must increase strictly within 2 to 64 KiB")
		}
		previous = tier
	}
	return nil
}

func (policy Policy) Digest() (string, error) {
	if err := policy.Validate(); err != nil {
		return "", err
	}
	return hashValue(PolicySchema, policy)
}

func (policy Policy) JSON() ([]byte, error) {
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(policy)
}

func DecodePolicy(data []byte) (Policy, error) {
	var policy Policy
	if err := decodeExact(data, MaxPolicyBytes, &policy); err != nil {
		return Policy{}, err
	}
	return policy, policy.Validate()
}

func hashBytes(domain string, data []byte) string {
	sum := sha256.Sum256(append([]byte(domain+"\x00"), data...))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func hashValue(domain string, value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return hashBytes(domain, data), nil
}

func decodeExact(data []byte, limit int, into any) error {
	if len(data) == 0 || len(data) > limit {
		return errors.New("context task JSON exceeds its byte bound")
	}
	if err := strictjson.Validate(data); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(into); err != nil {
		return err
	}
	canonical, err := json.Marshal(into)
	if err != nil {
		return err
	}
	var supplied, expected any
	for _, item := range []struct {
		data []byte
		into *any
	}{{data, &supplied}, {canonical, &expected}} {
		decoder := json.NewDecoder(bytes.NewReader(item.data))
		decoder.UseNumber()
		if err := decoder.Decode(item.into); err != nil {
			return err
		}
	}
	if !reflect.DeepEqual(supplied, expected) {
		return errors.New("context task JSON contains missing, noncanonical or case-aliased fields")
	}
	return nil
}
