// Package retonr is an OPTIONAL handoff to the sister project
// https://github.com/blisspixel/retonr.
//
// fitr works with no retonr installed. This package never calls retonr, never
// downloads it, and never claims a qualification. Retonr qualifies an exact
// artifact + runtime + hardware class; fitr measures one model on one device.
// The JSON here is device-measurement evidence a person (or retonr, later)
// can read. It is not an activation, license, or endorsement.
package retonr

import (
	"os/exec"
	"runtime"

	"github.com/blisspixel/fitr/internal/device"
	"github.com/blisspixel/fitr/internal/score"
)

const Schema = "fitr.retonr.evidence.v1"

const Disclaimer = "This is a fitr measurement of one model on one device. " +
	"It is not a retonr qualification, activation, or license decision. " +
	"Retonr qualifies an exact artifact, runtime, and hardware class; " +
	"a familiar model name is not enough."

const SisterURL = "https://github.com/blisspixel/retonr"

// Evidence is the stable, opt-in sidecar. Fields stay observational.
type Evidence struct {
	Schema      string `json:"schema"`
	Kind        string `json:"kind"`
	Disclaimer  string `json:"disclaimer"`
	Sister      string `json:"sister"`
	FitrVersion string `json:"fitr_version"`

	Model     string `json:"model"`
	Quant     string `json:"quant,omitempty"`
	Family    string `json:"family,omitempty"`
	ParamSize string `json:"param_size,omitempty"`
	Level     string `json:"level,omitempty"`
	Repeats   int    `json:"repeats,omitempty"`

	Device    device.Fingerprint `json:"device"`
	DeviceKey string             `json:"device_key"`
	Profile   string             `json:"profile,omitempty"`

	Needs  map[string]NeedObs `json:"needs"`
	Serves []string           `json:"serves,omitempty"`
	UseFor string             `json:"use_for,omitempty"`

	Plumbing string `json:"plumbing,omitempty"`
	Result   string `json:"result_path,omitempty"`
}

// NeedObs is one fitr need as an observation. State is PASS/FAIL/SKIP/n/a/BLKD.
type NeedObs struct {
	State string `json:"state"`
	Why   string `json:"why,omitempty"`
}

// LookPath returns the retonr executable if it is on PATH, else "".
func LookPath() string {
	names := []string{"retonr"}
	if runtime.GOOS == "windows" {
		names = []string{"retonr.exe", "retonr"}
	}
	for _, n := range names {
		if p, err := exec.LookPath(n); err == nil {
			return p
		}
	}
	return ""
}

// Hint is the one-line next step when retonr is installed. Empty otherwise.
func Hint(model string) string {
	if LookPath() == "" {
		return ""
	}
	if model == "" {
		model = "<model>"
	}
	return "fitr export " + model + " --retonr   # evidence for retonr; not a qualification"
}

// FromScorecard builds evidence from a scored result. Missing needs stay
// missing; we do not invent PASS.
func FromScorecard(fitrVersion, model, quant, family, paramSize, level string, repeats int,
	fp device.Fingerprint, deviceKey, profile string,
	sc score.Scorecard, plumbing, resultPath string) Evidence {
	needs := map[string]NeedObs{}
	for k, v := range sc.Needs {
		needs[k] = NeedObs{State: string(v.State), Why: v.Why}
	}
	return Evidence{
		Schema: Schema, Kind: "device_measurement",
		Disclaimer: Disclaimer, Sister: SisterURL,
		FitrVersion: fitrVersion,
		Model:       model, Quant: quant, Family: family, ParamSize: paramSize,
		Level: level, Repeats: repeats,
		Device: fp, DeviceKey: deviceKey, Profile: profile,
		Needs: needs, Serves: sc.Serves, UseFor: sc.UseFor,
		Plumbing: plumbing, Result: resultPath,
	}
}
