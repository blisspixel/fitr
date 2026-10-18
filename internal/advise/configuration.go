package advise

import (
	"strconv"
	"strings"

	"github.com/blisspixel/fitr/internal/device"
)

// ConfigurationAccountSchema names the provenance projection. It is not a
// sealed run record and it is not a ranking.
const ConfigurationAccountSchema = "fitr.configuration-account.v1"

const (
	// OriginMeasured is a value read from the serving runtime.
	OriginMeasured = "measured"
	// OriginDeclared is a value stored by the artifact author.
	OriginDeclared = "declared"
	// OriginRuntimeDefault is a value this tree supplied because nobody set one.
	OriginRuntimeDefault = "runtime-default"
	// OriginRequested is an explicit plan value that has not been observed
	// on a running server.
	OriginRequested = "requested"
	// OriginHistorical is a recorded value from before configuration
	// provenance existed. It is not a new measurement.
	OriginHistorical = "historical"
	// OriginUnobserved means the value was not read. No number is filled in.
	OriginUnobserved = "unobserved"
	// OriginProcessEnvironment is a value from this process, not the server.
	OriginProcessEnvironment = "process-environment"
)

const declaredSamplingNote = "declared by the artifact. a runtime that does not read this key uses its own default instead."

// ConfigurationValue is one setting and why that value is on the account.
// An empty Value means the account is refusing to invent one.
type ConfigurationValue struct {
	Name   string `json:"name"`
	Value  string `json:"value,omitempty"`
	Origin string `json:"origin"`
	Note   string `json:"note,omitempty"`
}

// ConfigurationAccount distinguishes where each setting came from.
// It does not choose a winner and it does not score the configuration.
type ConfigurationAccount struct {
	Schema string               `json:"schema"`
	Values []ConfigurationValue `json:"values,omitempty"`
	Note   string               `json:"note,omitempty"`
}

// PlanKV is a cache choice already resolved on a fitting plan.
// Explicit is false when the operator omitted it and the fitter substituted one.
type PlanKV struct {
	Type     string
	Flash    bool
	Explicit bool
}

// PlanRepeats is the repeat count on a fitting plan.
type PlanRepeats struct {
	N         int
	Defaulted bool
}

// ConfigurationInput is the evidence the account is allowed to cite.
// A zero field stays unobserved. The account does not look up a runtime's
// published sampler defaults.
type ConfigurationInput struct {
	Config       map[string]string
	ConfigSource string
	Author       *AuthorSampling
	PlanKV       *PlanKV
	PlanRepeats  *PlanRepeats
	// FitKVAssumed is the f16 stand-in fit arithmetic uses when no dtype was measured.
	FitKVAssumed bool
	// ProcessKV is a dtype taken from this process. It is not a server reading.
	ProcessKV string
}

// Configuration projects provenance for the settings it was given.
func Configuration(in ConfigurationInput) *ConfigurationAccount {
	account := &ConfigurationAccount{Schema: ConfigurationAccountSchema}
	account.Values = append(account.Values, servingValues(in.Config, in.ConfigSource)...)
	sampling, note := samplingValues(in.Author)
	account.Values = append(account.Values, sampling...)
	account.Note = note
	account.Values = append(account.Values, planKVValues(in.PlanKV)...)
	account.Values = append(account.Values, repeatValues(in.PlanRepeats)...)
	account.Values = append(account.Values, processValue(in.ProcessKV, account.Values)...)
	account.Values = append(account.Values, fitAssumption(in.FitKVAssumed, account.Values)...)
	return account
}

// Lines is the text projection. Unobserved rows and the fit-arithmetic
// stand-in stay in JSON. The fit stand-in is already a gap sentence, and
// saying "declares none" on every ordinary model would be noise.
func (a *ConfigurationAccount) Lines() []string {
	if a == nil {
		return nil
	}
	var lines []string
	for _, row := range a.Values {
		if row.Origin == OriginUnobserved || row.Name == "fit_kv_dtype" {
			continue
		}
		text := row.Name + "  " + row.Origin
		if row.Value != "" {
			text = row.Name + "  " + row.Value + "  " + row.Origin
		}
		if row.Note != "" {
			text += "  " + row.Note
		}
		lines = append(lines, text)
	}
	if len(lines) > 0 && a.Note != "" {
		lines = append(lines, a.Note)
	}
	return lines
}

func configurationFrom(in Input) *ConfigurationAccount {
	parsed := ConfigurationInput{
		Config: in.Config, ConfigSource: in.ConfigSource, Author: in.AuthorSampling,
	}
	if in.KVBytes > 0 && strings.HasPrefix(in.KVSrc, "OLLAMA_KV_CACHE_TYPE=") {
		parsed.ProcessKV = strings.TrimPrefix(in.KVSrc, "OLLAMA_KV_CACHE_TYPE=")
	} else if in.KVBytes == 0 {
		parsed.FitKVAssumed = true
	}
	return Configuration(parsed)
}

func servingObserved(source string) bool {
	return source == device.ConfigSourceServerLog || source == device.ConfigSourceOwnedLaunch ||
		source == device.ConfigSourceListener
}

func servingValues(config map[string]string, source string) []ConfigurationValue {
	if source == "" {
		return historicalServing(config)
	}
	if !servingObserved(source) {
		return []ConfigurationValue{valueRow("serving", "", OriginUnobserved,
			"the serving runtime was not read. start-time values are not measurements.")}
	}
	var rows []ConfigurationValue
	for _, knob := range device.ServingKnobs() {
		if knob.Scope != device.KnobStart {
			continue
		}
		value := config[knob.Name]
		if value == "" {
			rows = append(rows, valueRow(knob.Name, "", OriginUnobserved,
				"the serving runtime was read and this key was absent. a runtime default was not filled in."))
			continue
		}
		rows = append(rows, valueRow(knob.Name, value, OriginMeasured,
			"observed from the serving runtime on this machine."))
	}
	return rows
}

func historicalServing(config map[string]string) []ConfigurationValue {
	var rows []ConfigurationValue
	for _, knob := range device.ServingKnobs() {
		if knob.Scope != device.KnobStart {
			continue
		}
		value := config[knob.Name]
		if value == "" {
			continue
		}
		rows = append(rows, valueRow(knob.Name, value, OriginHistorical,
			"recorded before configuration provenance. not a measurement of the serving runtime."))
	}
	return rows
}

func samplingValues(author *AuthorSampling) ([]ConfigurationValue, string) {
	if author == nil || !author.Declared() {
		return []ConfigurationValue{valueRow("sampling", "", OriginUnobserved,
			"the artifact declares no sampler settings. a runtime default was not read and is not filled in.")}, ""
	}
	var rows []ConfigurationValue
	addFloat := func(name string, v *float64) {
		if v != nil {
			rows = append(rows, valueRow(name, strconv.FormatFloat(*v, 'g', -1, 64), OriginDeclared, declaredSamplingNote))
		}
	}
	addInt := func(name string, v *int) {
		if v != nil {
			rows = append(rows, valueRow(name, strconv.Itoa(*v), OriginDeclared, declaredSamplingNote))
		}
	}
	addFloat("temp", author.Temp)
	addInt("top_k", author.TopK)
	addFloat("top_p", author.TopP)
	addFloat("min_p", author.MinP)
	addFloat("penalty_repeat", author.PenaltyRepeat)
	addInt("penalty_last_n", author.PenaltyLastN)
	addInt("mirostat", author.Mirostat)
	addFloat("xtc_probability", author.XTCProbability)
	addFloat("xtc_threshold", author.XTCThreshold)
	addFloat("mirostat_tau", author.MirostatTau)
	addFloat("mirostat_eta", author.MirostatEta)
	if author.Sequence != "" {
		rows = append(rows, valueRow("sequence", author.Sequence, OriginDeclared, declaredSamplingNote))
	}
	return rows, "sampler fields the artifact omits stay absent. they are not filled from a runtime default."
}

func planKVValues(kv *PlanKV) []ConfigurationValue {
	if kv == nil {
		return nil
	}
	origin := OriginRequested
	note := "requested on the plan. it is not proof a running server applied it."
	if !kv.Explicit {
		origin = OriginRuntimeDefault
		note = "owned-runtime default because nobody set it. it is not a measurement and not an artifact declaration."
	}
	return []ConfigurationValue{
		valueRow("kv_cache_type", kv.Type, origin, note),
		valueRow("flash_attention", strconv.FormatBool(kv.Flash), origin, note),
	}
}

func repeatValues(rep *PlanRepeats) []ConfigurationValue {
	if rep == nil {
		return nil
	}
	origin := OriginRequested
	note := "requested repeat count. it is not a quality result."
	if rep.Defaulted {
		origin = OriginRuntimeDefault
		note = "runtime-default; minimum rankable schedule; no repeat count was requested. nobody chose this count."
	}
	return []ConfigurationValue{valueRow("repeats", strconv.Itoa(rep.N), origin, note)}
}

func processValue(processKV string, rows []ConfigurationValue) []ConfigurationValue {
	if processKV == "" || named(rows, "OLLAMA_KV_CACHE_TYPE") {
		return nil
	}
	return []ConfigurationValue{valueRow("OLLAMA_KV_CACHE_TYPE", processKV, OriginProcessEnvironment,
		"read from this process and used only as a fit-arithmetic input. it is not a measurement of the serving runtime.")}
}

func fitAssumption(assumed bool, rows []ConfigurationValue) []ConfigurationValue {
	if !assumed || named(rows, "OLLAMA_KV_CACHE_TYPE") || named(rows, "kv_cache_type") {
		return nil
	}
	return []ConfigurationValue{valueRow("fit_kv_dtype", "f16", OriginRuntimeDefault,
		"fit arithmetic assumed f16 because the cache dtype was not measured. this is not a reading of the server.")}
}

func named(rows []ConfigurationValue, name string) bool {
	for _, row := range rows {
		if row.Name == name {
			return true
		}
	}
	return false
}

func valueRow(name, value, origin, note string) ConfigurationValue {
	return ConfigurationValue{Name: name, Value: value, Origin: origin, Note: note}
}
