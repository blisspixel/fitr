package advise

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"time"
)

// ParseFitLog reads llama-fit-params / llama.cpp --fit stderr. The LAST
// "projected to use N MiB" is the fitted figure; earlier lines are the
// initial guess. "cannot fulfill" means even the fitter could not stay
// inside free device memory at this context.
func ParseFitLog(log string) (usedB int64, cannot bool, ok bool) {
	re := regexp.MustCompile(`projected to use (\d+) MiB of device memory`)
	all := re.FindAllStringSubmatch(log, -1)
	if len(all) == 0 {
		return 0, false, false
	}
	n, err := strconv.ParseInt(all[len(all)-1][1], 10, 64)
	if err != nil || n <= 0 {
		return 0, false, false
	}
	cannot = regexp.MustCompile(`cannot fulfill`).FindString(log) != ""
	return n * 1024 * 1024, cannot, true
}

// RunFitParams invokes llama-fit-params when it is on PATH. Missing binary
// is not an error: the caller keeps the weights+KV estimate and says so.
func RunFitParams(ctx context.Context, gguf string, ctxSize int) (usedB int64, cannot bool, err error) {
	bin, err := exec.LookPath("llama-fit-params")
	if err != nil {
		return 0, false, err
	}
	if ctxSize <= 0 {
		ctxSize = 8192
	}
	cctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, bin, "-m", gguf, "-c", strconv.Itoa(ctxSize))
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	runErr := cmd.Run()
	usedB, cannot, ok := ParseFitLog(buf.String())
	if !ok {
		if runErr != nil {
			return 0, false, runErr
		}
		return 0, false, fmt.Errorf("llama-fit-params produced no projection")
	}
	return usedB, cannot, nil
}
