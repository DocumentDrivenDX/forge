package agent

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

// consumeStream reads from a StreamingProvider's channel, emits delta events,
// and assembles a complete Response.
func consumeStream(
	ctx context.Context,
	sp StreamingProvider,
	messages []Message,
	tools []ToolDef,
	opts Options,
	callback EventCallback,
	sessionID string,
	seq *int,
) (Response, error) {
	streamStart := time.Now()
	ch, err := sp.ChatStream(ctx, messages, tools, opts)
	if err != nil {
		return Response{}, err
	}

	var resp Response
	var contentBuf strings.Builder
	var firstOutputAt time.Time

	// Track tool call assembly — deltas arrive as fragments
	type toolCallState struct {
		ID      string
		Name    string
		ArgsBuf strings.Builder
	}
	toolCalls := make(map[string]*toolCallState)
	var toolCallOrder []string

	for delta := range ch {
		// Emit delta event
		if callback != nil {
			emitCallback(callback, Event{
				SessionID: sessionID,
				Seq:       *seq,
				Type:      EventLLMDelta,
				Timestamp: time.Now().UTC(),
				Data:      mustMarshal(delta),
			})
			*seq++
		}

		// Accumulate content
		if delta.Content != "" {
			contentBuf.WriteString(delta.Content)
		}

		// Accumulate tool call fragments
		if delta.ToolCallID != "" {
			tc, exists := toolCalls[delta.ToolCallID]
			if !exists {
				tc = &toolCallState{ID: delta.ToolCallID}
				toolCalls[delta.ToolCallID] = tc
				toolCallOrder = append(toolCallOrder, delta.ToolCallID)
			}
			if delta.ToolCallName != "" {
				tc.Name = delta.ToolCallName
			}
			if delta.ToolCallArgs != "" {
				tc.ArgsBuf.WriteString(delta.ToolCallArgs)
			}
		}

		// Capture model and finish reason from final delta
		if delta.Model != "" {
			resp.Model = delta.Model
		}
		if delta.Attempt != nil {
			attempt := *delta.Attempt
			resp.Attempt = &attempt
		}
		if delta.FinishReason != "" {
			resp.FinishReason = delta.FinishReason
		}
		if delta.Usage != nil {
			resp.Usage.Add(*delta.Usage)
		}

		if delta.Err != nil {
			return Response{}, delta.Err
		}

		if firstOutputAt.IsZero() && streamDeltaHasOutput(delta) {
			firstOutputAt = time.Now()
		}

		if delta.Done {
			break
		}
	}

	resp.Content = contentBuf.String()
	resp.Usage.Total = resp.Usage.Input + resp.Usage.Output

	if !firstOutputAt.IsZero() {
		if resp.Attempt == nil {
			resp.Attempt = &AttemptMetadata{}
		}
		if resp.Attempt.Timing == nil {
			resp.Attempt.Timing = &TimingBreakdown{}
		}
		firstToken := firstOutputAt.Sub(streamStart)
		generation := time.Since(firstOutputAt)
		resp.Attempt.Timing.FirstToken = &firstToken
		resp.Attempt.Timing.Generation = &generation
	}

	// Assemble tool calls from fragments
	for _, id := range toolCallOrder {
		tc := toolCalls[id]
		resp.ToolCalls = append(resp.ToolCalls, ToolCall{
			ID:        tc.ID,
			Name:      tc.Name,
			Arguments: json.RawMessage(tc.ArgsBuf.String()),
		})
	}

	return resp, nil
}

func streamDeltaHasOutput(delta StreamDelta) bool {
	return delta.Content != "" ||
		delta.ToolCallID != "" ||
		delta.ToolCallName != "" ||
		delta.ToolCallArgs != ""
}
