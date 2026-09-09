package main

import (
	"flag"
	"strings"
	"testing"

	"github.com/blisspixel/fitr/internal/render"
)

// permute reorders arguments so `fitr run model --flag value` works, and it
// needs takesValue to know which flags consume the following token. A value
// flag missing from that list does not fail loudly: permute leaves its value
// behind as a positional, and the command reports "too many arguments" while
// naming neither the flag nor the real cause.
//
// The list is hand-maintained, so this walks the real flag sets instead. Any
// non-boolean flag registered on a command whose arguments go through permute
// must be declared, and the failure names the flag to add.
func TestEveryRegisteredValueFlagIsKnownToThePermuter(t *testing.T) {
	for name, register := range map[string]func() *flag.FlagSet{
		"run": func() *flag.FlagSet {
			fs, _ := newRunFlagSet(render.New("none"))
			return fs
		},
		"top run": func() *flag.FlagSet {
			fs := flag.NewFlagSet("top run", flag.ContinueOnError)
			registerTopRunPreviewFlags(fs)
			return fs
		},
	} {
		t.Run(name, func(t *testing.T) {
			register().VisitAll(func(f *flag.Flag) {
				if isBoolFlag(f) || takesValue(f.Name) {
					return
				}
				t.Errorf("--%s takes a value but is missing from takesValue; "+
					"permute will strand its value as a positional argument", f.Name)
			})
		})
	}
}

// The flag package marks a boolean by having its Value implement IsBoolFlag.
// Those are the only flags permute may leave without a following token.
func isBoolFlag(f *flag.Flag) bool {
	boolFlag, ok := f.Value.(interface{ IsBoolFlag() bool })
	return ok && boolFlag.IsBoolFlag()
}

// The stranded-value symptom itself, so a regression is recognizable from the
// test name rather than from an unrelated argument-count diagnostic.
func TestValueFlagAfterThePositionalKeepsItsValue(t *testing.T) {
	command, code, ok := parseRunCommand([]string{"model", "--context-tiers", "2048,8192"}, nil)
	if !ok || code != exitOK {
		t.Fatalf("parseRunCommand = ok %v, code %d, want the tier value bound to its flag", ok, code)
	}
	if len(command.contextTiers) != 2 {
		t.Fatalf("contextTiers = %v, want both declared tiers", command.contextTiers)
	}
	if strings.Contains(command.model, ",") {
		t.Fatalf("model = %q, want the tier list kept out of the positional argument", command.model)
	}
}
