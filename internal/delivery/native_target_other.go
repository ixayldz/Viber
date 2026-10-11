//go:build !windows

package delivery

import (
	"context"

	c "github.com/ixayldz/Viber/internal/contracts"
)

func inspectNativeTarget(string, []NativeFileIntent) (NativeTargetSeal, error) {
	return NativeTargetSeal{}, c.Fail(c.UnsupportedCapability, "exclusive live apply is unsupported on this operating system")
}

func acquireNativeTarget(context.Context, NativeTargetSeal, map[string]string) (NativeTargetLease, error) {
	return nil, c.Fail(c.UnsupportedCapability, "exclusive live apply is unsupported on this operating system")
}
