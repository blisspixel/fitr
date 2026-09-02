package main

import (
	"math"
	"strconv"
	"testing"
)

func TestCapacityBudgetFlagIsPreserved(t *testing.T) {
	command, code, ok := parseRunCommand([]string{"model", "--capacity-budget-gb", "20.5"}, nil)
	if !ok || code != exitOK {
		t.Fatalf("parseRunCommand = ok %v, code %d", ok, code)
	}
	if command.capacityBudgetGB == nil || *command.capacityBudgetGB != 20.5 {
		t.Fatalf("capacity budget = %v, want 20.5", command.capacityBudgetGB)
	}
	if command.capacityReserveGB != nil {
		t.Fatalf("capacity reserve = %v, want nil", command.capacityReserveGB)
	}
}

func TestCapacityReserveAllowsZero(t *testing.T) {
	command, code, ok := parseRunCommand([]string{"model", "--capacity-reserve-gb", "0"}, nil)
	if !ok || code != exitOK {
		t.Fatalf("parseRunCommand = ok %v, code %d", ok, code)
	}
	if command.capacityReserveGB == nil || *command.capacityReserveGB != 0 {
		t.Fatalf("capacity reserve = %v, want 0", command.capacityReserveGB)
	}
}

func TestCapacityPolicyFlagsAreMutuallyExclusive(t *testing.T) {
	_, code, ok := parseRunCommand([]string{
		"model", "--capacity-budget-gb", "20", "--capacity-reserve-gb", "4",
	}, nil)
	if ok || code != exitUsage {
		t.Fatalf("parseRunCommand = ok %v, code %d; want usage rejection", ok, code)
	}
}

func TestCapacityFlagsRejectInvalidNumbers(t *testing.T) {
	tests := []struct {
		name  string
		flag  string
		value float64
	}{
		{name: "zero budget", flag: "--capacity-budget-gb", value: 0},
		{name: "negative budget", flag: "--capacity-budget-gb", value: -0.5},
		{name: "nan budget", flag: "--capacity-budget-gb", value: math.NaN()},
		{name: "infinite budget", flag: "--capacity-budget-gb", value: math.Inf(1)},
		{name: "negative reserve", flag: "--capacity-reserve-gb", value: -0.5},
		{name: "nan reserve", flag: "--capacity-reserve-gb", value: math.NaN()},
		{name: "infinite reserve", flag: "--capacity-reserve-gb", value: math.Inf(1)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, code, ok := parseRunCommand([]string{
				"model", tc.flag, strconv.FormatFloat(tc.value, 'g', -1, 64),
			}, nil)
			if ok || code != exitUsage {
				t.Fatalf("parseRunCommand = ok %v, code %d; want usage rejection", ok, code)
			}
		})
	}
}
