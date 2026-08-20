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

func TestCtxKeySuffixSplitsOnlyNonDefault(t *testing.T) {
	if CtxKeySuffix(NumCtx) != "" || CtxKeySuffix(0) != "" {
		t.Fatal("default ctx must not change the device key")
	}
	if got := CtxKeySuffix(4096); got != "|ctx=4096" {
		t.Fatalf("got %q", got)
	}
	key := "lappy|780M|driver|vulkan|0.32.14|1|f16|ctx=4096"
	if ParseKeyCtx(key) != 4096 {
		t.Fatalf("ParseKeyCtx = %d", ParseKeyCtx(key))
	}
	if ParseKeyCtx("lappy|780M|driver") != 0 {
		t.Fatal("a default-ctx key has no suffix")
	}
	hw := HardwareKey(key)
	if hw != "lappy|780M|driver|vulkan|0.32.14|1|f16" {
		t.Fatalf("HardwareKey = %q", hw)
	}
	if HardwareKey(hw) != hw {
		t.Fatal("HardwareKey must be idempotent")
	}
}
