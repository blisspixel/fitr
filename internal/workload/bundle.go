package workload

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/blisspixel/fitr/internal/boundedio"
	"github.com/blisspixel/fitr/internal/strictjson"
)

const maximumBundleBytes = 64 << 20

func (bundle Bundle) Validate() error {
	if bundle.Schema != BundleSchema {
		return fmt.Errorf("unsupported workload bundle schema %q", bundle.Schema)
	}
	if err := bundle.Plan.Validate(); err != nil {
		return err
	}
	if len(bundle.Trials) != bundle.Plan.Trials {
		return errors.New("workload bundle trial count does not match the plan")
	}
	seen := make(map[int]bool, len(bundle.Trials))
	for _, trial := range bundle.Trials {
		if seen[trial.Index] {
			return errors.New("workload bundle repeats a trial index")
		}
		seen[trial.Index] = true
		if err := trial.Validate(bundle.Plan); err != nil {
			return fmt.Errorf("workload trial %d: %w", trial.Index, err)
		}
	}
	rebuilt := Analyze(bundle.Plan, bundle.Trials)
	rebuiltJSON, rebuiltErr := json.Marshal(rebuilt)
	storedJSON, storedErr := json.Marshal(bundle.Report)
	if rebuiltErr != nil || storedErr != nil || !bytes.Equal(rebuiltJSON, storedJSON) {
		return errors.New("stored workload report does not match its signed trials")
	}
	return nil
}

func (bundle Bundle) JSON() ([]byte, error) {
	if err := bundle.Validate(); err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		return nil, err
	}
	if len(data) > maximumBundleBytes {
		return nil, fmt.Errorf("workload bundle exceeds %d bytes", maximumBundleBytes)
	}
	return append(data, '\n'), nil
}

func LoadBundle(path string) (Bundle, error) {
	data, err := boundedio.ReadFile(path, maximumBundleBytes)
	if err != nil {
		return Bundle{}, err
	}
	return decodeBundle(data)
}

func decodeBundle(data []byte) (Bundle, error) {
	if len(data) > maximumBundleBytes {
		return Bundle{}, fmt.Errorf("workload bundle exceeds %d bytes", maximumBundleBytes)
	}
	if err := strictjson.Validate(data); err != nil {
		return Bundle{}, fmt.Errorf("invalid workload bundle JSON: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var bundle Bundle
	if err := decoder.Decode(&bundle); err != nil {
		return Bundle{}, fmt.Errorf("invalid workload bundle JSON: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return Bundle{}, errors.New("invalid workload bundle JSON: trailing content")
		}
		return Bundle{}, fmt.Errorf("invalid workload bundle JSON: %w", err)
	}
	if err := bundle.Validate(); err != nil {
		return Bundle{}, fmt.Errorf("invalid workload bundle: %w", err)
	}
	return bundle, nil
}
