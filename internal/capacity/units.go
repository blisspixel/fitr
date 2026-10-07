package capacity

import (
	"errors"
	"math"
)

// GiBBytes converts an operator quantity to bytes. Rounding happens before
// bounds checks: float64 cannot distinguish MaxInt64 from the first overflow.
// A required positive budget must also remain positive after rounding.
func GiBBytes(value float64, positive bool) (int64, error) {
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return 0, errors.New("capacity value is outside the supported byte range")
	}
	bytes := math.Round(value * (1 << 30))
	if bytes >= float64(math.MaxInt64) || (positive && bytes < 1) {
		return 0, errors.New("capacity value is outside the supported byte range")
	}
	return int64(bytes), nil
}
