package forge

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// Run executes the agent loop: send prompt, process tool calls, repeat until
// the model produces a final text response or limits are reached.
func Run(ctx context.Context, req Request) (Result, error) {
	start := time.Now()

	sessionID := fmt.Sprintf("s-%d", start.UnixNano())
	result := Result{
		SessionID: sessionID,
	}

	if req.Provider == nil {
		return result, fmt.Errorf("forge: provider is required")
	}

	// Build tool definitions for the provider
	toolDefs := make([]ToolDef, len(req.Tools))
	toolMap := make(map[string]Tool, len(req.Tools))
	for i, t := range req.Tools {
		toolDefs[i] = ToolDef{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters:  t.Schema(),
		}
		toolMap[t.Name()] = t
	}

	// Build initial conversation
	var messages []Message
	if req.SystemPrompt != "" {
		messages = append(messages, Message{Role: RoleSystem, Content: req.SystemPrompt})
	}
	messages = append(messages, Message{Role: RoleUser, Content: req.Prompt})

	// Emit session start
	emitCallback(req.Callback, Event{
		SessionID: sessionID,
		Seq:       0,
		Type:      EventSessionStart,
		Timestamp: time.Now().UTC(),
		Data: mustMarshal(map[string]any{
			"prompt":         req.Prompt,
			"system_prompt":  req.SystemPrompt,
			"max_iterations": req.MaxIterations,
			"metadata":       req.Metadata,
		}),
	})

	seq := 1
	opts := Options{}

	for iteration := 0; ; iteration++ {
		// Check iteration limit
		if req.MaxIterations > 0 && iteration >= req.MaxIterations {
			result.Status = StatusIterationLimit
			result.Duration = time.Since(start)
			emitSessionEnd(req.Callback, sessionID, &seq, result)
			return result, nil
		}

		// Check context cancellation
		if ctx.Err() != nil {
			result.Status = StatusCancelled
			result.Duration = time.Since(start)
			emitSessionEnd(req.Callback, sessionID, &seq, result)
			return result, nil
		}

		// Run compaction if configured
		if req.Compactor != nil {
			compacted, compErr := req.Compactor(ctx, messages, req.Provider, result.ToolCalls)
			if compErr != nil {
				// Compaction failure is non-fatal — continue with uncompacted messages
				emitCallback(req.Callback, Event{
					SessionID: sessionID,
					Seq:       seq,
					Type:      EventCompactionEnd,
					Timestamp: time.Now().UTC(),
					Data:      mustMarshal(map[string]any{"error": compErr.Error()}),
				})
				seq++
			} else if len(compacted) < len(messages) {
				// Compaction happened
				emitCallback(req.Callback, Event{
					SessionID: sessionID,
					Seq:       seq,
					Type:      EventCompactionEnd,
					Timestamp: time.Now().UTC(),
					Data: mustMarshal(map[string]any{
						"messages_before": len(messages),
						"messages_after":  len(compacted),
					}),
				})
				seq++
				messages = compacted
			}
		}

		// Emit LLM request event
		emitCallback(req.Callback, Event{
			SessionID: sessionID,
			Seq:       seq,
			Type:      EventLLMRequest,
			Timestamp: time.Now().UTC(),
			Data:      mustMarshal(map[string]any{"messages_count": len(messages)}),
		})
		seq++

		// Call the provider
		llmStart := time.Now()
		resp, err := req.Provider.Chat(ctx, messages, toolDefs, opts)
		llmDuration := time.Since(llmStart)

		if err != nil {
			if ctx.Err() != nil {
				result.Status = StatusCancelled
				result.Duration = time.Since(start)
				emitSessionEnd(req.Callback, sessionID, &seq, result)
				return result, nil
			}
			result.Status = StatusError
			result.Error = fmt.Errorf("forge: provider error: %w", err)
			result.Duration = time.Since(start)
			emitSessionEnd(req.Callback, sessionID, &seq, result)
			return result, result.Error
		}

		// Accumulate tokens
		result.Tokens.Add(resp.Usage)
		result.Model = resp.Model

		// Emit LLM response event
		emitCallback(req.Callback, Event{
			SessionID: sessionID,
			Seq:       seq,
			Type:      EventLLMResponse,
			Timestamp: time.Now().UTC(),
			Data: mustMarshal(map[string]any{
				"content":       resp.Content,
				"tool_calls":    len(resp.ToolCalls),
				"usage":         resp.Usage,
				"latency_ms":    llmDuration.Milliseconds(),
				"finish_reason": resp.FinishReason,
			}),
		})
		seq++

		// If no tool calls, we're done — the model returned a final response
		if len(resp.ToolCalls) == 0 {
			result.Status = StatusSuccess
			result.Output = resp.Content
			result.Duration = time.Since(start)
			emitSessionEnd(req.Callback, sessionID, &seq, result)
			return result, nil
		}

		// Append assistant message with tool calls
		messages = append(messages, Message{
			Role:      RoleAssistant,
			Content:   resp.Content,
			ToolCalls: resp.ToolCalls,
		})

		// Execute each tool call sequentially
		for _, tc := range resp.ToolCalls {
			tool, ok := toolMap[tc.Name]
			if !ok {
				// Unknown tool — return error to the model
				messages = append(messages, Message{
					Role:       RoleTool,
					Content:    fmt.Sprintf("error: unknown tool %q", tc.Name),
					ToolCallID: tc.ID,
				})
				result.ToolCalls = append(result.ToolCalls, ToolCallLog{
					Tool:  tc.Name,
					Input: tc.Arguments,
					Error: fmt.Sprintf("unknown tool %q", tc.Name),
				})
				continue
			}

			// Execute the tool
			toolStart := time.Now()
			output, toolErr := tool.Execute(ctx, tc.Arguments)
			toolDuration := time.Since(toolStart)

			log := ToolCallLog{
				Tool:     tc.Name,
				Input:    tc.Arguments,
				Output:   output,
				Duration: toolDuration,
			}

			if toolErr != nil {
				log.Error = toolErr.Error()
				output = fmt.Sprintf("error: %s", toolErr.Error())
			}

			result.ToolCalls = append(result.ToolCalls, log)

			// Emit tool call event
			emitCallback(req.Callback, Event{
				SessionID: sessionID,
				Seq:       seq,
				Type:      EventToolCall,
				Timestamp: time.Now().UTC(),
				Data: mustMarshal(map[string]any{
					"tool":        tc.Name,
					"input":       tc.Arguments,
					"output":      truncateForLog(output, 10000),
					"duration_ms": toolDuration.Milliseconds(),
					"error":       log.Error,
				}),
			})
			seq++

			// Append tool result to conversation
			messages = append(messages, Message{
				Role:       RoleTool,
				Content:    output,
				ToolCallID: tc.ID,
			})
		}
	}
}

func emitCallback(cb EventCallback, e Event) {
	if cb != nil {
		cb(e)
	}
}

func emitSessionEnd(cb EventCallback, sessionID string, seq *int, result Result) {
	errStr := ""
	if result.Error != nil {
		errStr = result.Error.Error()
	}
	emitCallback(cb, Event{
		SessionID: sessionID,
		Seq:       *seq,
		Type:      EventSessionEnd,
		Timestamp: time.Now().UTC(),
		Data: mustMarshal(map[string]any{
			"status":      result.Status,
			"output":      result.Output,
			"tokens":      result.Tokens,
			"duration_ms": result.Duration.Milliseconds(),
			"error":       errStr,
		}),
	})
	*seq++
}

func mustMarshal(v any) json.RawMessage {
	data, _ := json.Marshal(v)
	return data
}

func truncateForLog(s string, maxLen int) string {
	if len(s) > maxLen {
		return s[:maxLen] + "...[truncated]"
	}
	return s
}
