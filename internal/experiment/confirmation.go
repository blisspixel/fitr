package experiment

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/blisspixel/fitr/internal/boundedio"
	"github.com/blisspixel/fitr/internal/decision"
	"github.com/blisspixel/fitr/internal/record"
	"github.com/blisspixel/fitr/internal/strictjson"
)

const (
	ConfirmationPlanSchema   = "fitr.experiment.confirmation.plan.v1"
	ConfirmationBundleSchema = "fitr.experiment.confirmation.bundle.v1"
	confirmationKind         = "configuration"
	maximumConfirmationCtx   = 16 * 1024 * 1024
)

type ConfirmationPlan struct {
	Schema             string                 `json:"schema"`
	PlanSHA256         string                 `json:"plan_sha256"`
	Candidates         []record.ModelIdentity `json:"candidates"`
	DeviceKey          string                 `json:"device_key"`
	DecisionSpecSHA256 string                 `json:"decision_spec_sha256"`
	SeedSet            string                 `json:"seedset"`
	RequestedContext   int                    `json:"requested_context"`
	Repeats            int                    `json:"repeats"`
	Level              string                 `json:"level"`
}

type ConfirmationBundle struct {
	Schema       string                `json:"schema"`
	Plan         ConfirmationPlan      `json:"plan"`
	Spec         decision.DecisionSpec `json:"decision_spec"`
	Report       QuantReport           `json:"report"`
	PointRecords []*record.Record      `json:"point_records"`
}

func NewConfirmationPlan(candidates []record.ModelIdentity, deviceKey string,
	spec decision.DecisionSpec, requestedContext, repeats int) (ConfirmationPlan, error) {
	seedBytes := make([]byte, 16)
	if _, err := rand.Read(seedBytes); err != nil {
		return ConfirmationPlan{}, fmt.Errorf("create confirmation seed set: %w", err)
	}
	plan := ConfirmationPlan{
		Schema: ConfirmationPlanSchema, Candidates: append([]record.ModelIdentity(nil), candidates...),
		DeviceKey: strings.TrimSpace(deviceKey), SeedSet: "confirm-" + hex.EncodeToString(seedBytes),
		RequestedContext: requestedContext, Repeats: repeats, Level: "full",
	}
	specDigest, err := factorDigest("confirmation_decision_spec", spec)
	if err != nil {
		return ConfirmationPlan{}, err
	}
	plan.DecisionSpecSHA256 = specDigest
	if err := plan.validateWithoutDigest(spec); err != nil {
		return ConfirmationPlan{}, err
	}
	plan.PlanSHA256, err = confirmationPlanDigest(plan)
	if err != nil {
		return ConfirmationPlan{}, err
	}
	return plan, nil
}

func (plan ConfirmationPlan) Validate(spec decision.DecisionSpec) error {
	if err := plan.validateWithoutDigest(spec); err != nil {
		return err
	}
	digest, err := confirmationPlanDigest(plan)
	if err != nil {
		return err
	}
	if digest != plan.PlanSHA256 {
		return errors.New("confirmation plan digest does not match")
	}
	return nil
}

func (plan ConfirmationPlan) validateWithoutDigest(spec decision.DecisionSpec) error {
	if plan.Schema != ConfirmationPlanSchema || plan.Level != "full" {
		return errors.New("unsupported confirmation plan schema or level")
	}
	if err := spec.Validate(); err != nil {
		return fmt.Errorf("confirmation decision spec: %w", err)
	}
	if spec.Evidence != decision.EvidenceConfirm || spec.Objective == nil {
		return errors.New("confirmation requires evidence_level confirm and one narrow objective")
	}
	specDigest, err := factorDigest("confirmation_decision_spec", spec)
	if err != nil || specDigest != plan.DecisionSpecSHA256 {
		return errors.New("confirmation decision spec digest does not match")
	}
	if len(plan.Candidates) < 2 || len(plan.Candidates) > 4 {
		return errors.New("confirmation requires between two and four candidates")
	}
	if strings.TrimSpace(plan.DeviceKey) == "" || len(plan.SeedSet) < 16 || len(plan.SeedSet) > 128 {
		return errors.New("confirmation device identity or seed set is invalid")
	}
	if plan.RequestedContext < 1 || plan.RequestedContext > maximumConfirmationCtx ||
		plan.Repeats < 3 || plan.Repeats > 20 {
		return errors.New("confirmation context or repeat count is outside the supported bounds")
	}
	return validateConfirmationCandidates(plan.Candidates)
}

func validateConfirmationCandidates(candidates []record.ModelIdentity) error {
	seen := make(map[string]bool, len(candidates))
	backend, runtime := candidates[0].Backend, candidates[0].Runtime
	for _, candidate := range candidates {
		digest := candidate.RuntimeBoundDigest()
		if digest == "" {
			return errors.New("confirmation candidates require runtime-bound artifacts")
		}
		if candidate.Backend != backend || candidate.Runtime != runtime {
			return errors.New("confirmation candidates must use one backend and runtime")
		}
		if seen[digest] {
			return errors.New("confirmation candidate artifacts must be distinct")
		}
		seen[digest] = true
	}
	return nil
}

func ConfirmationPlanBinding(plan ConfirmationPlan, pointIndex int) record.ExperimentBinding {
	return record.ExperimentBinding{
		Schema: record.ExperimentBindingSchema, Kind: confirmationKind, Stage: string(StageConfirm),
		PlanSHA256: plan.PlanSHA256, PointIndex: pointIndex, PointCount: len(plan.Candidates),
	}
}

func AnalyzeConfirmation(results []*record.Record, plan ConfirmationPlan,
	spec decision.DecisionSpec) (QuantReport, error) {
	if err := plan.Validate(spec); err != nil {
		return QuantReport{}, err
	}
	if len(results) != len(plan.Candidates) {
		return QuantReport{}, errors.New("confirmation result count does not match the sealed candidate set")
	}
	for index, result := range results {
		if err := validateConfirmationPoint(result, plan, index); err != nil {
			return QuantReport{}, fmt.Errorf("confirmation point %d: %w", index+1, err)
		}
	}
	return analyzeConfirmedQuant(results, spec, nil)
}

func validateConfirmationPoint(result *record.Record, plan ConfirmationPlan, index int) error {
	if result == nil || result.Manifest == nil || result.EvidenceIntegrityIssue() != "" {
		return errors.New("result is not valid sealed evidence")
	}
	wantBinding := ConfirmationPlanBinding(plan, index+1)
	if result.Experiment == nil || *result.Experiment != wantBinding {
		return errors.New("result is not bound to its confirmation plan position")
	}
	if result.Manifest.Model != plan.Candidates[index] {
		return errors.New("runtime-bound artifact differs from the sealed candidate")
	}
	if result.Device.Key() != plan.DeviceKey || result.ContextSize() != plan.RequestedContext {
		return errors.New("device or requested context differs from the confirmation plan")
	}
	if result.Level != plan.Level || result.Repeats != plan.Repeats || result.SeedSet != plan.SeedSet {
		return errors.New("measurement protocol differs from the confirmation plan")
	}
	return nil
}

func NewConfirmationBundle(plan ConfirmationPlan, spec decision.DecisionSpec,
	results []*record.Record) (ConfirmationBundle, error) {
	report, err := AnalyzeConfirmation(results, plan, spec)
	if err != nil {
		return ConfirmationBundle{}, err
	}
	return ConfirmationBundle{
		Schema: ConfirmationBundleSchema, Plan: plan, Spec: spec, Report: report,
		PointRecords: append([]*record.Record(nil), results...),
	}, nil
}

func (bundle ConfirmationBundle) Validate() (QuantReport, error) {
	if bundle.Schema != ConfirmationBundleSchema {
		return QuantReport{}, fmt.Errorf("unsupported confirmation bundle schema %q", bundle.Schema)
	}
	rebuilt, err := AnalyzeConfirmation(bundle.PointRecords, bundle.Plan, bundle.Spec)
	if err != nil {
		return QuantReport{}, err
	}
	rebuiltJSON, rebuiltErr := json.Marshal(rebuilt)
	storedJSON, storedErr := json.Marshal(bundle.Report)
	if rebuiltErr != nil || storedErr != nil || !bytes.Equal(rebuiltJSON, storedJSON) {
		return QuantReport{}, errors.New("stored confirmation report does not match its sealed point records")
	}
	return rebuilt, nil
}

func (bundle ConfirmationBundle) JSON() ([]byte, error) {
	if _, err := bundle.Validate(); err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		return nil, err
	}
	if len(data) > maximumBundleBytes {
		return nil, fmt.Errorf("confirmation bundle exceeds %d bytes", maximumBundleBytes)
	}
	return append(data, '\n'), nil
}

func LoadConfirmationBundle(path string) (ConfirmationBundle, error) {
	data, err := boundedio.ReadFile(path, maximumBundleBytes)
	if err != nil {
		return ConfirmationBundle{}, err
	}
	return decodeConfirmationBundle(data)
}

func decodeConfirmationBundle(data []byte) (ConfirmationBundle, error) {
	if len(data) > maximumBundleBytes {
		return ConfirmationBundle{}, fmt.Errorf("confirmation bundle exceeds %d bytes", maximumBundleBytes)
	}
	if err := strictjson.Validate(data); err != nil {
		return ConfirmationBundle{}, fmt.Errorf("invalid confirmation bundle JSON: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var bundle ConfirmationBundle
	if err := decoder.Decode(&bundle); err != nil {
		return ConfirmationBundle{}, fmt.Errorf("invalid confirmation bundle JSON: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return ConfirmationBundle{}, errors.New("invalid confirmation bundle JSON: trailing content")
		}
		return ConfirmationBundle{}, fmt.Errorf("invalid confirmation bundle JSON: %w", err)
	}
	if _, err := bundle.Validate(); err != nil {
		return ConfirmationBundle{}, fmt.Errorf("invalid confirmation bundle: %w", err)
	}
	return bundle, nil
}

func confirmationPlanDigest(plan ConfirmationPlan) (string, error) {
	plan.PlanSHA256 = ""
	return factorDigest("confirmation_plan", plan)
}
