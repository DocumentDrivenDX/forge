package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/DocumentDrivenDX/agent/telemetry"
)

// Run executes the agent loop: send prompt, process tool calls, repeat until
// the model produces a final text response or limits are reached.
func Run(ctx context.Context, req Request) (Result, error) {
	start := time.Now()
	const maxProviderAttempts = 3

	sessionID := fmt.Sprintf("s-%d", start.UnixNano())
	result := Result{
		SessionID: sessionID,
	}

	if req.Provider == nil {
		return result, fmt.Errorf("agent: provider is required")
	}
	runtimeTelemetry := req.Telemetry
	if runtimeTelemetry == nil {
		runtimeTelemetry = telemetry.NewNoop()
	}
	rootCtx, rootSpan := runtimeTelemetry.StartInvokeAgent(ctx, telemetry.InvokeAgentSpan{
		HarnessName:    "agent",
		SessionID:      sessionID,
		ConversationID: sessionID,
	})
	ctx = rootCtx
	defer func() {
		if result.Error != nil {
			recordSpanError(rootSpan, result.Error)
		}
		rootSpan.End()
	}()

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
	messages := append([]Message(nil), req.History...)
	messages = append(messages, Message{Role: RoleUser, Content: req.Prompt})
	snapshotMessages := func() {
		result.Messages = append([]Message(nil), messages...)
	}

	// Emit session start
	sessionProvider, sessionModel := sessionStartIdentity(req.Provider)
	chatProviderSystem, chatServerAddress, chatServerPort := chatStartIdentity(req.Provider)
	emitCallback(req.Callback, Event{
		SessionID: sessionID,
		Seq:       0,
		Type:      EventSessionStart,
		Timestamp: time.Now().UTC(),
		Data: mustMarshal(map[string]any{
			"provider":       sessionProvider,
			"model":          sessionModel,
			"work_dir":       req.WorkDir,
			"prompt":         req.Prompt,
			"system_prompt":  req.SystemPrompt,
			"max_iterations": req.MaxIterations,
			"metadata":       req.Metadata,
		}),
	})

	seq := 1
	opts := Options{}
	sessionCostKnown := true

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
					"error":   compErr.Error(),
					"success": false,
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
					"success":        true,
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
			snapshotMessages()
			emitSessionEnd(req.Callback, sessionID, &seq, result, req.Metadata)
			return result, nil
		}

		// Check context cancellation
		if ctx.Err() != nil {
			result.Status = StatusCancelled
			result.Duration = time.Since(start)
			snapshotMessages()
			emitSessionEnd(req.Callback, sessionID, &seq, result, req.Metadata)
			return result, nil
		}

		// Run compaction before iteration (pre-iteration check)
		runCompaction()

		providerMessages := append([]Message(nil), messages...)
		if req.SystemPrompt != "" {
			providerMessages = append([]Message{{
				Role:    RoleSystem,
				Content: req.SystemPrompt,
			}}, providerMessages...)
		}

		var resp Response
		var err error
		for attempt := 1; attempt <= maxProviderAttempts; attempt++ {
			chatStart := time.Now()
			chatCtx, chatSpan := runtimeTelemetry.StartChat(ctx, telemetry.ChatSpan{
				HarnessName:    "agent",
				SessionID:      sessionID,
				ConversationID: sessionID,
				TurnIndex:      iteration + 1,
				AttemptIndex:   attempt,
				StartTime:      chatStart,
				ProviderName:   sessionProvider,
				ProviderSystem: chatProviderSystem,
				RequestedModel: sessionModel,
				ServerAddress:  chatServerAddress,
				ServerPort:     chatServerPort,
			})
			// Emit LLM request event with full message bodies and tool definitions.
			emitCallback(req.Callback, Event{
				SessionID: sessionID,
				Seq:       seq,
				Type:      EventLLMRequest,
				Timestamp: time.Now().UTC(),
				Data: mustMarshal(map[string]any{
					"attempt_index": attempt,
					"messages":      providerMessages,
					"tools":         toolDefs,
				}),
			})
			seq++

			llmStart := time.Now()
			if sp, ok := req.Provider.(StreamingProvider); ok && !req.NoStream {
				resp, err = consumeStream(chatCtx, sp, providerMessages, toolDefs, opts, req.Callback, sessionID, chatStart, &seq)
			} else {
				resp, err = req.Provider.Chat(chatCtx, providerMessages, toolDefs, opts)
			}
			llmDuration := time.Since(llmStart)

			if err != nil {
				recordSpanError(chatSpan, err)
				chatSpan.End()
				emitCallback(req.Callback, Event{
					SessionID: sessionID,
					Seq:       seq,
					Type:      EventLLMResponse,
					Timestamp: time.Now().UTC(),
					Data: mustMarshal(map[string]any{
						"attempt_index": attempt,
						"error":         err.Error(),
						"latency_ms":    llmDuration.Milliseconds(),
					}),
				})
				seq++

				if ctx.Err() != nil {
					result.Status = StatusCancelled
					result.Duration = time.Since(start)
					snapshotMessages()
					emitSessionEnd(req.Callback, sessionID, &seq, result, req.Metadata)
					return result, nil
				}

				if attempt < maxProviderAttempts {
					delaySeconds := time.Duration(1 << min(attempt-1, 10))
					delay := time.Second * delaySeconds
					select {
					case <-ctx.Done():
						result.Status = StatusCancelled
						result.Duration = time.Since(start)
						snapshotMessages()
						emitSessionEnd(req.Callback, sessionID, &seq, result, req.Metadata)
						return result, nil
					case <-time.After(delay):
					}
					continue
				}

				result.Status = StatusError
				result.Error = fmt.Errorf("agent: provider error: %w", err)
				result.Duration = time.Since(start)
				snapshotMessages()
				emitSessionEnd(req.Callback, sessionID, &seq, result, req.Metadata)
				return result, result.Error
			}

			if resp.Attempt == nil {
				resp.Attempt = &AttemptMetadata{}
			}
			resp.Attempt.AttemptIndex = attempt
			if (resp.Attempt.Cost == nil || resp.Attempt.Cost.Source == CostSourceUnknown) &&
				resp.Attempt.ProviderSystem != "" &&
				resp.Attempt.ResolvedModel != "" {
				if configuredCost, ok := runtimeTelemetry.ResolveCost(resp.Attempt.ProviderSystem, resp.Attempt.ResolvedModel); ok {
					resp.Attempt.Cost = &CostAttribution{
						Source:     CostSourceConfigured,
						Currency:   configuredCost.Currency,
						Amount:     configuredCost.Amount,
						PricingRef: configuredCost.PricingRef,
					}
				}
			}

			// Accumulate tokens
			result.Tokens.Add(resp.Usage)
			result.Model = resp.Model

			// Accumulate cost only when the attempt provides known provenance.
			iterCost, costKnown := attemptCostUSD(resp.Attempt)
			if costKnown {
				if sessionCostKnown {
					result.CostUSD += iterCost
				}
			} else {
				sessionCostKnown = false
				result.CostUSD = -1
			}

			// Emit LLM response event with full tool call bodies.
			responseData := map[string]any{
				"attempt_index": attempt,
				"content":       resp.Content,
				"tool_calls":    resp.ToolCalls,
				"usage":         resp.Usage,
				"latency_ms":    llmDuration.Milliseconds(),
				"model":         resp.Model,
				"finish_reason": resp.FinishReason,
				"attempt":       resp.Attempt,
			}
			if costKnown {
				responseData["cost_usd"] = iterCost
			}
			emitCallback(req.Callback, Event{
				SessionID: sessionID,
				Seq:       seq,
				Type:      EventLLMResponse,
				Timestamp: time.Now().UTC(),
				Data:      mustMarshal(responseData),
			})
			seq++
			annotateChatSpan(chatSpan, resp)
			chatSpan.End()

			assistantMsg := Message{
				Role:      RoleAssistant,
				Content:   resp.Content,
				ToolCalls: resp.ToolCalls,
			}
			messages = append(messages, assistantMsg)

			break
		}

		// If no tool calls, we're done — the model returned a final response
		if len(resp.ToolCalls) == 0 {
			result.Status = StatusSuccess
			result.Output = resp.Content
			result.Duration = time.Since(start)
			snapshotMessages()
			emitSessionEnd(req.Callback, sessionID, &seq, result, req.Metadata)
			return result, nil
		}

		// Execute each tool call sequentially
		for toolExecutionIndex, tc := range resp.ToolCalls {
			toolCtx, toolSpan := runtimeTelemetry.StartExecuteTool(ctx, telemetry.ExecuteToolSpan{
				HarnessName:        "agent",
				SessionID:          sessionID,
				ConversationID:     sessionID,
				TurnIndex:          iteration + 1,
				ToolExecutionIndex: toolExecutionIndex + 1,
				ToolName:           tc.Name,
				ToolType:           "function",
				ToolCallID:         tc.ID,
			})
			tool, ok := toolMap[tc.Name]
			if !ok {
				// Unknown tool — return error to the model
				unknownErr := fmt.Errorf("unknown tool %q", tc.Name)
				recordSpanError(toolSpan, unknownErr)
				toolSpan.End()
				messages = append(messages, Message{
					Role:       RoleTool,
					Content:    fmt.Sprintf("error: %s", unknownErr.Error()),
					ToolCallID: tc.ID,
				})
				result.ToolCalls = append(result.ToolCalls, ToolCallLog{
					Tool:  tc.Name,
					Input: tc.Arguments,
					Error: unknownErr.Error(),
				})
				continue
			}

			// Execute the tool
			toolStart := time.Now()
			output, toolErr := tool.Execute(toolCtx, tc.Arguments)
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
				recordSpanError(toolSpan, toolErr)
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
			toolSpan.End()

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

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
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
	data := map[string]any{
		"status":      result.Status,
		"output":      result.Output,
		"tokens":      result.Tokens,
		"duration_ms": result.Duration.Milliseconds(),
		"model":       result.Model,
		"metadata":    metadata,
		"error":       errStr,
	}
	if result.CostUSD >= 0 {
		data["cost_usd"] = result.CostUSD
	}
	emitCallback(cb, Event{
		SessionID: sessionID,
		Seq:       *seq,
		Type:      EventSessionEnd,
		Timestamp: time.Now().UTC(),
		Data:      mustMarshal(data),
	})
	*seq++
}

func mustMarshal(v any) json.RawMessage {
	data, _ := json.Marshal(v)
	return data
}

type sessionStartIdentityProvider interface {
	SessionStartMetadata() (provider, model string)
}

type chatStartIdentityProvider interface {
	ChatStartMetadata() (providerSystem, serverAddress string, serverPort int)
}

func sessionStartIdentity(provider Provider) (string, string) {
	if provider == nil {
		return "unknown", "unknown"
	}

	metaProvider, ok := provider.(sessionStartIdentityProvider)
	if !ok {
		return "unknown", "unknown"
	}

	providerName, modelName := metaProvider.SessionStartMetadata()
	if providerName == "" {
		providerName = "unknown"
	}
	if modelName == "" {
		modelName = "unknown"
	}
	return providerName, modelName
}

func chatStartIdentity(provider Provider) (string, string, int) {
	if provider == nil {
		return "unknown", "", 0
	}

	metaProvider, ok := provider.(chatStartIdentityProvider)
	if !ok {
		providerSystem, _ := sessionStartIdentity(provider)
		return providerSystem, "", 0
	}

	providerSystem, serverAddress, serverPort := metaProvider.ChatStartMetadata()
	if providerSystem == "" {
		providerSystem = "unknown"
	}
	return providerSystem, serverAddress, serverPort
}

func attemptCostUSD(attempt *AttemptMetadata) (float64, bool) {
	if attempt == nil || attempt.Cost == nil || attempt.Cost.Amount == nil {
		return 0, false
	}

	switch attempt.Cost.Source {
	case CostSourceProviderReported, CostSourceGatewayReported, CostSourceConfigured:
		return *attempt.Cost.Amount, true
	default:
		return 0, false
	}
}

func truncateForLog(s string, maxLen int) string {
	if len(s) > maxLen {
		return s[:maxLen] + "...[truncated]"
	}
	return s
}

func annotateChatSpan(span trace.Span, resp Response) {
	if span == nil {
		return
	}

	attrs := make([]attribute.KeyValue, 0, 16)
	if resp.Attempt != nil {
		attrs = appendStringAttr(attrs, telemetry.KeyProviderName, resp.Attempt.ProviderName)
		attrs = appendStringAttr(attrs, telemetry.KeyProviderSystem, resp.Attempt.ProviderSystem)
		attrs = appendStringAttr(attrs, telemetry.KeyProviderRoute, resp.Attempt.Route)
		attrs = appendStringAttr(attrs, telemetry.KeyRequestModel, resp.Attempt.RequestedModel)
		attrs = appendStringAttr(attrs, telemetry.KeyResponseModel, resp.Attempt.ResponseModel)
		attrs = appendStringAttr(attrs, telemetry.KeyProviderModelResolved, resp.Attempt.ResolvedModel)
		attrs = appendStringAttr(attrs, telemetry.KeyServerAddress, resp.Attempt.ServerAddress)
		attrs = appendIntAttr(attrs, telemetry.KeyServerPort, resp.Attempt.ServerPort)
		if resp.Attempt.Timing != nil {
			attrs = appendDurationMSAttr(attrs, telemetry.KeyTimingFirstTokenMS, resp.Attempt.Timing.FirstToken)
			attrs = appendDurationMSAttr(attrs, telemetry.KeyTimingQueueMS, resp.Attempt.Timing.Queue)
			attrs = appendDurationMSAttr(attrs, telemetry.KeyTimingPrefillMS, resp.Attempt.Timing.Prefill)
			attrs = appendDurationMSAttr(attrs, telemetry.KeyTimingGenerationMS, resp.Attempt.Timing.Generation)
			attrs = appendDurationMSAttr(attrs, telemetry.KeyTimingCacheReadMS, resp.Attempt.Timing.CacheRead)
			attrs = appendDurationMSAttr(attrs, telemetry.KeyTimingCacheWriteMS, resp.Attempt.Timing.CacheWrite)
		}
	}
	if resp.Usage.Input > 0 || resp.Usage.Output > 0 || resp.Usage.CacheRead > 0 || resp.Usage.CacheWrite > 0 {
		attrs = appendIntAttr(attrs, telemetry.KeyUsageInput, resp.Usage.Input)
		attrs = appendIntAttr(attrs, telemetry.KeyUsageOutput, resp.Usage.Output)
		attrs = appendIntAttr(attrs, telemetry.KeyUsageCacheRead, resp.Usage.CacheRead)
		attrs = appendIntAttr(attrs, telemetry.KeyUsageCacheWrite, resp.Usage.CacheWrite)
	}
	span.SetAttributes(attrs...)
}

func recordSpanError(span trace.Span, err error) {
	if span == nil || err == nil {
		return
	}
	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())
	span.SetAttributes(attribute.String(telemetry.KeyErrorType, fmt.Sprintf("%T", err)))
}

func appendStringAttr(dst []attribute.KeyValue, key, value string) []attribute.KeyValue {
	if value == "" {
		return dst
	}
	return append(dst, attribute.String(key, value))
}

func appendIntAttr(dst []attribute.KeyValue, key string, value int) []attribute.KeyValue {
	if value == 0 {
		return dst
	}
	return append(dst, attribute.Int(key, value))
}

func appendDurationMSAttr(dst []attribute.KeyValue, key string, value *time.Duration) []attribute.KeyValue {
	if value == nil {
		return dst
	}
	ms := float64(*value) / float64(time.Millisecond)
	return append(dst, attribute.Float64(key, ms))
}
