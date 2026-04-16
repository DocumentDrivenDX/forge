package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockStreamingProvider implements StreamingProvider for testing.
type mockStreamingProvider struct {
	mockProvider // embed for Chat() fallback
	deltas       []StreamDelta
	delayFirst   time.Duration
	delayBetween time.Duration
	setupDelay   time.Duration
	bufferSize   int
}

func (m *mockStreamingProvider) ChatStream(ctx context.Context, messages []Message, tools []ToolDef, opts Options) (<-chan StreamDelta, error) {
	if m.setupDelay > 0 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(m.setupDelay):
		}
	}
	capacity := m.bufferSize
	if capacity <= 0 {
		capacity = len(m.deltas)
	}
	if capacity <= 0 {
		capacity = 1
	}
	ch := make(chan StreamDelta, capacity)
	go func() {
		defer close(ch)
		for i, d := range m.deltas {
			if i == 0 && m.delayFirst > 0 {
				select {
				case <-ctx.Done():
					return
				case <-time.After(m.delayFirst):
				}
			} else if i > 0 && m.delayBetween > 0 {
				select {
				case <-ctx.Done():
					return
				case <-time.After(m.delayBetween):
				}
			}
			d.ArrivedAt = time.Now()
			select {
			case <-ctx.Done():
				return
			case ch <- d:
			}
		}
	}()
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
	start := time.Now()

	resp, err := consumeStream(context.Background(), sp, nil, nil, Options{}, cb, "test", start, &seq, streamThresholds{})
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
	start := time.Now()
	resp, err := consumeStream(context.Background(), sp, nil, nil, Options{}, nil, "test", start, &seq, streamThresholds{})
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
	start := time.Now()
	resp, err := consumeStream(context.Background(), sp, nil, nil, Options{}, nil, "test", start, &seq, streamThresholds{})
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
	start := time.Now()
	resp, err := consumeStream(context.Background(), sp, nil, nil, Options{}, nil, "test", start, &seq, streamThresholds{})
	require.NoError(t, err)
	assert.Equal(t, "I'll read that. ", resp.Content)
	require.Len(t, resp.ToolCalls, 1)
}

func TestConsumeStream_CapturesTimingWhenStreamingOutputArrives(t *testing.T) {
	sp := &mockStreamingProvider{
		delayFirst:   15 * time.Millisecond,
		delayBetween: 20 * time.Millisecond,
		deltas: []StreamDelta{
			{
				Content: "hello ",
				Attempt: &AttemptMetadata{
					ProviderName:   "openai",
					ProviderSystem: "openai",
					RequestedModel: "gpt-4o",
					ResponseModel:  "gpt-4o",
					ResolvedModel:  "gpt-4o",
				},
			},
			{Content: "world", Done: true},
		},
	}

	seq := 0
	start := time.Now()
	resp, err := consumeStream(context.Background(), sp, nil, nil, Options{}, nil, "test", start, &seq, streamThresholds{})
	require.NoError(t, err)
	require.NotNil(t, resp.Attempt)
	require.NotNil(t, resp.Attempt.Timing)
	require.NotNil(t, resp.Attempt.Timing.FirstToken)
	require.NotNil(t, resp.Attempt.Timing.Generation)
	assert.GreaterOrEqual(t, resp.Attempt.Timing.FirstToken.Milliseconds(), int64(15))
	assert.GreaterOrEqual(t, resp.Attempt.Timing.Generation.Milliseconds(), int64(20))
}

func TestConsumeStream_CapturesTimingFromChatStreamSetup(t *testing.T) {
	sp := &mockStreamingProvider{
		setupDelay: 20 * time.Millisecond,
		deltas: []StreamDelta{
			{
				Content: "hello",
				Attempt: &AttemptMetadata{
					ProviderName:   "openai",
					ProviderSystem: "openai",
					RequestedModel: "gpt-4o",
					ResponseModel:  "gpt-4o",
					ResolvedModel:  "gpt-4o",
				},
			},
			{Done: true},
		},
	}

	seq := 0
	start := time.Now()
	resp, err := consumeStream(context.Background(), sp, nil, nil, Options{}, nil, "test", start, &seq, streamThresholds{})
	require.NoError(t, err)
	require.NotNil(t, resp.Attempt)
	require.NotNil(t, resp.Attempt.Timing)
	require.NotNil(t, resp.Attempt.Timing.FirstToken)
	assert.GreaterOrEqual(t, resp.Attempt.Timing.FirstToken.Milliseconds(), int64(18))
}

func TestConsumeStream_IgnoresCallbackLatencyForTiming(t *testing.T) {
	sp := &mockStreamingProvider{
		bufferSize:   1,
		delayBetween: 25 * time.Millisecond,
		deltas: []StreamDelta{
			{
				Content: "hello ",
				Attempt: &AttemptMetadata{
					ProviderName:   "openai",
					ProviderSystem: "openai",
					RequestedModel: "gpt-4o",
					ResponseModel:  "gpt-4o",
					ResolvedModel:  "gpt-4o",
				},
			},
			{Content: "world", Done: true},
		},
	}

	var events []Event
	cb := func(e Event) {
		if e.Type == EventLLMDelta {
			events = append(events, e)
		}
		if len(events) == 1 {
			time.Sleep(80 * time.Millisecond)
		}
	}

	seq := 0
	start := time.Now()
	resp, err := consumeStream(context.Background(), sp, nil, nil, Options{}, cb, "test", start, &seq, streamThresholds{})
	require.NoError(t, err)
	require.NotNil(t, resp.Attempt)
	require.NotNil(t, resp.Attempt.Timing)
	require.NotNil(t, resp.Attempt.Timing.FirstToken)
	require.NotNil(t, resp.Attempt.Timing.Generation)
	assert.Less(t, *resp.Attempt.Timing.FirstToken, 40*time.Millisecond)
	assert.GreaterOrEqual(t, *resp.Attempt.Timing.Generation, 20*time.Millisecond)
	assert.Len(t, events, 2)
	assert.True(t, events[0].Timestamp.Before(events[1].Timestamp))
}

func TestConsumeStream_OmitsTimingWhenNoOutputBearingDeltaArrives(t *testing.T) {
	sp := &mockStreamingProvider{
		deltas: []StreamDelta{
			{
				Model: "gpt-4o",
				Attempt: &AttemptMetadata{
					ProviderName:   "openai",
					ProviderSystem: "openai",
					RequestedModel: "gpt-4o",
					ResponseModel:  "gpt-4o",
					ResolvedModel:  "gpt-4o",
				},
			},
			{Done: true},
		},
	}

	seq := 0
	start := time.Now()
	resp, err := consumeStream(context.Background(), sp, nil, nil, Options{}, nil, "test", start, &seq, streamThresholds{})
	require.NoError(t, err)
	require.NotNil(t, resp.Attempt)
	assert.Nil(t, resp.Attempt.Timing)
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

func TestRun_StreamingProviderMergesSplitUsage(t *testing.T) {
	sp := &mockStreamingProvider{
		deltas: []StreamDelta{
			{
				Content: "streamed ",
				Model:   "gpt-4o",
				Usage:   &TokenUsage{Input: 12},
			},
			{
				Content: "response",
				Usage:   &TokenUsage{Output: 8, CacheRead: 3},
			},
			{
				FinishReason: "stop",
				Usage:        &TokenUsage{CacheWrite: 4},
				Done:         true,
			},
		},
	}

	expectedUsage := TokenUsage{Input: 12, Output: 8, CacheRead: 3, CacheWrite: 4, Total: 20}
	var responseUsage TokenUsage
	var sessionUsage TokenUsage

	result, err := Run(context.Background(), Request{
		Prompt:   "test",
		Provider: sp,
		Callback: func(e Event) {
			switch e.Type {
			case EventLLMResponse:
				var payload struct {
					Usage TokenUsage `json:"usage"`
				}
				require.NoError(t, json.Unmarshal(e.Data, &payload))
				responseUsage = payload.Usage
			case EventSessionEnd:
				var payload struct {
					Tokens TokenUsage `json:"tokens"`
				}
				require.NoError(t, json.Unmarshal(e.Data, &payload))
				sessionUsage = payload.Tokens
			}
		},
	})
	require.NoError(t, err)
	assert.Equal(t, StatusSuccess, result.Status)
	assert.Equal(t, "streamed response", result.Output)
	assert.Equal(t, expectedUsage, result.Tokens)
	assert.Equal(t, expectedUsage, responseUsage)
	assert.Equal(t, expectedUsage, sessionUsage)
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
	start := time.Now()
	_, err := consumeStream(context.Background(), sp, nil, nil, Options{}, nil, "test", start, &seq, streamThresholds{})
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
	start := time.Now()
	_, err := consumeStream(context.Background(), sp, nil, nil, Options{}, nil, "test", start, &seq, streamThresholds{})
	require.ErrorIs(t, err, netErr)
}

func TestConsumeStream_ReasoningOverflow(t *testing.T) {
	// Stream aborted with ErrReasoningOverflow when reasoning_content
	// exceeds the configured byte limit with no content or tool_call delta.
	chunk := strings.Repeat("x", 4096)
	var deltas []StreamDelta
	// 9 chunks = 36k bytes > custom limit (32k)
	for i := 0; i < 9; i++ {
		deltas = append(deltas, StreamDelta{ReasoningContent: chunk})
	}
	deltas = append(deltas, StreamDelta{Done: true})

	sp := &mockStreamingProvider{deltas: deltas}

	seq := 0
	start := time.Now()
	_, err := consumeStream(context.Background(), sp, nil, nil, Options{}, nil, "test", start, &seq, streamThresholds{
		reasoningByteLimit: 32 * 1024,
		modelName:          "test-model",
	})
	require.ErrorIs(t, err, ErrReasoningOverflow)
	assert.Contains(t, err.Error(), "test-model")
	assert.Contains(t, err.Error(), "32KB")
}

func TestConsumeStream_ReasoningOverflow_NotTriggeredAfterContent(t *testing.T) {
	// If a content delta arrives first, reasoning overflow should not fire even
	// if subsequent reasoning exceeds the limit.
	chunk := strings.Repeat("x", 4096)
	deltas := []StreamDelta{
		{Content: "hello"},
	}
	for i := 0; i < 9; i++ {
		deltas = append(deltas, StreamDelta{ReasoningContent: chunk})
	}
	deltas = append(deltas, StreamDelta{Done: true})

	sp := &mockStreamingProvider{deltas: deltas}

	seq := 0
	start := time.Now()
	resp, err := consumeStream(context.Background(), sp, nil, nil, Options{}, nil, "test", start, &seq, streamThresholds{
		reasoningByteLimit: 32 * 1024,
	})
	require.NoError(t, err)
	assert.Equal(t, "hello", resp.Content)
}

func TestConsumeStream_ReasoningStall(t *testing.T) {
	// AC-FEAT-001-08: stream aborted with ErrReasoningStall when only reasoning_content
	// deltas arrive for longer than the stall timeout with no content or tool_call delta.
	//
	// Use a custom stall timeout smaller than the default to keep the test fast.
	// We test the time.Since path by manipulating reasoningStallStart indirectly:
	// the easiest approach is to set a very old stall start by making the first
	// delta arrive with an ArrivedAt far in the past. Instead, we exercise the
	// code path by temporarily overriding the constant via a helper that accepts
	// a timeout parameter — but since the constants are package-level, we use
	// the internal test package and a small timeout value via a wrapper function.
	//
	// Since reasoningStallTimeout is a package-level constant we cannot override
	// it in a white-box test without modifying the API.  Instead we verify the
	// stall path by using a mock that injects a pre-old ArrivedAt timestamp on
	// the first reasoning delta so that time.Since(reasoningStallStart) exceeds
	// the threshold immediately.
	//
	// We do this by setting ArrivedAt on the reasoning delta to a time far in
	// the past. The stall start is initialized to time.Now() at the top of
	// consumeStream — but the check uses time.Since(reasoningStallStart), which
	// uses the real clock. So we need another approach: send enough reasoning
	// chunks first to nearly-but-not-quite overflow, then verify stall fires
	// after the timeout.
	//
	// For unit test speed, we use a sub-package internal test that can access
	// the constant. Since this file is package agent (white-box), we can
	// directly test the stall path by calling consumeStreamWithConfig (added
	// below) or by using a trick: set a very short delay between deltas so
	// real time elapses.  But reasoningStallTimeout = 120s is too long for a
	// unit test.
	//
	// Resolution: extract consumeStream to accept the thresholds as parameters
	// (or add a consumeStreamConfig variant) — but the bead says keep it simple
	// and not create new config types. Instead, test the stall path via an
	// integration-style approach with a very short timeout constant injected via
	// the test binary override below.
	//
	// Simplest correct approach: expose the stall detection via a thin internal
	// wrapper used only in tests, accepting a custom stall duration.
	t.Run("stall path via short timeout", func(t *testing.T) {
		shortStall := 50 * time.Millisecond

		// A stream that emits reasoning deltas with a delay between each one
		// that will exceed shortStall.
		sp := &mockStreamingProvider{
			delayBetween: 30 * time.Millisecond,
			deltas: []StreamDelta{
				{ReasoningContent: "thinking..."},
				{ReasoningContent: "still thinking..."},
				{ReasoningContent: "more thinking..."},
				{Done: true},
			},
		}

		seq := 0
		start := time.Now()
		_, err := consumeStream(context.Background(), sp, nil, nil, Options{}, nil, "test", start, &seq, streamThresholds{
			reasoningStallTimeout: shortStall,
			modelName:             "test-stall-model",
		})
		require.ErrorIs(t, err, ErrReasoningStall)
		assert.Contains(t, err.Error(), "test-stall-model")
		assert.Contains(t, err.Error(), "50ms")
	})
}

func TestConsumeStream_ReasoningStall_NotTriggeredAfterToolCall(t *testing.T) {
	// If a tool call delta arrives, stall detection should be disabled.
	shortStall := 50 * time.Millisecond

	sp := &mockStreamingProvider{
		delayBetween: 30 * time.Millisecond,
		deltas: []StreamDelta{
			{ToolCallID: "tc1", ToolCallName: "read", ToolCallArgs: `{"path":"a.go"}`},
			{ReasoningContent: "thinking after tool call"},
			{ReasoningContent: "still thinking"},
			{Done: true},
		},
	}

	seq := 0
	start := time.Now()
	resp, err := consumeStream(context.Background(), sp, nil, nil, Options{}, nil, "test", start, &seq, streamThresholds{reasoningStallTimeout: shortStall})
	require.NoError(t, err)
	require.Len(t, resp.ToolCalls, 1)
}

func TestConsumeStream_UnlimitedReasoningByteLimit(t *testing.T) {
	// When reasoningByteLimit is 0, overflow detection is disabled (unlimited).
	chunk := strings.Repeat("x", 4096)
	var deltas []StreamDelta
	for i := 0; i < 100; i++ { // 400KB — well past default 256KB
		deltas = append(deltas, StreamDelta{ReasoningContent: chunk})
	}
	deltas = append(deltas, StreamDelta{Content: "done", Done: true})

	sp := &mockStreamingProvider{deltas: deltas}
	seq := 0
	start := time.Now()
	resp, err := consumeStream(context.Background(), sp, nil, nil, Options{}, nil, "test", start, &seq, streamThresholds{
		reasoningByteLimit:    0,                // unlimited
		reasoningStallTimeout: 10 * time.Minute, // high to avoid stall
	})
	require.NoError(t, err)
	assert.Equal(t, "done", resp.Content)
}

func TestConsumeStream_UnlimitedReasoningStallTimeout(t *testing.T) {
	// When reasoningStallTimeout is 0, stall detection is disabled (unlimited).
	sp := &mockStreamingProvider{
		delayBetween: 30 * time.Millisecond,
		deltas: []StreamDelta{
			{ReasoningContent: "thinking..."},
			{ReasoningContent: "still thinking..."},
			{Content: "done", Done: true},
		},
	}

	seq := 0
	start := time.Now()
	resp, err := consumeStream(context.Background(), sp, nil, nil, Options{}, nil, "test", start, &seq, streamThresholds{
		reasoningStallTimeout: 0,                // unlimited
		reasoningByteLimit:    10 * 1024 * 1024, // high to avoid overflow
	})
	require.NoError(t, err)
	assert.Equal(t, "done", resp.Content)
}

func TestConsumeStream_CustomByteLimit(t *testing.T) {
	// Custom byte limit is respected — 8KB limit, 12KB of reasoning triggers overflow.
	chunk := strings.Repeat("x", 4096)
	deltas := []StreamDelta{
		{ReasoningContent: chunk},
		{ReasoningContent: chunk},
		{ReasoningContent: chunk}, // 12KB > 8KB limit
		{Done: true},
	}

	sp := &mockStreamingProvider{deltas: deltas}
	seq := 0
	start := time.Now()
	_, err := consumeStream(context.Background(), sp, nil, nil, Options{}, nil, "test", start, &seq, streamThresholds{
		reasoningByteLimit: 8 * 1024,
		modelName:          "custom-model",
	})
	require.ErrorIs(t, err, ErrReasoningOverflow)
	assert.Contains(t, err.Error(), "custom-model")
	assert.Contains(t, err.Error(), "8KB")
}

func TestConsumeStream_DefaultThresholds(t *testing.T) {
	// With DefaultReasoningByteLimit (256KB), 200KB of reasoning does NOT
	// trigger overflow but 300KB does.
	chunk := strings.Repeat("x", 4096)

	t.Run("under limit", func(t *testing.T) {
		var deltas []StreamDelta
		for i := 0; i < 50; i++ { // 200KB < 256KB
			deltas = append(deltas, StreamDelta{ReasoningContent: chunk})
		}
		deltas = append(deltas, StreamDelta{Content: "done", Done: true})

		sp := &mockStreamingProvider{deltas: deltas}
		seq := 0
		start := time.Now()
		resp, err := consumeStream(context.Background(), sp, nil, nil, Options{}, nil, "test", start, &seq, streamThresholds{
			reasoningByteLimit:    DefaultReasoningByteLimit,
			reasoningStallTimeout: DefaultReasoningStallTimeout,
		})
		require.NoError(t, err)
		assert.Equal(t, "done", resp.Content)
	})

	t.Run("over limit", func(t *testing.T) {
		var deltas []StreamDelta
		for i := 0; i < 70; i++ { // 280KB > 256KB
			deltas = append(deltas, StreamDelta{ReasoningContent: chunk})
		}
		deltas = append(deltas, StreamDelta{Done: true})

		sp := &mockStreamingProvider{deltas: deltas}
		seq := 0
		start := time.Now()
		_, err := consumeStream(context.Background(), sp, nil, nil, Options{}, nil, "test", start, &seq, streamThresholds{
			reasoningByteLimit:    DefaultReasoningByteLimit,
			reasoningStallTimeout: DefaultReasoningStallTimeout,
		})
		require.ErrorIs(t, err, ErrReasoningOverflow)
	})
}

func TestDefaultConstants(t *testing.T) {
	assert.Equal(t, 256*1024, DefaultReasoningByteLimit)
	assert.Equal(t, 300*time.Second, DefaultReasoningStallTimeout)
}
