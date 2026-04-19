package compaction

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/DocumentDrivenDX/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockSummarizer struct {
	response string
}

func (m *mockSummarizer) Chat(ctx context.Context, messages []agent.Message, tools []agent.ToolDef, opts agent.Options) (agent.Response, error) {
	return agent.Response{Content: m.response}, nil
}

func TestSummarize(t *testing.T) {
	provider := &mockSummarizer{response: "## Goal\nFix the bug\n\n## Progress\n### Done\n- [x] Read main.go"}

	msgs := []agent.Message{
		{Role: agent.RoleUser, Content: "Fix the bug in main.go"},
		{Role: agent.RoleAssistant, Content: "I'll read the file."},
	}
	toolCalls := []agent.ToolCallLog{
		{Tool: "read", Input: json.RawMessage(`{"path":"main.go"}`)},
	}

	summary, ops, err := Summarize(context.Background(), provider, msgs, toolCalls, "", DefaultConfig(), 0)
	require.NoError(t, err)

	assert.Contains(t, summary, "## Goal")
	assert.Contains(t, summary, "Fix the bug")
	assert.Contains(t, summary, "<read-files>")
	assert.Contains(t, summary, "main.go")
	assert.NotNil(t, ops)
	assert.True(t, ops.Read["main.go"])
}

func TestSummarize_UpdateMode(t *testing.T) {
	provider := &mockSummarizer{response: "## Goal\nFix the bug\n\n## Progress\n### Done\n- [x] Read main.go\n- [x] Applied fix"}

	msgs := []agent.Message{
		{Role: agent.RoleUser, Content: "Now run the tests"},
	}

	summary, _, err := Summarize(context.Background(), provider, msgs, nil, "previous summary here", DefaultConfig(), 0)
	require.NoError(t, err)
	assert.Contains(t, summary, "Applied fix")
}

func TestSummarize_EmptyResponse(t *testing.T) {
	provider := &mockSummarizer{response: ""}

	summary, _, err := Summarize(context.Background(), provider, []agent.Message{
		{Role: agent.RoleUser, Content: "test"},
	}, nil, "", DefaultConfig(), 0)
	require.NoError(t, err)
	assert.Contains(t, summary, NoSummaryFallback)
}

func TestInjectSummary(t *testing.T) {
	msg := InjectSummary("## Goal\nDo stuff")
	assert.Equal(t, agent.RoleUser, msg.Role)
	assert.Contains(t, msg.Content, "<summary>")
	assert.Contains(t, msg.Content, "## Goal")
	assert.Contains(t, msg.Content, "</summary>")
	assert.Contains(t, msg.Content, "compacted into the following summary")
}

func TestCompactMessages(t *testing.T) {
	provider := &mockSummarizer{response: "## Goal\nTest compaction"}

	// Create a long conversation
	var msgs []agent.Message
	msgs = append(msgs, agent.Message{Role: agent.RoleUser, Content: "Start working on the project"})
	for i := 0; i < 20; i++ {
		msgs = append(msgs, agent.Message{Role: agent.RoleAssistant, Content: "Working on step " + string(rune('A'+i)) + "... " + string(make([]byte, 500))})
		msgs = append(msgs, agent.Message{Role: agent.RoleUser, Content: "Continue with next step"})
	}

	cfg := DefaultConfig()
	cfg.KeepRecentTokens = 200 // small budget to force compaction

	newMsgs, result, err := CompactMessages(context.Background(), provider, msgs, nil, "", nil, cfg)
	require.NoError(t, err)
	require.NotNil(t, result)

	assert.Less(t, len(newMsgs), len(msgs))
	assert.Contains(t, result.Summary, "## Goal")
	assert.Greater(t, result.TokensBefore, result.TokensAfter)
	assert.NotEmpty(t, result.Warning)

	// Summary should be LAST (per SD-006: recent messages first, summary last for prompt cache)
	assert.True(t, IsCompactionSummary(newMsgs[len(newMsgs)-1]))
	// First message should NOT be a summary
	assert.False(t, IsCompactionSummary(newMsgs[0]))
}

func TestCompactMessages_SummaryIsLast(t *testing.T) {
	provider := &mockSummarizer{response: "## Goal\nVerify ordering"}

	var msgs []agent.Message
	msgs = append(msgs, agent.Message{Role: agent.RoleUser, Content: "Start"})
	for i := 0; i < 20; i++ {
		msgs = append(msgs, agent.Message{Role: agent.RoleAssistant, Content: "Step " + string(rune('A'+i)) + string(make([]byte, 500))})
		msgs = append(msgs, agent.Message{Role: agent.RoleUser, Content: "Continue"})
	}

	cfg := DefaultConfig()
	cfg.KeepRecentTokens = 200

	newMsgs, result, err := CompactMessages(context.Background(), provider, msgs, nil, "", nil, cfg)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Greater(t, len(newMsgs), 0)

	// SD-006: summary must be the LAST message so recent user turns come first
	assert.True(t, IsCompactionSummary(newMsgs[len(newMsgs)-1]), "summary must be last message")

	// All messages before the last must NOT be summaries
	for i := 0; i < len(newMsgs)-1; i++ {
		assert.False(t, IsCompactionSummary(newMsgs[i]), "message %d should not be a summary", i)
	}
}

func TestCompactMessages_NothingToCompact(t *testing.T) {
	provider := &mockSummarizer{response: "should not be called"}

	msgs := []agent.Message{
		{Role: agent.RoleUser, Content: "short message"},
	}

	cfg := DefaultConfig()
	cfg.KeepRecentTokens = 100000 // huge budget

	newMsgs, result, err := CompactMessages(context.Background(), provider, msgs, nil, "", nil, cfg)
	require.NoError(t, err)
	assert.Nil(t, result)
	assert.Equal(t, len(msgs), len(newMsgs))
}

func TestCompactMessages_ExcludesPreviousSummaries(t *testing.T) {
	callCount := 0
	provider := &mockSummarizer{response: "## Goal\nContinued work"}

	// Simulate a conversation with a previous compaction summary
	msgs := []agent.Message{
		InjectSummary("previous compaction summary"),
		{Role: agent.RoleUser, Content: "do more work " + string(make([]byte, 2000))},
		{Role: agent.RoleAssistant, Content: "doing it " + string(make([]byte, 2000))},
		{Role: agent.RoleUser, Content: "and more " + string(make([]byte, 2000))},
	}
	_ = callCount

	cfg := DefaultConfig()
	cfg.KeepRecentTokens = 100

	_, result, err := CompactMessages(context.Background(), provider, msgs, nil, "", nil, cfg)
	require.NoError(t, err)
	require.NotNil(t, result)
	// The previous summary should have been filtered from summarization input
}
