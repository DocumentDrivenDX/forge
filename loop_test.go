package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockProvider is a test provider that returns pre-configured responses.
type mockProvider struct {
	responses []Response
	callCount int
}

func (m *mockProvider) Chat(ctx context.Context, messages []Message, tools []ToolDef, opts Options) (Response, error) {
	if ctx.Err() != nil {
		return Response{}, ctx.Err()
	}
	if m.callCount >= len(m.responses) {
		return Response{Content: "no more responses"}, nil
	}
	resp := m.responses[m.callCount]
	m.callCount++
	return resp, nil
}

// mockTool is a test tool that returns a fixed result.
type mockTool struct {
	name   string
	result string
	err    error
}

func (t *mockTool) Name() string            { return t.name }
func (t *mockTool) Description() string     { return "mock tool" }
func (t *mockTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (t *mockTool) Execute(ctx context.Context, params json.RawMessage) (string, error) {
	return t.result, t.err
}

func TestRun_SimpleTextResponse(t *testing.T) {
	provider := &mockProvider{
		responses: []Response{
			{Content: "Hello, world!", Usage: TokenUsage{Input: 10, Output: 5, Total: 15}},
		},
	}

	result, err := Run(context.Background(), Request{
		Prompt:   "Say hello",
		Provider: provider,
	})
	require.NoError(t, err)
	assert.Equal(t, StatusSuccess, result.Status)
	assert.Equal(t, "Hello, world!", result.Output)
	assert.Equal(t, 10, result.Tokens.Input)
	assert.Equal(t, 5, result.Tokens.Output)
	assert.Empty(t, result.ToolCalls)
}

func TestRun_ToolCallThenResponse(t *testing.T) {
	provider := &mockProvider{
		responses: []Response{
			{
				ToolCalls: []ToolCall{
					{ID: "tc1", Name: "read", Arguments: json.RawMessage(`{"path":"main.go"}`)},
				},
				Usage: TokenUsage{Input: 20, Output: 10, Total: 30},
			},
			{
				Content: "The package is main.",
				Usage:   TokenUsage{Input: 50, Output: 15, Total: 65},
			},
		},
	}

	readTool := &mockTool{name: "read", result: "package main\n"}

	result, err := Run(context.Background(), Request{
		Prompt:   "Read main.go and tell me the package",
		Provider: provider,
		Tools:    []Tool{readTool},
	})
	require.NoError(t, err)
	assert.Equal(t, StatusSuccess, result.Status)
	assert.Equal(t, "The package is main.", result.Output)
	assert.Equal(t, 70, result.Tokens.Input)
	assert.Equal(t, 25, result.Tokens.Output)
	require.Len(t, result.ToolCalls, 1)
	assert.Equal(t, "read", result.ToolCalls[0].Tool)
	assert.Equal(t, "package main\n", result.ToolCalls[0].Output)
}

func TestRun_IterationLimit(t *testing.T) {
	// Provider always returns tool calls — loop should stop at limit
	provider := &mockProvider{
		responses: []Response{
			{ToolCalls: []ToolCall{{ID: "tc1", Name: "read", Arguments: json.RawMessage(`{}`)}}, Usage: TokenUsage{Total: 10}},
			{ToolCalls: []ToolCall{{ID: "tc2", Name: "read", Arguments: json.RawMessage(`{}`)}}, Usage: TokenUsage{Total: 10}},
			{ToolCalls: []ToolCall{{ID: "tc3", Name: "read", Arguments: json.RawMessage(`{}`)}}, Usage: TokenUsage{Total: 10}},
		},
	}

	readTool := &mockTool{name: "read", result: "content"}

	result, err := Run(context.Background(), Request{
		Prompt:        "loop forever",
		Provider:      provider,
		Tools:         []Tool{readTool},
		MaxIterations: 2,
	})
	require.NoError(t, err)
	assert.Equal(t, StatusIterationLimit, result.Status)
}

func TestRun_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	provider := &mockProvider{
		responses: []Response{
			{Content: "should not reach this"},
		},
	}

	result, err := Run(ctx, Request{
		Prompt:   "test",
		Provider: provider,
	})
	require.NoError(t, err)
	assert.Equal(t, StatusCancelled, result.Status)
}

func TestRun_UnknownToolCall(t *testing.T) {
	provider := &mockProvider{
		responses: []Response{
			{
				ToolCalls: []ToolCall{
					{ID: "tc1", Name: "nonexistent", Arguments: json.RawMessage(`{}`)},
				},
				Usage: TokenUsage{Total: 10},
			},
			{Content: "I see, that tool doesn't exist."},
		},
	}

	result, err := Run(context.Background(), Request{
		Prompt:   "test",
		Provider: provider,
	})
	require.NoError(t, err)
	assert.Equal(t, StatusSuccess, result.Status)
	require.Len(t, result.ToolCalls, 1)
	assert.Contains(t, result.ToolCalls[0].Error, "unknown tool")
}

func TestRun_NilProvider(t *testing.T) {
	_, err := Run(context.Background(), Request{
		Prompt: "test",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "provider is required")
}

func TestRun_EmptyResponse(t *testing.T) {
	provider := &mockProvider{
		responses: []Response{
			{Content: "", Usage: TokenUsage{Total: 5}},
		},
	}

	result, err := Run(context.Background(), Request{
		Prompt:   "test",
		Provider: provider,
	})
	require.NoError(t, err)
	assert.Equal(t, StatusSuccess, result.Status)
	assert.Equal(t, "", result.Output)
}

func TestRun_EventCallback(t *testing.T) {
	provider := &mockProvider{
		responses: []Response{
			{Content: "done", Usage: TokenUsage{Total: 10}},
		},
	}

	var events []Event
	cb := func(e Event) {
		events = append(events, e)
	}

	result, err := Run(context.Background(), Request{
		Prompt:   "test",
		Provider: provider,
		Callback: cb,
	})
	require.NoError(t, err)
	assert.Equal(t, StatusSuccess, result.Status)

	// Should have: session.start, llm.request, llm.response, session.end
	require.Len(t, events, 4)
	assert.Equal(t, EventSessionStart, events[0].Type)
	assert.Equal(t, EventLLMRequest, events[1].Type)
	assert.Equal(t, EventLLMResponse, events[2].Type)
	assert.Equal(t, EventSessionEnd, events[3].Type)
}

func TestRun_MultipleToolCalls(t *testing.T) {
	provider := &mockProvider{
		responses: []Response{
			{
				ToolCalls: []ToolCall{
					{ID: "tc1", Name: "read", Arguments: json.RawMessage(`{"path":"a.go"}`)},
					{ID: "tc2", Name: "read", Arguments: json.RawMessage(`{"path":"b.go"}`)},
				},
				Usage: TokenUsage{Total: 20},
			},
			{Content: "Both files read.", Usage: TokenUsage{Total: 30}},
		},
	}

	readTool := &mockTool{name: "read", result: "content"}

	result, err := Run(context.Background(), Request{
		Prompt:   "read both files",
		Provider: provider,
		Tools:    []Tool{readTool},
	})
	require.NoError(t, err)
	assert.Equal(t, StatusSuccess, result.Status)
	require.Len(t, result.ToolCalls, 2)
}

func TestRun_CostAccumulation(t *testing.T) {
	// gpt-4o is in DefaultPricing: $2.50/MTok input, $10.00/MTok output
	// iteration 1: 1000 input, 500 output -> $0.0025 + $0.005 = $0.0075
	// iteration 2: 2000 input, 1000 output -> $0.005 + $0.010 = $0.015
	// total: $0.0225
	provider := &mockProvider{
		responses: []Response{
			{
				ToolCalls: []ToolCall{
					{ID: "tc1", Name: "read", Arguments: json.RawMessage(`{}`)},
				},
				Usage: TokenUsage{Input: 1000, Output: 500, Total: 1500},
				Model: "gpt-4o",
			},
			{
				Content: "done",
				Usage:   TokenUsage{Input: 2000, Output: 1000, Total: 3000},
				Model:   "gpt-4o",
			},
		},
	}

	readTool := &mockTool{name: "read", result: "content"}

	result, err := Run(context.Background(), Request{
		Prompt:   "test cost",
		Provider: provider,
		Tools:    []Tool{readTool},
	})
	require.NoError(t, err)
	assert.Equal(t, StatusSuccess, result.Status)

	expected := DefaultPricing.EstimateCost("gpt-4o", 1000, 500) +
		DefaultPricing.EstimateCost("gpt-4o", 2000, 1000)
	assert.InDelta(t, expected, result.CostUSD, 1e-9)
	assert.Greater(t, result.CostUSD, 0.0)
}

func TestRun_CostUnknownModel(t *testing.T) {
	// Unknown model should result in CostUSD == -1
	provider := &mockProvider{
		responses: []Response{
			{Content: "done", Usage: TokenUsage{Input: 100, Output: 50, Total: 150}, Model: "unknown-model-xyz"},
		},
	}

	result, err := Run(context.Background(), Request{
		Prompt:   "test",
		Provider: provider,
	})
	require.NoError(t, err)
	assert.Equal(t, StatusSuccess, result.Status)
	assert.Equal(t, -1.0, result.CostUSD)
}

func TestRun_SessionEndEventIncludesCost(t *testing.T) {
	provider := &mockProvider{
		responses: []Response{
			{Content: "done", Usage: TokenUsage{Input: 1000, Output: 500, Total: 1500}, Model: "gpt-4o"},
		},
	}

	var sessionEndData map[string]any
	cb := func(e Event) {
		if e.Type == EventSessionEnd {
			_ = json.Unmarshal(e.Data, &sessionEndData)
		}
	}

	result, err := Run(context.Background(), Request{
		Prompt:   "test",
		Provider: provider,
		Callback: cb,
	})
	require.NoError(t, err)
	assert.Greater(t, result.CostUSD, 0.0)
	require.NotNil(t, sessionEndData)
	costVal, ok := sessionEndData["cost_usd"]
	require.True(t, ok, "session.end event must include cost_usd")
	assert.Greater(t, costVal.(float64), 0.0)
}
