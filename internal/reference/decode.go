package reference

import (
	"bytes"
	"encoding/json"
	"errors"

	"github.com/blisspixel/fitr/internal/strictjson"
)

type verifierDocument struct {
	Schema string        `json:"schema"`
	Tasks  []Observation `json:"tasks"`
}

// DeclaredConditions is the operator's statement about the machine.
// The worker probe is not in this file; the command records that itself.
type DeclaredConditions struct {
	Schema        string `json:"schema"`
	GPUBusy       bool   `json:"gpu_busy"`
	OtherModel    bool   `json:"other_model"`
	ServedContext int    `json:"served_context"`
}

type imageDocument struct {
	Schema string            `json:"schema"`
	Images map[string]string `json:"images"`
}

// DecodePlan accepts one sealed plan and rejects a digest that does not match.
func DecodePlan(data []byte) (Plan, error) {
	var plan Plan
	if err := decodeBounded(data, MaxPlanBytes, &plan); err != nil {
		return Plan{}, err
	}
	return plan, plan.Validate()
}

// DecodeVerifier accepts recorded upstream rows. A problem statement, a
// patch, or any other unknown field is rejected rather than stored.
func DecodeVerifier(data []byte) ([]Observation, error) {
	var document verifierDocument
	if err := decodeBounded(data, MaxVerifierBytes, &document); err != nil {
		return nil, err
	}
	if document.Schema != VerifierSchema {
		return nil, errors.New("unsupported external reference verifier schema")
	}
	for _, row := range document.Tasks {
		if !knownVerdict(row.Verdict) {
			return nil, errors.New("verifier observation has an unknown verdict")
		}
	}
	return document.Tasks, nil
}

// DecodeConditions reads operator declarations. Absent fields stay absent
// and are not filled with false, which would look like an idle GPU.
func DecodeConditions(data []byte) (DeclaredConditions, error) {
	if len(data) == 0 || len(data) > MaxConditionsBytes {
		return DeclaredConditions{}, errors.New("conditions JSON exceeds its byte bound")
	}
	if err := strictjson.Validate(data); err != nil {
		return DeclaredConditions{}, err
	}
	raw, err := objectKeys(data)
	if err != nil {
		return DeclaredConditions{}, err
	}
	if !exactKeys(raw, "schema", "gpu_busy", "other_model", "served_context") {
		return DeclaredConditions{}, errors.New("conditions omit a required field or carry an unknown one")
	}
	var declared DeclaredConditions
	if err := json.Unmarshal(data, &declared); err != nil {
		return DeclaredConditions{}, err
	}
	if declared.Schema != ConditionsSchema {
		return DeclaredConditions{}, errors.New("unsupported external reference conditions schema")
	}
	return declared, nil
}

// AsRun copies the declarations. The probe fields stay at their zero
// values until the command records a live probe.
func (declared DeclaredConditions) AsRun() RunConditions {
	return RunConditions{
		GPUBusyDeclared: declared.GPUBusy, OtherModelDeclared: declared.OtherModel,
		ServedContext: declared.ServedContext,
	}
}

// ApplyImages copies operator digests onto the frozen subset. A missing
// or extra instance is refused. An image name is not accepted as a digest.
func ApplyImages(data []byte) ([]Instance, error) {
	if len(data) == 0 || len(data) > MaxImagesBytes {
		return nil, errors.New("image digest JSON exceeds its byte bound")
	}
	if err := strictjson.Validate(data); err != nil {
		return nil, err
	}
	var document imageDocument
	if err := decodeObject(data, &document); err != nil {
		return nil, err
	}
	if document.Schema != ImagesSchema {
		return nil, errors.New("unsupported external reference image schema")
	}
	return instancesFromImages(document.Images)
}

func instancesFromImages(images map[string]string) ([]Instance, error) {
	subset := DefaultSubset()
	if len(images) != len(subset) {
		return nil, errors.New("image digests do not match the frozen subset")
	}
	instances := make([]Instance, len(subset))
	for i, item := range subset {
		digest, ok := images[item.ID]
		if !ok || !validSHA256(digest) {
			return nil, errors.New("image digests do not match the frozen subset")
		}
		instances[i] = Instance{ID: item.ID, Created: item.Created, ImageDigest: digest}
	}
	return instances, nil
}

// DecodeResult loads a result and re-derives it. A stored rate that the
// observations do not support is rejected.
func DecodeResult(data []byte) (Result, error) {
	var result Result
	if err := decodeBounded(data, MaxResultBytes, &result); err != nil {
		return Result{}, err
	}
	return result, result.Validate()
}

// Validate re-derives the result. Fresh evidence cannot be claimed by a file.
func (result Result) Validate() error {
	if result.Schema != ResultSchema || result.EvidenceClass != EvidenceClass {
		return errors.New("unsupported external reference result")
	}
	derived, err := Analyze(result.Plan, result.Conditions, result.Observations)
	if err != nil {
		return err
	}
	return sameJSON(result, derived)
}

func sameJSON(left, right any) error {
	a, err := json.Marshal(left)
	if err != nil {
		return err
	}
	b, err := json.Marshal(right)
	if err != nil {
		return err
	}
	if !bytes.Equal(a, b) {
		return errors.New("stored external reference conclusion does not match its observations")
	}
	return nil
}

func decodeBounded(data []byte, limit int, into any) error {
	if len(data) == 0 || len(data) > limit {
		return errors.New("external reference JSON exceeds its byte bound")
	}
	if err := strictjson.Validate(data); err != nil {
		return err
	}
	return decodeObject(data, into)
}

func decodeObject(data []byte, into any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(into); err != nil {
		return err
	}
	if decoder.More() {
		return errors.New("content after the JSON value")
	}
	return nil
}

func objectKeys(data []byte) (map[string]json.RawMessage, error) {
	var raw map[string]json.RawMessage
	if err := decodeObject(data, &raw); err != nil {
		return nil, err
	}
	return raw, nil
}

func exactKeys(raw map[string]json.RawMessage, keys ...string) bool {
	if len(raw) != len(keys) {
		return false
	}
	for _, key := range keys {
		if _, ok := raw[key]; !ok {
			return false
		}
	}
	return true
}

// ObservationDigest names a result file without putting a path or a
// hostname into the name. Equal observations keep the same digest.
func ObservationDigest(rows []Observation) (string, error) {
	return hashValue(observationDomain, rows)
}
