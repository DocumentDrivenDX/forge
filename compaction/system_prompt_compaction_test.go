package compaction

import (
	"context"
	"strings"
	"testing"

	"github.com/DocumentDrivenDX/agent"
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

func TestRun_SystemPromptCountsTowardCompactionBudget(t *testing.T) {
	provider := &recordingProvider{
		responses: []agent.Response{
			{Content: "## Goal\nSummarized context"},
			{Content: "final answer"},
		},
	}

	cfg := DefaultConfig()
	cfg.ContextWindow = 130
	cfg.ReserveTokens = 0
	cfg.KeepRecentTokens = 70
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

	require.NotEmpty(t, provider.calls[1])
	assert.Equal(t, agent.RoleSystem, provider.calls[1][0].Role)
	assert.Equal(t, systemPrompt, provider.calls[1][0].Content)

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
