package eval

import "context"

// NumCtx is the default request context. Override per run with WithNumCtx
// so `fitr advise`'s num_ctx remedy is something `fitr run --ctx` can apply.
const NumCtx = 8192

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
