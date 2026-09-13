package httpx

import (
	"context"
	"strings"
)

type mcpClientBindingContextKey struct{}

type mcpClientBinding struct {
	OwnerKey string
}

func withMCPClientBinding(ctx context.Context, ownerKey string) context.Context {
	return context.WithValue(ctx, mcpClientBindingContextKey{}, mcpClientBinding{OwnerKey: strings.TrimSpace(ownerKey)})
}

func mcpClientBindingFromContext(ctx context.Context) (mcpClientBinding, bool) {
	if ctx == nil {
		return mcpClientBinding{}, false
	}
	binding, ok := ctx.Value(mcpClientBindingContextKey{}).(mcpClientBinding)
	return binding, ok && strings.TrimSpace(binding.OwnerKey) != ""
}
