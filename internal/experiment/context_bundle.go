package experiment

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/blisspixel/fitr/internal/boundedio"
	"github.com/blisspixel/fitr/internal/record"
	"github.com/blisspixel/fitr/internal/strictjson"
)

const (
	ContextBundleSchema = "fitr.experiment.context.bundle.v1"
	maximumBundleBytes  = 256 << 20
)

// ContextBundle is the local replay artifact for one completed context plan.
// Point records retain their own signatures. The stored report is checked
// against a fresh derivation whenever the bundle is loaded.
type ContextBundle struct {
	Schema       string           `json:"schema"`
	Plan         ContextPlan      `json:"plan"`
	PlanSHA256   string           `json:"plan_sha256"`
	Report       ContextReport    `json:"report"`
	PointRecords []*record.Record `json:"point_records"`
}

func NewContextBundle(plan ContextPlan, planSHA256 string,
	results []*record.Record) (ContextBundle, error) {
	report, err := AnalyzePlannedContext(results, plan, planSHA256)
	if err != nil {
		return ContextBundle{}, err
	}
	return ContextBundle{
		Schema: ContextBundleSchema, Plan: plan, PlanSHA256: planSHA256,
		Report: report, PointRecords: append([]*record.Record(nil), results...),
	}, nil
}

// Validate rebuilds the report from the individually signed point records and
// rejects any stored projection that no longer matches those sources.
func (bundle ContextBundle) Validate() (ContextReport, error) {
	if bundle.Schema != ContextBundleSchema {
		return ContextReport{}, fmt.Errorf("unsupported context bundle schema %q", bundle.Schema)
	}
	rebuilt, err := AnalyzePlannedContext(bundle.PointRecords, bundle.Plan, bundle.PlanSHA256)
	if err != nil {
		return ContextReport{}, err
	}
	rebuiltJSON, rebuiltErr := json.Marshal(rebuilt)
	storedJSON, storedErr := json.Marshal(bundle.Report)
	if rebuiltErr != nil || storedErr != nil {
		return ContextReport{}, errors.New("context report could not be normalized")
	}
	if !bytes.Equal(rebuiltJSON, storedJSON) {
		return ContextReport{}, errors.New("stored context report does not match its sealed point records")
	}
	return rebuilt, nil
}

func (bundle ContextBundle) JSON() ([]byte, error) {
	if _, err := bundle.Validate(); err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		return nil, err
	}
	if len(data) > maximumBundleBytes {
		return nil, fmt.Errorf("context bundle exceeds %d bytes", maximumBundleBytes)
	}
	return append(data, '\n'), nil
}

func LoadContextBundle(path string) (ContextBundle, error) {
	data, err := boundedio.ReadFile(path, maximumBundleBytes)
	if err != nil {
		return ContextBundle{}, err
	}
	return decodeContextBundle(data)
}

func decodeContextBundle(data []byte) (ContextBundle, error) {
	if len(data) > maximumBundleBytes {
		return ContextBundle{}, fmt.Errorf("context bundle exceeds %d bytes", maximumBundleBytes)
	}
	if err := strictjson.Validate(data); err != nil {
		return ContextBundle{}, fmt.Errorf("invalid context bundle JSON: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var bundle ContextBundle
	if err := decoder.Decode(&bundle); err != nil {
		return ContextBundle{}, fmt.Errorf("invalid context bundle JSON: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return ContextBundle{}, errors.New("invalid context bundle JSON: content after the bundle")
		}
		return ContextBundle{}, fmt.Errorf("invalid context bundle JSON: %w", err)
	}
	if _, err := bundle.Validate(); err != nil {
		return ContextBundle{}, fmt.Errorf("invalid context bundle: %w", err)
	}
	return bundle, nil
}
