package compaction

import (
	"context"
	"errors"
	"sync"

	"github.com/DocumentDrivenDX/agent"
	"github.com/DocumentDrivenDX/agent/internal/compactionctx"
)

// state tracks compaction state across invocations.
type state struct {
	mu                        sync.Mutex
	previousSummary           string
	previousFileOps           *FileOps
	consecutiveFailedAttempts int
}

// NewCompactor creates a Compactor function suitable for agent.Request.Compactor.
// It uses the provided config to determine when and how to compact.
func NewCompactor(cfg Config) func(ctx context.Context, messages []agent.Message, provider agent.Provider, toolCalls []agent.ToolCallLog) ([]agent.Message, *agent.CompactionResult, error) {
	s := &state{}

	stuckThreshold := cfg.StuckThreshold
	if stuckThreshold <= 0 {
		stuckThreshold = DefaultStuckThreshold
	}

	return func(ctx context.Context, messages []agent.Message, provider agent.Provider, toolCalls []agent.ToolCallLog) ([]agent.Message, *agent.CompactionResult, error) {
		if !cfg.Enabled {
			return messages, nil, nil
		}

		prefixTokens := compactionctx.PrefixTokens(ctx)

		// Estimate current token count
		estimated := EstimateConversationTokens(messages) + prefixTokens

		// Check if compaction is needed
		if !ShouldCompact(estimated, cfg.ContextWindow, cfg.EffectivePercent, cfg.ReserveTokens) {
			// Below threshold — reset the stuck counter since conditions changed.
			s.mu.Lock()
			s.consecutiveFailedAttempts = 0
			s.mu.Unlock()
			return messages, nil, nil
		}

		// Re-compaction guard: skip if the last message is already a summary
		if len(messages) > 0 && IsCompactionSummary(messages[len(messages)-1]) {
			return messages, nil, nil
		}

		s.mu.Lock()
		prevSummary := s.previousSummary
		prevOps := s.previousFileOps
		s.mu.Unlock()

		newMessages, result, err := compactMessages(
			ctx, provider, messages, toolCalls, prevSummary, prevOps, cfg, prefixTokens,
		)
		if err != nil {
			// compactMessages failed — count this as a failed attempt unless
			// it is already a fatal error (ErrCompactionNoFit).
			if !errors.Is(err, agent.ErrCompactionNoFit) {
				s.mu.Lock()
				s.consecutiveFailedAttempts++
				if s.consecutiveFailedAttempts >= stuckThreshold {
					s.mu.Unlock()
					return messages, nil, agent.ErrCompactionStuck
				}
				s.mu.Unlock()
			}
			return messages, nil, err
		}
		if result == nil {
			// ShouldCompact said yes but compactMessages produced nothing.
			s.mu.Lock()
			s.consecutiveFailedAttempts++
			if s.consecutiveFailedAttempts >= stuckThreshold {
				s.mu.Unlock()
				return messages, nil, agent.ErrCompactionStuck
			}
			s.mu.Unlock()
			return messages, nil, nil
		}

		if prefixTokens > 0 {
			result.TokensBefore += prefixTokens
			result.TokensAfter += prefixTokens
		}

		s.mu.Lock()
		s.previousSummary = result.Summary
		s.previousFileOps = result.FileOps
		s.consecutiveFailedAttempts = 0
		s.mu.Unlock()

		// Convert to agent.CompactionResult
		agentResult := &agent.CompactionResult{
			Summary:      result.Summary,
			TokensBefore: result.TokensBefore,
			TokensAfter:  result.TokensAfter,
			Warning:      result.Warning,
		}
		if result.FileOps != nil {
			agentResult.FileOps = map[string]any{
				"read":     result.FileOps.Read,
				"modified": result.FileOps.Modified,
			}
		}

		return newMessages, agentResult, nil
	}
}
