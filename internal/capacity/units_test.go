package capacity

import (
	"math"
	"testing"
)

func TestGiBBytesRoundsBeforeTheIntegerBound(t *testing.T) {
	for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), -1} {
		if _, err := GiBBytes(value, false); err == nil {
			t.Fatalf("accepted %v", value)
		}
	}
	if _, err := GiBBytes(0, true); err == nil {
		t.Fatal("a required positive budget rounded to zero")
	}
	if _, err := GiBBytes(1e-20, true); err == nil {
		t.Fatal("a sub-byte positive budget rounded to zero")
	}
	if _, err := GiBBytes(float64(math.MaxInt64)/(1<<30), true); err == nil {
		t.Fatal("a rounded overflow became an int")
	}
	got, err := GiBBytes(1.5, true)
	if err != nil || got != (1<<30)+(1<<29) {
		t.Fatalf("1.5 GiB = %d err %v", got, err)
	}
	got, err = GiBBytes(0, false)
	if err != nil || got != 0 {
		t.Fatalf("zero reserve = %d err %v", got, err)
	}
}
