package compaction

import (
	"context"
	"sync"

	"github.com/DocumentDrivenDX/forge"
)

// state tracks compaction state across invocations.
type state struct {
	mu              sync.Mutex
	previousSummary string
	previousFileOps *FileOps
	lastUsage       forge.TokenUsage
}

// NewCompactor creates a Compactor function suitable for forge.Request.Compactor.
// It uses the provided config to determine when and how to compact.
func NewCompactor(cfg Config) func(ctx context.Context, messages []forge.Message, provider forge.Provider, toolCalls []forge.ToolCallLog) ([]forge.Message, error) {
	s := &state{}

	return func(ctx context.Context, messages []forge.Message, provider forge.Provider, toolCalls []forge.ToolCallLog) ([]forge.Message, error) {
		if !cfg.Enabled {
			return messages, nil
		}

		// Estimate current token count
		estimated := EstimateConversationTokens(messages)

		// Check if compaction is needed
		if !ShouldCompact(estimated, cfg.ContextWindow, cfg.EffectivePercent, cfg.ReserveTokens) {
			return messages, nil
		}

		// Re-compaction guard: skip if the last message is already a summary
		if len(messages) > 0 && IsCompactionSummary(messages[len(messages)-1]) {
			return messages, nil
		}

		s.mu.Lock()
		prevSummary := s.previousSummary
		prevOps := s.previousFileOps
		s.mu.Unlock()

		newMessages, result, err := CompactMessages(
			ctx, provider, messages, toolCalls, prevSummary, prevOps, cfg,
		)
		if err != nil {
			return messages, err
		}
		if result == nil {
			return messages, nil
		}

		s.mu.Lock()
		s.previousSummary = result.Summary
		s.previousFileOps = result.FileOps
		s.mu.Unlock()

		return newMessages, nil
	}
}
