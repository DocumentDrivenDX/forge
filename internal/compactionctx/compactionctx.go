package compactionctx

import (
	"context"
)

type prefixMessagesKey struct{}

// WithPrefixTokens stores the token count of non-persisted prefix context for
// compaction accounting.
func WithPrefixTokens(ctx context.Context, tokens int) context.Context {
	if tokens <= 0 {
		return ctx
	}

	return context.WithValue(ctx, prefixMessagesKey{}, tokens)
}

// PrefixTokens returns the non-persisted prefix token count stored in the
// context, if any.
func PrefixTokens(ctx context.Context) int {
	if ctx == nil {
		return 0
	}

	prefix, ok := ctx.Value(prefixMessagesKey{}).(int)
	if !ok || prefix <= 0 {
		return 0
	}

	return prefix
}
