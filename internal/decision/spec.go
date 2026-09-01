package decision

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/blisspixel/fitr/internal/boundedio"
	"github.com/blisspixel/fitr/internal/strictjson"
)

const maximumSpecBytes = 1 << 20

// LoadSpec reads one strict decision-spec JSON document. Unknown fields and
// trailing values are rejected so a misspelled requirement cannot disappear
// silently while the command appears to succeed.
func LoadSpec(path string) (DecisionSpec, error) {
	data, err := boundedio.ReadFile(path, maximumSpecBytes)
	if err != nil {
		return DecisionSpec{}, fmt.Errorf("open decision spec: %w", err)
	}
	if err := strictjson.Validate(data); err != nil {
		return DecisionSpec{}, fmt.Errorf("decode decision spec: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var spec DecisionSpec
	if err := decoder.Decode(&spec); err != nil {
		return DecisionSpec{}, fmt.Errorf("decode decision spec: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return DecisionSpec{}, errors.New("decode decision spec: trailing JSON value")
		}
		return DecisionSpec{}, fmt.Errorf("decode decision spec: %w", err)
	}
	if err := spec.Validate(); err != nil {
		return DecisionSpec{}, err
	}
	return spec, nil
}
