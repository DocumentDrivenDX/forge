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

	// runCompaction handles the compaction logic and event emission.
	// Returns true if compaction occurred.
	runCompaction := func() (bool, *CompactionResult) {
		if req.Compactor == nil {
			return false, nil
		}

		// Emit compaction start event
		emitCallback(req.Callback, Event{
			SessionID: sessionID,
			Seq:       seq,
			Type:      EventCompactionStart,
			Timestamp: time.Now().UTC(),
			Data: mustMarshal(map[string]any{
				"messages_before": len(messages),
			}),
		})
		seq++

		// Run compaction
		compacted, compResult, compErr := req.Compactor(ctx, messages, req.Provider, result.ToolCalls)
		if compErr != nil {
			// Compaction failure is non-fatal — continue with uncompacted messages
			emitCallback(req.Callback, Event{
				SessionID: sessionID,
				Seq:       seq,
				Type:      EventCompactionEnd,
				Timestamp: time.Now().UTC(),
				Data: mustMarshal(map[string]any{
					"error":    compErr.Error(),
					"success":  false,
				}),
			})
			seq++
			return false, nil
		}

		if compResult != nil {
			// Compaction happened — emit end with full result
			emitCallback(req.Callback, Event{
				SessionID: sessionID,
				Seq:       seq,
				Type:      EventCompactionEnd,
				Timestamp: time.Now().UTC(),
				Data: mustMarshal(map[string]any{
					"success":         true,
					"summary":        compResult.Summary,
					"file_ops":       compResult.FileOps,
					"tokens_before":  compResult.TokensBefore,
					"tokens_after":   compResult.TokensAfter,
					"warning":        compResult.Warning,
					"messages_after": len(compacted),
				}),
			})
			seq++
			messages = compacted
			return true, compResult
		}

		// No compaction happened
		return false, nil
	}

	for iteration := 0; ; iteration++ {
		// Check iteration limit
		if req.MaxIterations > 0 && iteration >= req.MaxIterations {
			result.Status = StatusIterationLimit
			result.Duration = time.Since(start)
			emitSessionEnd(req.Callback, sessionID, &seq, result, req.Metadata)
			return result, nil
		}

		// Check context cancellation
		if ctx.Err() != nil {
			result.Status = StatusCancelled
			result.Duration = time.Since(start)
			emitSessionEnd(req.Callback, sessionID, &seq, result, req.Metadata)
			return result, nil
		}

		// Run compaction before iteration (pre-iteration check)
		runCompaction()

		// Emit LLM request event with full message bodies and tool definitions
		emitCallback(req.Callback, Event{
			SessionID: sessionID,
			Seq:       seq,
			Type:      EventLLMRequest,
			Timestamp: time.Now().UTC(),
			Data: mustMarshal(map[string]any{
				"messages": messages,
				"tools":    toolDefs,
			}),
		})
		seq++

		// Call the provider (streaming if supported)
		llmStart := time.Now()
		var resp Response
		var err error
		if sp, ok := req.Provider.(StreamingProvider); ok && !req.NoStream {
			resp, err = consumeStream(ctx, sp, messages, toolDefs, opts, req.Callback, sessionID, &seq)
		} else {
			resp, err = req.Provider.Chat(ctx, messages, toolDefs, opts)
		}
		llmDuration := time.Since(llmStart)

		if err != nil {
			if ctx.Err() != nil {
				result.Status = StatusCancelled
				result.Duration = time.Since(start)
				emitSessionEnd(req.Callback, sessionID, &seq, result, req.Metadata)
				return result, nil
			}
			result.Status = StatusError
			result.Error = fmt.Errorf("forge: provider error: %w", err)
			result.Duration = time.Since(start)
			emitSessionEnd(req.Callback, sessionID, &seq, result, req.Metadata)
			return result, result.Error
		}

		// Accumulate tokens
		result.Tokens.Add(resp.Usage)
		result.Model = resp.Model

		// Accumulate cost
		iterCost := DefaultPricing.EstimateCost(resp.Model, resp.Usage.Input, resp.Usage.Output)
		if iterCost < 0 {
			// Unknown model — mark total cost as unknown if not already set
			if result.CostUSD == 0 {
				result.CostUSD = -1
			}
		} else if result.CostUSD >= 0 {
			result.CostUSD += iterCost
		}

		// Emit LLM response event with full tool call bodies
		emitCallback(req.Callback, Event{
			SessionID: sessionID,
			Seq:       seq,
			Type:      EventLLMResponse,
			Timestamp: time.Now().UTC(),
			Data: mustMarshal(map[string]any{
				"content":       resp.Content,
				"tool_calls":    resp.ToolCalls,
				"usage":         resp.Usage,
				"cost_usd":      iterCost,
				"latency_ms":    llmDuration.Milliseconds(),
				"model":         resp.Model,
				"finish_reason": resp.FinishReason,
			}),
		})
		seq++

		// If no tool calls, we're done — the model returned a final response
		if len(resp.ToolCalls) == 0 {
			result.Status = StatusSuccess
			result.Output = resp.Content
			result.Duration = time.Since(start)
			emitSessionEnd(req.Callback, sessionID, &seq, result, req.Metadata)
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

		// Mid-iteration compaction check (after tool results)
		// This handles cases where large bash output increases token count
		runCompaction()
	}
}

func emitCallback(cb EventCallback, e Event) {
	if cb != nil {
		cb(e)
	}
}

func emitSessionEnd(cb EventCallback, sessionID string, seq *int, result Result, metadata map[string]string) {
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
			"cost_usd":    result.CostUSD,
			"duration_ms": result.Duration.Milliseconds(),
			"model":       result.Model,
			"metadata":    metadata,
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
