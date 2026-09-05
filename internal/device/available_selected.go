package device

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"math"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// SingleNVIDIAAvailable binds a current free-memory observation to the one
// accelerator described by the fingerprint. The first owned runtime profile
// refuses multiple cards instead of borrowing another card's free memory.
func SingleNVIDIAAvailable(ctx context.Context, expected Fingerprint) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "nvidia-smi", "--query-gpu=name,memory.total,memory.free", "--format=csv,noheader,nounits")
	cmd.WaitDelay = 250 * time.Millisecond
	out := &selectedMemoryOutput{}
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Run(); err != nil {
		return 0, errors.New("owned accelerator availability needs a successful bounded nvidia-smi observation")
	}
	return parseSingleNVIDIAAvailable(out.String(), expected)
}

type selectedMemoryOutput struct{ data bytes.Buffer }

func (out *selectedMemoryOutput) String() string { return out.data.String() }
func (out *selectedMemoryOutput) Len() int       { return out.data.Len() }

func (out *selectedMemoryOutput) Write(data []byte) (int, error) {
	if len(data) > 8192-out.Len() {
		return 0, errors.New("selected accelerator observation exceeds 8 KiB")
	}
	return out.data.Write(data)
}

func parseSingleNVIDIAAvailable(text string, expected Fingerprint) (int64, error) {
	reader := csv.NewReader(strings.NewReader(text))
	reader.TrimLeadingSpace = true
	reader.FieldsPerRecord = 3
	rows, err := reader.ReadAll()
	if err != nil || len(rows) != 1 || expected.VRAMSource != "nvidia-smi" ||
		!strings.EqualFold(strings.TrimSpace(rows[0][0]), strings.TrimSpace(expected.GPU)) {
		return 0, errors.New("owned accelerator observation is missing, ambiguous or differs from the selected device")
	}
	totalMiB, totalErr := strconv.ParseInt(strings.TrimSpace(rows[0][1]), 10, 64)
	freeMiB, freeErr := strconv.ParseInt(strings.TrimSpace(rows[0][2]), 10, 64)
	if totalErr != nil || freeErr != nil || totalMiB <= 0 || totalMiB > 1<<24 || freeMiB < 0 || freeMiB > totalMiB ||
		math.IsNaN(expected.VRAMGb) || math.IsInf(expected.VRAMGb, 0) ||
		math.Abs(float64(totalMiB)/1024-expected.VRAMGb) > 1.0/1024 {
		return 0, errors.New("owned accelerator capacity changed or free memory is invalid")
	}
	return freeMiB << 20, nil
}
