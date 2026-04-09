package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockStreamingProvider implements StreamingProvider for testing.
type mockStreamingProvider struct {
	mockProvider // embed for Chat() fallback
	deltas       []StreamDelta
}

func (m *mockStreamingProvider) ChatStream(ctx context.Context, messages []Message, tools []ToolDef, opts Options) (<-chan StreamDelta, error) {
	ch := make(chan StreamDelta, len(m.deltas))
	for _, d := range m.deltas {
		ch <- d
	}
	close(ch)
	return ch, nil
}

func TestConsumeStream_TextStreaming(t *testing.T) {
	sp := &mockStreamingProvider{
		deltas: []StreamDelta{
			{Content: "Hello, ", Model: "test-model"},
			{Content: "world!"},
			{Usage: &TokenUsage{Input: 10, Output: 5, Total: 15}, FinishReason: "stop", Done: true},
		},
	}

	var events []Event
	cb := func(e Event) { events = append(events, e) }
	seq := 0

	resp, err := consumeStream(context.Background(), sp, nil, nil, Options{}, cb, "test", &seq)
	require.NoError(t, err)

	assert.Equal(t, "Hello, world!", resp.Content)
	assert.Equal(t, "test-model", resp.Model)
	assert.Equal(t, "stop", resp.FinishReason)
	assert.Equal(t, 15, resp.Usage.Total)
	assert.Empty(t, resp.ToolCalls)

	// Should have emitted 3 delta events
	assert.Len(t, events, 3)
	for _, e := range events {
		assert.Equal(t, EventLLMDelta, e.Type)
	}
}

func TestConsumeStream_ToolCallAssembly(t *testing.T) {
	sp := &mockStreamingProvider{
		deltas: []StreamDelta{
			{ToolCallID: "tc1", ToolCallName: "read"},
			{ToolCallID: "tc1", ToolCallArgs: `{"path":`},
			{ToolCallID: "tc1", ToolCallArgs: `"main.go"}`},
			{Usage: &TokenUsage{Input: 20, Output: 10, Total: 30}, FinishReason: "tool_calls", Done: true},
		},
	}

	seq := 0
	resp, err := consumeStream(context.Background(), sp, nil, nil, Options{}, nil, "test", &seq)
	require.NoError(t, err)

	require.Len(t, resp.ToolCalls, 1)
	assert.Equal(t, "tc1", resp.ToolCalls[0].ID)
	assert.Equal(t, "read", resp.ToolCalls[0].Name)

	var args map[string]string
	require.NoError(t, json.Unmarshal(resp.ToolCalls[0].Arguments, &args))
	assert.Equal(t, "main.go", args["path"])
}

func TestConsumeStream_MultipleToolCalls(t *testing.T) {
	sp := &mockStreamingProvider{
		deltas: []StreamDelta{
			{ToolCallID: "tc1", ToolCallName: "read", ToolCallArgs: `{"path":"a.go"}`},
			{ToolCallID: "tc2", ToolCallName: "read", ToolCallArgs: `{"path":"b.go"}`},
			{Done: true},
		},
	}

	seq := 0
	resp, err := consumeStream(context.Background(), sp, nil, nil, Options{}, nil, "test", &seq)
	require.NoError(t, err)
	require.Len(t, resp.ToolCalls, 2)
	assert.Equal(t, "tc1", resp.ToolCalls[0].ID)
	assert.Equal(t, "tc2", resp.ToolCalls[1].ID)
}

func TestConsumeStream_ContentAndToolCalls(t *testing.T) {
	sp := &mockStreamingProvider{
		deltas: []StreamDelta{
			{Content: "I'll read that. "},
			{ToolCallID: "tc1", ToolCallName: "read", ToolCallArgs: `{"path":"main.go"}`},
			{Done: true},
		},
	}

	seq := 0
	resp, err := consumeStream(context.Background(), sp, nil, nil, Options{}, nil, "test", &seq)
	require.NoError(t, err)
	assert.Equal(t, "I'll read that. ", resp.Content)
	require.Len(t, resp.ToolCalls, 1)
}

func TestRun_StreamingFallback(t *testing.T) {
	// Provider implements only Provider, not StreamingProvider
	provider := &mockProvider{
		responses: []Response{
			{Content: "non-streaming response"},
		},
	}

	result, err := Run(context.Background(), Request{
		Prompt:   "test",
		Provider: provider,
	})
	require.NoError(t, err)
	assert.Equal(t, StatusSuccess, result.Status)
	assert.Equal(t, "non-streaming response", result.Output)
}

func TestRun_NoStreamFlag(t *testing.T) {
	// Provider supports streaming but NoStream is set
	sp := &mockStreamingProvider{
		mockProvider: mockProvider{
			responses: []Response{
				{Content: "non-streaming forced"},
			},
		},
		deltas: []StreamDelta{
			{Content: "should not see this", Done: true},
		},
	}

	result, err := Run(context.Background(), Request{
		Prompt:   "test",
		Provider: sp,
		NoStream: true,
	})
	require.NoError(t, err)
	assert.Equal(t, "non-streaming forced", result.Output)
}

func TestRun_StreamingProvider(t *testing.T) {
	sp := &mockStreamingProvider{
		deltas: []StreamDelta{
			{Content: "streamed ", Model: "stream-model"},
			{Content: "response"},
			{Usage: &TokenUsage{Input: 10, Output: 5, Total: 15}, Done: true},
		},
	}

	var events []Event
	result, err := Run(context.Background(), Request{
		Prompt:   "test",
		Provider: sp,
		Callback: func(e Event) { events = append(events, e) },
	})
	require.NoError(t, err)
	assert.Equal(t, StatusSuccess, result.Status)
	assert.Equal(t, "streamed response", result.Output)
	assert.Equal(t, "stream-model", result.Model)

	// Should have delta events
	deltaCount := 0
	for _, e := range events {
		if e.Type == EventLLMDelta {
			deltaCount++
		}
	}
	assert.Equal(t, 3, deltaCount)
}

func TestRun_StreamingProviderPreservesAttemptMetadata(t *testing.T) {
	sp := &mockStreamingProvider{
		deltas: []StreamDelta{
			{Content: "streamed ", Model: "gpt-4o"},
			{
				Content: "response",
				Model:   "gpt-4o",
				Attempt: &AttemptMetadata{
					ProviderName:   "openai",
					ProviderSystem: "openai",
					RequestedModel: "gpt-4o",
					ResponseModel:  "gpt-4o",
					ResolvedModel:  "gpt-4o",
					Cost: &CostAttribution{
						Source: CostSourceUnknown,
					},
				},
				Done: true,
			},
		},
	}

	var responseEvent Event
	result, err := Run(context.Background(), Request{
		Prompt:   "test",
		Provider: sp,
		Callback: func(e Event) {
			if e.Type == EventLLMResponse {
				responseEvent = e
			}
		},
	})
	require.NoError(t, err)
	assert.Equal(t, StatusSuccess, result.Status)

	attempt := findResponseAttempt(t, responseEvent.Data)
	assert.Equal(t, "openai", attempt["provider_name"])
	assert.Equal(t, "openai", attempt["provider_system"])
	assert.Equal(t, "gpt-4o", attempt["requested_model"])
	assert.Equal(t, "gpt-4o", attempt["response_model"])
	assert.Equal(t, "gpt-4o", attempt["resolved_model"])

	cost, ok := attempt["cost"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "unknown", cost["source"])
}

func TestConsumeStream_StreamError(t *testing.T) {
	netErr := errors.New("connection reset by peer")
	sp := &mockStreamingProvider{
		deltas: []StreamDelta{
			{Content: "partial"},
			{Err: netErr},
		},
	}

	seq := 0
	_, err := consumeStream(context.Background(), sp, nil, nil, Options{}, nil, "test", &seq)
	require.ErrorIs(t, err, netErr)
}

func TestConsumeStream_StreamErrorAfterToolCall(t *testing.T) {
	netErr := errors.New("network timeout")
	sp := &mockStreamingProvider{
		deltas: []StreamDelta{
			{ToolCallID: "tc1", ToolCallName: "read"},
			{ToolCallID: "tc1", ToolCallArgs: `{"path":`},
			{Err: netErr},
		},
	}

	seq := 0
	_, err := consumeStream(context.Background(), sp, nil, nil, Options{}, nil, "test", &seq)
	require.ErrorIs(t, err, netErr)
}
