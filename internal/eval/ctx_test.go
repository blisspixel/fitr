package eval

import (
	"context"
	"testing"
)

func TestWithNumCtxOverridesDefault(t *testing.T) {
	if numCtx(context.Background()) != NumCtx {
		t.Fatal("unset context must be the 8192 default")
	}
	ctx := WithNumCtx(context.Background(), 4096)
	if numCtx(ctx) != 4096 {
		t.Fatalf("got %d, want 4096", numCtx(ctx))
	}
	if numCtx(WithNumCtx(context.Background(), 0)) != NumCtx {
		t.Fatal("0 must mean default, not a zero-token window")
	}
	if ResolvedCtx(21760) != 21760 || ResolvedCtx(0) != NumCtx {
		t.Fatal("ResolvedCtx must match WithNumCtx")
	}
}
