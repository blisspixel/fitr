package decision

import (
	"strings"
	"testing"
)

func TestEachIdentityFieldIsComparedOnItsOwn(t *testing.T) {
	for _, field := range []string{
		"artifact_digest", "quant", "runtime", "compute_backend", "driver", "os",
		"kernel", "hardware", "effective_context", "concurrency", "residency", "harness",
	} {
		t.Run(field, func(t *testing.T) {
			report, err := LoadRequalification(differingIdentity(t, field))
			if err != nil {
				t.Fatal(err)
			}
			got := report.Claims[0]
			if got.State != RequalifyStale || len(got.Changed) != 1 || got.Changed[0] != field {
				t.Fatalf("%s: %+v", field, got)
			}
		})
	}
}

func TestRequalificationInputRejectsMalformedIdentity(t *testing.T) {
	base := string(differingIdentity(t, "runtime"))
	cases := map[string]string{
		"duplicate schema":    strings.Replace(base, `"schema"`, `"schema":"fitr.requalification-input.v1","schema"`, 1),
		"numeric value":       strings.Replace(base, `"left"`, `1`, 1),
		"unknown state":       strings.Replace(base, `"observed"`, `"Observed"`, 1),
		"unknown dependency":  strings.Replace(base, `"depends_on":["runtime"]`, `"depends_on":["model_name"]`, 1),
		"value on unknown":    strings.Replace(base, `"state":"observed","value":"right"`, `"state":"unknown","value":"right"`, 1),
		"wrong schema":        strings.Replace(base, RequalificationInputSchema, "fitr.requalification.v1", 1),
		"no claims":           strings.Replace(base, `"claims":[{"id":"fit","depends_on":["runtime"]}]`, `"claims":[]`, 1),
		"repeated claim":      strings.Replace(base, `[{"id":"fit","depends_on":["runtime"]}]`, `[{"id":"fit","depends_on":["runtime"]},{"id":"fit","depends_on":["quant"]}]`, 1),
		"repeated dependency": strings.Replace(base, `["runtime"]`, `["runtime","runtime"]`, 1),
		"control in value":    strings.Replace(base, "right", `right\n`, 1),
		"extra field":         strings.Replace(base, `"prior"`, `"host":"box","prior"`, 1),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := LoadRequalification([]byte(body)); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

func TestPresentationDoesNotScheduleRequalification(t *testing.T) {
	body := strings.Replace(string(differingIdentity(t, "runtime")), `"value":"right"`, `"value":"left"`, 1)
	body = strings.Replace(body, `"current":{`, `"current":{"presentation":"narrow","model_name":"library-name","announcement":"please upgrade",`, 1)
	report, err := LoadRequalification([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if report.Claims[0].State != RequalifyValid || report.Next != nil {
		t.Fatalf("%+v", report)
	}
	text := strings.Join(report.Lines(), "\n")
	if strings.Contains(text, "please upgrade") || strings.Contains(text, "next") || strings.Contains(text, "pass") {
		t.Fatalf("lines: %s", text)
	}
}

func TestRequalificationLinesKeepChangedAndUnknown(t *testing.T) {
	prior := installedStack()
	current := installedStack()
	current.Kernel = observed("6.13.0")
	current.Driver = IdentityFact{}
	text := strings.Join(Requalify(prior, current, []RequalificationClaimInput{
		claim("stack", "kernel", "driver"),
	}).Lines(), "\n")
	if !strings.Contains(text, "stale  stack  changed kernel  unknown driver") || strings.Contains(text, "rerun") {
		t.Fatalf("lines: %s", text)
	}
}

func differingIdentity(t *testing.T, field string) []byte {
	t.Helper()
	prior := identityObject("left")
	current := identityObject("left")
	needle := `"` + field + `":{"state":"observed","value":"left"}`
	if !strings.Contains(current, needle) {
		t.Fatalf("current fixture has no %s", field)
	}
	current = strings.Replace(current, needle, `"`+field+`":{"state":"observed","value":"right"}`, 1)
	return []byte(`{"schema":"` + RequalificationInputSchema + `","prior":` + prior +
		`,"current":` + current + `,"claims":[{"id":"fit","depends_on":["` + field + `"]}]}`)
}

func identityObject(value string) string {
	fact := `{"state":"observed","value":"` + value + `"}`
	fields := []string{
		"artifact_digest", "quant", "runtime", "compute_backend", "driver", "os",
		"kernel", "hardware", "effective_context", "concurrency", "residency", "harness",
	}
	parts := make([]string, 0, len(fields))
	for _, field := range fields {
		parts = append(parts, `"`+field+`":`+fact)
	}
	return "{" + strings.Join(parts, ",") + "}"
}
