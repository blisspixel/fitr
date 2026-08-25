package eval

import "testing"

// A user task file is arbitrary JSON that ends up driving generation: family
// names select a generator, and numeric parameters index fixed pools. The
// decoder, the validator and the generator therefore have to agree about what
// is satisfiable, or a file in ~/.fitr/tasks crashes the run that reads it.
//
// The invariant: anything ValidateCheck accepts, Generate must survive.
func FuzzUserCheck(f *testing.F) {
	f.Add(`{"id":"a","kind":"check","family":"csv_strict","need":"structured_output","num_predict":64,"params":{"rows":4}}`)
	f.Add(`{"id":"a","kind":"check","family":"csv_strict","need":"structured_output","num_predict":64,"params":{"rows":-1}}`)
	f.Add(`{"id":"a","kind":"check","family":"json_object","need":"structured_output","num_predict":64,"params":{}}`)
	f.Add(`{"id":"a","kind":"check","family":"math_chain","need":"reasoning","num_predict":64,"params":{"steps":3}}`)
	f.Add(`{"id":"a","kind":"check","family":"static","need":"user_tasks","num_predict":64,"params":{"prompt":"hi","grader":{"type":"exact","value":"x"}}}`)
	f.Add(`{"id":"a","kind":"check","family":"keywords","need":"instruction_precision","num_predict":64,"params":{"count":1e18}}`)
	f.Add(`{"kind":"exec"}`)
	f.Add(`{}`)
	f.Add(``)

	f.Fuzz(func(t *testing.T, body string) {
		var cs CheckSpec
		if err := decodeUserCheck([]byte(body), &cs); err != nil {
			return
		}
		if err := ValidateCheck(cs); err != nil {
			return
		}
		// Accepted. Generation must now be safe, and must describe a task.
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("accepted task panicked in Generate: %v\nspec=%+v\nbody=%s", r, cs, body)
			}
		}()
		inst := Generate(cs, 0x5EED)
		if inst.Grade == nil {
			t.Fatalf("accepted task generated no grader: %+v", cs)
		}
		if inst.Prompt == "" {
			t.Fatalf("accepted task generated an empty prompt: %+v", cs)
		}
		// A generated instance must grade its own canonical answer as correct,
		// or the task cannot measure anything.
		if inst.Canon != "" {
			if ok, why := inst.Grade(inst.Canon); !ok {
				t.Fatalf("task cannot grade its own canon as passing: %s\nspec=%+v", why, cs)
			}
		}
	})
}
