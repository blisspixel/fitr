package eval

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// NumCtx is the default request context. Override per run with WithNumCtx
// so `fitr advise`'s num_ctx remedy is something `fitr run --ctx` can apply.
const NumCtx = 8192

// ctxKeyPrefix is appended to a device key when the request context is not
// the default. Board groups by the full key so 4096 is never ranked against
// 8192, then strips this suffix to label the split as context, not hardware.
const ctxKeyPrefix = "|ctx="

type numCtxKey struct{}

// WithNumCtx pins the request context for this evaluation. n<=0 keeps NumCtx.
func WithNumCtx(ctx context.Context, n int) context.Context {
	if n <= 0 {
		n = NumCtx
	}
	return context.WithValue(ctx, numCtxKey{}, n)
}

func numCtx(ctx context.Context) int {
	if n, ok := ctx.Value(numCtxKey{}).(int); ok && n > 0 {
		return n
	}
	return NumCtx
}

// ResolvedCtx is the public form of numCtx for callers that record it.
func ResolvedCtx(n int) int {
	if n <= 0 {
		return NumCtx
	}
	return n
}

// CtxKeySuffix is empty at the default context, otherwise "|ctx=N".
func CtxKeySuffix(n int) string {
	n = ResolvedCtx(n)
	if n == NumCtx {
		return ""
	}
	return fmt.Sprintf("%s%d", ctxKeyPrefix, n)
}

// ParseKeyCtx reads a "|ctx=N" suffix. Zero means the key is default-ctx
// (or predates the suffix).
func ParseKeyCtx(key string) int {
	i := strings.LastIndex(key, ctxKeyPrefix)
	if i < 0 {
		return 0
	}
	n, err := strconv.Atoi(key[i+len(ctxKeyPrefix):])
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// HardwareKey strips a "|ctx=N" suffix so two runs on the same box at
// different request contexts still compare as the same machine.
func HardwareKey(key string) string {
	if i := strings.LastIndex(key, ctxKeyPrefix); i >= 0 {
		return key[:i]
	}
	return key
}
