package main

import "testing"

func TestOptionalNonnegativeGiBBytesPreservesZeroReserve(t *testing.T) {
	zero := 0.0
	got, err := optionalNonnegativeGiBBytes(&zero)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || *got != 0 {
		t.Fatalf("zero reserve = %v, want pointer to zero", got)
	}
}

func TestOptionalPositiveGiBBytesRejectsZero(t *testing.T) {
	zero := 0.0
	if _, err := optionalPositiveGiBBytes(&zero); err == nil {
		t.Fatal("zero positive capacity accepted")
	}
}
