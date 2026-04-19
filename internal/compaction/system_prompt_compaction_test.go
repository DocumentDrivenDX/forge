package compaction

import (
	"context"
	"strings"
	"testing"

	"github.com/DocumentDrivenDX/agent"
	"github.com/DocumentDrivenDX/agent/internal/compactionctx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type recordingProvider struct {
	responses []agent.Response
	callCount int
	calls     [][]agent.Message
}

func (r *recordingProvider) Chat(ctx context.Context, messages []agent.Message, tools []agent.ToolDef, opts agent.Options) (agent.Response, error) {
	if ctx.Err() != nil {
		return agent.Response{}, ctx.Err()
	}

	copied := append([]agent.Message(nil), messages...)
	r.calls = append(r.calls, copied)

	if r.callCount >= len(r.responses) {
		return agent.Response{Content: "no more responses"}, nil
	}

	resp := r.responses[r.callCount]
	r.callCount++
	return resp, nil
}

type summarizationAwareRecordingProvider struct {
	finalResponse string
	calls         [][]agent.Message
}

func (p *summarizationAwareRecordingProvider) Chat(ctx context.Context, messages []agent.Message, tools []agent.ToolDef, opts agent.Options) (agent.Response, error) {
	if ctx.Err() != nil {
		return agent.Response{}, ctx.Err()
	}

	copied := append([]agent.Message(nil), messages...)
	p.calls = append(p.calls, copied)

	if len(messages) >= 2 &&
		messages[0].Role == agent.RoleSystem &&
		strings.Contains(messages[0].Content, "context summarization assistant") {
		return agent.Response{Content: "## Goal\nSummarized context"}, nil
	}

	return agent.Response{Content: p.finalResponse}, nil
}

func TestRun_SystemPromptCountsTowardCompactionBudget(t *testing.T) {
	provider := &summarizationAwareRecordingProvider{finalResponse: "final answer"}

	cfg := DefaultConfig()
	cfg.ContextWindow = 100
	cfg.ReserveTokens = 0
	cfg.KeepRecentTokens = 5
	cfg.EffectivePercent = 100

	systemPrompt := strings.Repeat("S", 120)

	result, err := agent.Run(context.Background(), agent.Request{
		History: []agent.Message{
			{Role: agent.RoleUser, Content: strings.Repeat("A", 160)},
			{Role: agent.RoleAssistant, Content: strings.Repeat("B", 100)},
			{Role: agent.RoleUser, Content: strings.Repeat("C", 100)},
		},
		Prompt:       strings.Repeat("P", 20),
		SystemPrompt: systemPrompt,
		Provider:     provider,
		Compactor:    NewCompactor(cfg),
	})
	require.NoError(t, err)
	assert.Equal(t, agent.StatusSuccess, result.Status)
	assert.Equal(t, "final answer", result.Output)
	require.Len(t, provider.calls, 2)

	require.NotEmpty(t, provider.calls[len(provider.calls)-1])
	assert.Equal(t, agent.RoleSystem, provider.calls[len(provider.calls)-1][0].Role)
	assert.Equal(t, systemPrompt, provider.calls[len(provider.calls)-1][0].Content)

	systemCount := 0
	for _, msg := range result.Messages {
		if msg.Role == agent.RoleSystem {
			systemCount++
		}
	}
	assert.Zero(t, systemCount, "persisted history must not duplicate the system prompt")

	summarySeen := false
	for _, msg := range result.Messages {
		if IsCompactionSummary(msg) {
			summarySeen = true
			break
		}
	}
	assert.True(t, summarySeen, "compaction should have inserted a summary message")
}

func TestRun_SystemPromptDoesNotConsumeKeepBudgetForActivePrompt(t *testing.T) {
	provider := &summarizationAwareRecordingProvider{finalResponse: "final answer"}

	cfg := DefaultConfig()
	cfg.ContextWindow = 100
	cfg.ReserveTokens = 0
	cfg.KeepRecentTokens = 5
	cfg.EffectivePercent = 100

	systemPrompt := strings.Repeat("S", 80)
	activePrompt := "DO-THE-THING"

	result, err := agent.Run(context.Background(), agent.Request{
		History: []agent.Message{
			{Role: agent.RoleUser, Content: strings.Repeat("A", 120)},
			{Role: agent.RoleAssistant, Content: strings.Repeat("B", 100)},
			{Role: agent.RoleUser, Content: strings.Repeat("C", 100)},
		},
		Prompt:       activePrompt,
		SystemPrompt: systemPrompt,
		Provider:     provider,
		Compactor:    NewCompactor(cfg),
	})
	require.NoError(t, err)
	assert.Equal(t, agent.StatusSuccess, result.Status)
	assert.Equal(t, "final answer", result.Output)
	require.GreaterOrEqual(t, len(provider.calls), 2)

	foundActivePrompt := false
	for _, msg := range provider.calls[len(provider.calls)-1] {
		if msg.Role == agent.RoleUser && msg.Content == activePrompt {
			foundActivePrompt = true
			break
		}
	}
	assert.True(t, foundActivePrompt, "compaction must keep the active user prompt verbatim")
}

func TestCompactor_SystemPromptPrefixFitKeepsActivePromptAndBudget(t *testing.T) {
	provider := &mockSummarizer{response: strings.Repeat("S", 100)}

	cfg := DefaultConfig()
	cfg.ContextWindow = 80
	cfg.ReserveTokens = 0
	cfg.KeepRecentTokens = 20
	cfg.EffectivePercent = 100

	systemPrompt := strings.Repeat("P", 120)
	prefixTokens := EstimateMessageTokens(agent.Message{Role: agent.RoleSystem, Content: systemPrompt})
	ctx := compactionctx.WithPrefixTokens(context.Background(), prefixTokens)

	var messages []agent.Message
	for i := 0; i < 5; i++ {
		messages = append(messages, agent.Message{Role: agent.RoleUser, Content: strings.Repeat("A", 120)})
		messages = append(messages, agent.Message{Role: agent.RoleAssistant, Content: strings.Repeat("B", 120)})
	}
	messages = append(messages, agent.Message{Role: agent.RoleUser, Content: "DO-THE-THING"})

	newMsgs, result, err := NewCompactor(cfg)(ctx, messages, provider, nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.LessOrEqual(t, result.TokensAfter, cfg.ContextWindow*cfg.EffectivePercent/100-cfg.ReserveTokens)

	foundActivePrompt := false
	for _, msg := range newMsgs {
		if msg.Role == agent.RoleUser && msg.Content == "DO-THE-THING" {
			foundActivePrompt = true
			break
		}
	}
	assert.True(t, foundActivePrompt, "compaction must keep the active user prompt verbatim")
	assert.True(t, IsCompactionSummary(newMsgs[len(newMsgs)-1]))
	assert.LessOrEqual(t, result.TokensAfter, 80)
}

func TestCompactor_SystemPromptPrefixNoFitFailsClosed(t *testing.T) {
	provider := &mockSummarizer{response: "x"}

	cfg := DefaultConfig()
	cfg.ContextWindow = 80
	cfg.ReserveTokens = 0
	cfg.KeepRecentTokens = 20
	cfg.EffectivePercent = 100

	systemPrompt := strings.Repeat("P", 260)
	prefixTokens := EstimateMessageTokens(agent.Message{Role: agent.RoleSystem, Content: systemPrompt})
	ctx := compactionctx.WithPrefixTokens(context.Background(), prefixTokens)

	messages := []agent.Message{
		{Role: agent.RoleUser, Content: strings.Repeat("A", 120)},
		{Role: agent.RoleAssistant, Content: strings.Repeat("B", 120)},
		{Role: agent.RoleUser, Content: "DO-THE-THING"},
	}

	newMsgs, result, err := NewCompactor(cfg)(ctx, messages, provider, nil)
	require.ErrorIs(t, err, agent.ErrCompactionNoFit)
	assert.Nil(t, result, "compaction should fail closed when the prefix leaves no fit")
	assert.Equal(t, messages, newMsgs)
}
