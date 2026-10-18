package advise

import (
	"strings"
	"testing"
)

func TestDeclaredSamplingStaysDeclared(t *testing.T) {
	temp := 0.6
	minP := 0.0
	account := Configuration(ConfigurationInput{Author: &AuthorSampling{Temp: &temp, MinP: &minP}})
	got := findAccountRow(t, account.Values, "temp", OriginDeclared)
	if got.Value != "0.6" {
		t.Fatalf("temp = %q", got.Value)
	}
	zero := findAccountRow(t, account.Values, "min_p", OriginDeclared)
	if zero.Value != "0" {
		t.Fatalf("a declared min_p of zero was dropped: %q", zero.Value)
	}
	for _, row := range account.Values {
		if row.Origin == OriginRuntimeDefault || row.Origin == OriginMeasured {
			t.Fatalf("a declared sampler became %s: %+v", row.Origin, row)
		}
	}
}

func TestAbsentSamplingIsUnobserved(t *testing.T) {
	account := Configuration(ConfigurationInput{})
	row := findAccountRow(t, account.Values, "sampling", OriginUnobserved)
	if row.Value != "" {
		t.Fatalf("an absent sampler was given a value: %q", row.Value)
	}
	if strings.Contains(row.Note, "0.8") || strings.Contains(strings.ToLower(row.Note), "temperature") {
		t.Fatalf("an absent sampler was filled with a runtime default: %q", row.Note)
	}
}

func TestObservedCacheIsMeasured(t *testing.T) {
	account := Configuration(ConfigurationInput{
		Config:       map[string]string{"OLLAMA_KV_CACHE_TYPE": "q8_0"},
		ConfigSource: "server-log",
	})
	got := findAccountRow(t, account.Values, "OLLAMA_KV_CACHE_TYPE", OriginMeasured)
	if got.Value != "q8_0" {
		t.Fatalf("measured cache = %q", got.Value)
	}
}

func TestUnobservedCacheIsNotAMeasurement(t *testing.T) {
	account := Configuration(ConfigurationInput{
		Config:       map[string]string{"OLLAMA_KV_CACHE_TYPE": "q8_0"},
		ConfigSource: "unobserved",
	})
	for _, row := range account.Values {
		if row.Value == "q8_0" || row.Origin == OriginMeasured {
			t.Fatalf("an unread runtime became a measurement: %+v", row)
		}
	}
	findAccountRow(t, account.Values, "serving", OriginUnobserved)
}

func TestHistoricalCacheIsNotMeasured(t *testing.T) {
	account := Configuration(ConfigurationInput{
		Config: map[string]string{"OLLAMA_KV_CACHE_TYPE": "f16"},
	})
	got := findAccountRow(t, account.Values, "OLLAMA_KV_CACHE_TYPE", OriginHistorical)
	if got.Value != "f16" || strings.Contains(got.Origin, OriginMeasured) {
		t.Fatalf("historical cache = %+v", got)
	}
	for _, row := range account.Values {
		if row.Origin == OriginMeasured {
			t.Fatalf("a record without provenance was measured: %+v", row)
		}
		if row.Name == "OLLAMA_FLASH_ATTENTION" {
			t.Fatalf("an unread historical key was treated as known absence: %+v", row)
		}
	}
}

func TestOmittedKVIsARuntimeDefault(t *testing.T) {
	account := Configuration(ConfigurationInput{PlanKV: &PlanKV{Type: "f16", Flash: true}})
	got := findAccountRow(t, account.Values, "kv_cache_type", OriginRuntimeDefault)
	if got.Value != "f16" || !strings.Contains(got.Note, "nobody") {
		t.Fatalf("omitted KV = %+v", got)
	}
	for _, row := range account.Values {
		if row.Name == "kv_cache_type" && (row.Origin == OriginDeclared || row.Origin == OriginMeasured) {
			t.Fatalf("an omitted KV was called %s", row.Origin)
		}
	}
}

func TestExplicitKVIsRequested(t *testing.T) {
	account := Configuration(ConfigurationInput{PlanKV: &PlanKV{Type: "q8_0", Flash: true, Explicit: true}})
	got := findAccountRow(t, account.Values, "kv_cache_type", OriginRequested)
	if got.Value != "q8_0" {
		t.Fatalf("requested KV = %+v", got)
	}
	for _, row := range account.Values {
		if row.Name == "kv_cache_type" && row.Origin != OriginRequested {
			t.Fatalf("an explicit KV was %s", row.Origin)
		}
	}
}

func TestFitAssumptionIsNotAServerReading(t *testing.T) {
	account := Configuration(ConfigurationInput{FitKVAssumed: true})
	got := findAccountRow(t, account.Values, "fit_kv_dtype", OriginRuntimeDefault)
	if got.Value != "f16" || !strings.Contains(got.Note, "not a reading") {
		t.Fatalf("fit assumption = %+v", got)
	}
}

func TestProcessEnvironmentIsNotMeasured(t *testing.T) {
	account := Configuration(ConfigurationInput{
		Config:       map[string]string{"OLLAMA_KV_CACHE_TYPE": "q4_0"},
		ConfigSource: "unobserved",
		ProcessKV:    "q4_0",
	})
	got := findAccountRow(t, account.Values, "OLLAMA_KV_CACHE_TYPE", OriginProcessEnvironment)
	if got.Value != "q4_0" {
		t.Fatalf("process environment = %+v", got)
	}
	for _, row := range account.Values {
		if row.Origin == OriginMeasured {
			t.Fatalf("this process's environment was measured: %+v", row)
		}
	}
}

func TestMeasuredAndRequestedKVBothStay(t *testing.T) {
	account := Configuration(ConfigurationInput{
		Config:       map[string]string{"OLLAMA_KV_CACHE_TYPE": "q8_0"},
		ConfigSource: "server-log",
		PlanKV:       &PlanKV{Type: "q4_0", Explicit: true},
	})
	measured := findAccountRow(t, account.Values, "OLLAMA_KV_CACHE_TYPE", OriginMeasured)
	requested := findAccountRow(t, account.Values, "kv_cache_type", OriginRequested)
	if measured.Value != "q8_0" || requested.Value != "q4_0" {
		t.Fatalf("disagreement collapsed: measured %+v requested %+v", measured, requested)
	}
}

func TestUnknownConfigSourceHidesValues(t *testing.T) {
	account := Configuration(ConfigurationInput{
		Config:       map[string]string{"OLLAMA_KV_CACHE_TYPE": "q8_0"},
		ConfigSource: "environment",
	})
	for _, row := range account.Values {
		if row.Value == "q8_0" || row.Origin == OriginMeasured {
			t.Fatalf("an unknown provenance became a measurement: %+v", row)
		}
	}
	findAccountRow(t, account.Values, "serving", OriginUnobserved)
}

func TestEvaluateAccountsAnAssumedCacheWithoutPrintingIt(t *testing.T) {
	report := Evaluate(Input{Model: "m"})
	if report.Configuration == nil || report.Configuration.Schema != ConfigurationAccountSchema {
		t.Fatal("advise JSON lost the configuration account")
	}
	findAccountRow(t, report.Configuration.Values, "fit_kv_dtype", OriginRuntimeDefault)
	for _, row := range report.Configuration.Values {
		if row.Origin == OriginMeasured {
			t.Fatalf("an empty advise input was measured: %+v", row)
		}
	}
	var buf strings.Builder
	Write(&buf, report)
	if strings.Contains(buf.String(), "fit_kv_dtype") || strings.Contains(buf.String(), "runtime-default") {
		t.Fatalf("the fit stand-in was printed beside the gap that already states it:\n%s", buf.String())
	}
}

func findAccountRow(t *testing.T, rows []ConfigurationValue, name, origin string) ConfigurationValue {
	t.Helper()
	for _, row := range rows {
		if row.Name == name && row.Origin == origin {
			return row
		}
	}
	t.Fatalf("no %s/%s in %+v", name, origin, rows)
	return ConfigurationValue{}
}
