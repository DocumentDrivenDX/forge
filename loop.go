package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/DocumentDrivenDX/agent/internal/compactionctx"
	"github.com/DocumentDrivenDX/agent/telemetry"
)

// Run executes the agent loop: send prompt, process tool calls, repeat until
// the model produces a final text response or limits are reached.
func Run(ctx context.Context, req Request) (Result, error) {
	start := time.Now()
	const maxProviderAttempts = 5

	sessionID := fmt.Sprintf("s-%d", start.UnixNano())
	result := Result{
		SessionID:         sessionID,
		SelectedProvider:  req.SelectedProvider,
		SelectedRoute:     req.SelectedRoute,
		RequestedModel:    req.RequestedModel,
		RequestedModelRef: req.RequestedModelRef,
		ResolvedModelRef:  req.ResolvedModelRef,
		ResolvedModel:     req.ResolvedModel,
	}

	if req.Provider == nil {
		return result, fmt.Errorf("agent: provider is required")
	}
	runtimeTelemetry := req.Telemetry
	if runtimeTelemetry == nil {
		runtimeTelemetry = telemetry.NewNoop()
	}
	defer runtimeTelemetry.Shutdown(context.Background())
	rootCtx, rootSpan := runtimeTelemetry.StartInvokeAgent(ctx, telemetry.InvokeAgentSpan{
		HarnessName:    "agent",
		SessionID:      sessionID,
		ConversationID: sessionID,
	})
	ctx = rootCtx
	chatMetricsRecorder, _ := runtimeTelemetry.(telemetry.ChatMetricsRecorder)
	sessionCostObserved := false
	sessionCostKnown := true
	sessionCostSource := ""
	sessionCostCurrency := ""
	sessionCostPricingRef := ""
	sessionCostStable := true
	defer func() {
		applyRoutingReport(req.Provider, &result)
		applyRoutingSpanAttributes(rootSpan, result)
		applySessionCostAttributes(rootSpan, sessionCostObserved, sessionCostKnown, sessionCostSource, result.CostUSD, sessionCostCurrency, sessionCostPricingRef, sessionCostStable)
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
			"provider":            sessionProvider,
			"model":               sessionModel,
			"selected_provider":   req.SelectedProvider,
			"selected_route":      req.SelectedRoute,
			"requested_model":     req.RequestedModel,
			"requested_model_ref": req.RequestedModelRef,
			"resolved_model_ref":  req.ResolvedModelRef,
			"resolved_model":      req.ResolvedModel,
			"work_dir":            req.WorkDir,
			"prompt":              req.Prompt,
			"system_prompt":       req.SystemPrompt,
			"max_iterations":      req.MaxIterations,
			"metadata":            req.Metadata,
		}),
	})

	seq := 1
	opts := Options{}

	// Tool-call loop detection: abort when the same fingerprint repeats consecutively.
	const toolCallLoopLimit = 3
	var lastToolCallFingerprint string
	consecutiveToolCallCount := 0

	compactionCtx := ctx
	if req.SystemPrompt != "" {
		compactionCtx = compactionctx.WithPrefixTokens(compactionCtx, estimateCompactionPrefixTokens(req.SystemPrompt))
	}

	// runCompaction handles the compaction logic and event emission.
	// Returns true if compaction occurred; returns ErrCompactionNoFit when the
	// compactor could not produce an in-budget history.
	runCompaction := func() (bool, *CompactionResult, error) {
		if req.Compactor == nil {
			return false, nil, nil
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
		compacted, compResult, compErr := req.Compactor(compactionCtx, messages, req.Provider, result.ToolCalls)
		if compErr != nil {
			// Compaction failure is non-fatal — continue with uncompacted messages
			emitCallback(req.Callback, Event{
				SessionID: sessionID,
				Seq:       seq,
				Type:      EventCompactionEnd,
				Timestamp: time.Now().UTC(),
				Data: mustMarshal(map[string]any{
					"error":           compErr.Error(),
					"success":         false,
					"no_compaction":   true,
					"messages_before": len(messages),
					"messages_after":  len(messages),
				}),
			})
			seq++
			if errors.Is(compErr, ErrCompactionNoFit) {
				return false, nil, compErr
			}
			return false, nil, nil
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
			return true, compResult, nil
		}

		// No compaction happened - still close the event pair so callbacks stay balanced.
		emitCallback(req.Callback, Event{
			SessionID: sessionID,
			Seq:       seq,
			Type:      EventCompactionEnd,
			Timestamp: time.Now().UTC(),
			Data: mustMarshal(map[string]any{
				"success":         false,
				"no_compaction":   true,
				"messages_before": len(messages),
				"messages_after":  len(messages),
			}),
		})
		seq++
		return false, nil, nil
	}

	for iteration := 0; ; iteration++ {
		// Check iteration limit
		if req.MaxIterations > 0 && iteration >= req.MaxIterations {
			result.Status = StatusIterationLimit
			result.Duration = time.Since(start)
			snapshotMessages()
			emitFinalSessionEnd(req.Callback, sessionID, &seq, req.Provider, &result, req.Metadata)
			return result, nil
		}

		// Check context cancellation
		if ctx.Err() != nil {
			result.Status = StatusCancelled
			result.Duration = time.Since(start)
			snapshotMessages()
			emitFinalSessionEnd(req.Callback, sessionID, &seq, req.Provider, &result, req.Metadata)
			return result, nil
		}

		// Run compaction before iteration (pre-iteration check)
		if _, _, compErr := runCompaction(); compErr != nil {
			result.Status = StatusError
			result.Error = compErr
			result.Duration = time.Since(start)
			snapshotMessages()
			emitFinalSessionEnd(req.Callback, sessionID, &seq, req.Provider, &result, req.Metadata)
			return result, compErr
		}

		providerMessages := append([]Message(nil), messages...)
		if req.SystemPrompt != "" {
			providerMessages = append([]Message{{
				Role:    RoleSystem,
				Content: req.SystemPrompt,
			}}, providerMessages...)
		}

		var resp Response
		var err error
		overflowCompacted := false
	providerRetry:
		for attempt := 1; attempt <= maxProviderAttempts; attempt++ {
			chatStart := time.Now()
			chatAttrs := telemetry.ChatSpan{
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
			}
			chatCtx, chatSpan := runtimeTelemetry.StartChat(ctx, chatAttrs)
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

			if resp.Attempt == nil {
				resp.Attempt = &AttemptMetadata{}
			}
			resp.Attempt.AttemptIndex = attempt
			if resp.Attempt.ProviderName == "" {
				resp.Attempt.ProviderName = req.SelectedProvider
			}
			if resp.Attempt.Route == "" {
				resp.Attempt.Route = req.SelectedRoute
			}
			if resp.Attempt.ResolvedModel == "" && req.ResolvedModel != "" {
				resp.Attempt.ResolvedModel = req.ResolvedModel
			}
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

			// Preserve usage and cost for both successful and failed attempts.
			result.Tokens.Add(resp.Usage)
			if resp.Model != "" {
				result.Model = resp.Model
			}

			iterCost, costKnown := attemptCostUSD(resp.Attempt)
			if costKnown {
				sessionCostObserved = true
				if resp.Attempt != nil && resp.Attempt.Cost != nil {
					if sessionCostSource == "" {
						sessionCostSource = string(resp.Attempt.Cost.Source)
						sessionCostCurrency = resp.Attempt.Cost.Currency
						sessionCostPricingRef = resp.Attempt.Cost.PricingRef
					} else if sessionCostSource != string(resp.Attempt.Cost.Source) || sessionCostCurrency != resp.Attempt.Cost.Currency || sessionCostPricingRef != resp.Attempt.Cost.PricingRef {
						sessionCostStable = false
					}
				}
				if sessionCostKnown {
					result.CostUSD += iterCost
				}
			} else {
				sessionCostObserved = true
				sessionCostKnown = false
				result.CostUSD = -1
			}

			if chatMetricsRecorder != nil {
				chatMetricsRecorder.RecordChatMetrics(chatCtx, chatAttrs, telemetry.ChatMetrics{
					ResponseModel: resp.Model,
					ResolvedModel: func() string {
						if resp.Attempt != nil {
							return resp.Attempt.ResolvedModel
						}
						return ""
					}(),
					Usage: telemetry.Usage{
						Input:      resp.Usage.Input,
						Output:     resp.Usage.Output,
						CacheRead:  resp.Usage.CacheRead,
						CacheWrite: resp.Usage.CacheWrite,
						Total:      resp.Usage.Total,
					},
					Duration: llmDuration,
					Err:      err,
				})
			}

			if err != nil {
				recordSpanError(chatSpan, err)
				annotateChatSpan(chatSpan, resp)
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
					emitFinalSessionEnd(req.Callback, sessionID, &seq, req.Provider, &result, req.Metadata)
					return result, nil
				}

				// Reasoning loop detection: the model produced only reasoning
				// tokens past the byte or stall threshold. Log a warning and stop
				// looping — this is non-retryable and should not count as an error
				// in benchmark mode.
				if errors.Is(err, ErrReasoningOverflow) || errors.Is(err, ErrReasoningStall) {
					slog.Warn("reasoning overflow: aborting stream", "reasoning_bytes", reasoningByteLimit, "err", err)
					result.Status = StatusError
					result.Error = fmt.Errorf("agent: %w", err)
					result.Duration = time.Since(start)
					snapshotMessages()
					emitFinalSessionEnd(req.Callback, sessionID, &seq, req.Provider, &result, req.Metadata)
					return result, result.Error
				}

				// Overflow recovery: when the provider signals a context overflow
				// and we haven't already attempted overflow-compaction this turn,
				// run compaction and retry the provider call once. Only one attempt
				// per turn to prevent infinite loops.
				if IsContextOverflowError(err) && !overflowCompacted && req.Compactor != nil {
					overflowCompacted = true
					compacted, _, compErr := runCompaction()
					if compErr != nil {
						// ErrCompactionNoFit or other fatal compaction error.
						result.Status = StatusError
						result.Error = fmt.Errorf("agent: provider error: %w", err)
						result.Duration = time.Since(start)
						snapshotMessages()
						emitFinalSessionEnd(req.Callback, sessionID, &seq, req.Provider, &result, req.Metadata)
						return result, result.Error
					}
					if compacted {
						// Rebuild providerMessages from the freshly compacted history.
						providerMessages = append([]Message(nil), messages...)
						if req.SystemPrompt != "" {
							providerMessages = append([]Message{{
								Role:    RoleSystem,
								Content: req.SystemPrompt,
							}}, providerMessages...)
						}
						attempt = 0 // incremented to 1 by the loop header
						continue providerRetry
					}
					// Compactor ran but produced no shorter history — fall through to error.
				}

				if !IsTransientError(err) {
					result.Status = StatusError
					result.Error = fmt.Errorf("agent: provider error: %w", err)
					result.Duration = time.Since(start)
					snapshotMessages()
					emitFinalSessionEnd(req.Callback, sessionID, &seq, req.Provider, &result, req.Metadata)
					return result, result.Error
				}

				if attempt < maxProviderAttempts {
					delaySeconds := time.Duration(1 << min(attempt-1, 10))
					delay := time.Second * delaySeconds
					slog.Warn("provider error, retrying", "attempt", attempt, "err", err, "delay", delay)
					select {
					case <-ctx.Done():
						result.Status = StatusCancelled
						result.Duration = time.Since(start)
						snapshotMessages()
						emitFinalSessionEnd(req.Callback, sessionID, &seq, req.Provider, &result, req.Metadata)
						return result, nil
					case <-time.After(delay):
					}
					continue
				}

				result.Status = StatusError
				result.Error = fmt.Errorf("agent: provider error: %w", err)
				result.Duration = time.Since(start)
				snapshotMessages()
				emitFinalSessionEnd(req.Callback, sessionID, &seq, req.Provider, &result, req.Metadata)
				return result, result.Error
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
			emitFinalSessionEnd(req.Callback, sessionID, &seq, req.Provider, &result, req.Metadata)
			return result, nil
		}

		// Determine whether all tools in this batch are safe to run concurrently.
		allParallel := true
		for _, tc := range resp.ToolCalls {
			if tool, ok := toolMap[tc.Name]; !ok || !tool.Parallel() {
				allParallel = false
				break
			}
		}

		// toolResult holds the per-call output collected during (possibly parallel) execution.
		type toolResult struct {
			output      string
			toolErr     error
			toolDuration time.Duration
			log         ToolCallLog
			toolSpan    trace.Span
		}
		results := make([]toolResult, len(resp.ToolCalls))

		if allParallel && len(resp.ToolCalls) > 1 {
			// Run all tools concurrently; collect results in index order.
			var wg sync.WaitGroup
			wg.Add(len(resp.ToolCalls))
			for toolExecutionIndex, tc := range resp.ToolCalls {
				toolExecutionIndex, tc := toolExecutionIndex, tc // capture loop vars
				go func() {
					defer wg.Done()
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
					tool := toolMap[tc.Name] // safe: allParallel guarantees existence
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
					results[toolExecutionIndex] = toolResult{
						output:      output,
						toolErr:     toolErr,
						toolDuration: toolDuration,
						log:         log,
						toolSpan:    toolSpan,
					}
				}()
			}
			wg.Wait()
		} else {
			// Execute each tool call sequentially.
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
					// Unknown tool — return error to the model; no result slot needed.
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
					results[toolExecutionIndex] = toolResult{} // mark as handled
					continue
				}
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
				results[toolExecutionIndex] = toolResult{
					output:      output,
					toolErr:     toolErr,
					toolDuration: toolDuration,
					log:         log,
					toolSpan:    toolSpan,
				}
			}
		}

		// Merge results (in order) into messages and logs.
		for i, tc := range resp.ToolCalls {
			r := results[i]
			if r.toolSpan == nil {
				// Already handled inline (unknown tool in sequential path).
				continue
			}
			result.ToolCalls = append(result.ToolCalls, r.log)

			// Emit tool call event
			emitCallback(req.Callback, Event{
				SessionID: sessionID,
				Seq:       seq,
				Type:      EventToolCall,
				Timestamp: time.Now().UTC(),
				Data: mustMarshal(map[string]any{
					"tool":        tc.Name,
					"input":       tc.Arguments,
					"output":      truncateForLog(r.output, 10000),
					"duration_ms": r.toolDuration.Milliseconds(),
					"error":       r.log.Error,
				}),
			})
			seq++
			r.toolSpan.End()

			// Append tool result to conversation
			messages = append(messages, Message{
				Role:       RoleTool,
				Content:    r.output,
				ToolCallID: tc.ID,
			})
		}

		// Mid-iteration compaction check (after tool results)
		// This handles cases where large bash output increases token count.
		if _, _, compErr := runCompaction(); compErr != nil {
			result.Status = StatusError
			result.Error = compErr
			result.Duration = time.Since(start)
			snapshotMessages()
			emitFinalSessionEnd(req.Callback, sessionID, &seq, req.Provider, &result, req.Metadata)
			return result, compErr
		}

		// Detect identical consecutive tool-call turns and abort.
		fp := toolCallFingerprint(resp.ToolCalls)
		if fp == lastToolCallFingerprint {
			consecutiveToolCallCount++
		} else {
			consecutiveToolCallCount = 1
			lastToolCallFingerprint = fp
		}
		if consecutiveToolCallCount >= toolCallLoopLimit {
			slog.Warn("tool-call loop: identical calls repeated 3 times, aborting")
			result.Status = StatusError
			result.Error = ErrToolCallLoop
			result.Duration = time.Since(start)
			snapshotMessages()
			emitFinalSessionEnd(req.Callback, sessionID, &seq, req.Provider, &result, req.Metadata)
			return result, ErrToolCallLoop
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func estimateCompactionPrefixTokens(systemPrompt string) int {
	return estimateCompactionTextTokens(string(RoleSystem)) + estimateCompactionTextTokens(systemPrompt)
}

func estimateCompactionTextTokens(s string) int {
	return (len(s) + 3) / 4
}

func emitCallback(cb EventCallback, e Event) {
	if cb != nil {
		cb(e)
	}
}

func emitFinalSessionEnd(cb EventCallback, sessionID string, seq *int, provider Provider, result *Result, metadata map[string]string) {
	applyRoutingReport(provider, result)
	emitSessionEnd(cb, sessionID, seq, *result, metadata)
}

func emitSessionEnd(cb EventCallback, sessionID string, seq *int, result Result, metadata map[string]string) {
	errStr := ""
	if result.Error != nil {
		errStr = result.Error.Error()
	}
	data := map[string]any{
		"status":              result.Status,
		"output":              result.Output,
		"tokens":              result.Tokens,
		"duration_ms":         result.Duration.Milliseconds(),
		"model":               result.Model,
		"selected_provider":   result.SelectedProvider,
		"selected_route":      result.SelectedRoute,
		"requested_model":     result.RequestedModel,
		"requested_model_ref": result.RequestedModelRef,
		"resolved_model_ref":  result.ResolvedModelRef,
		"resolved_model":      result.ResolvedModel,
		"attempted_providers": result.AttemptedProviders,
		"failover_count":      result.FailoverCount,
		"metadata":            metadata,
		"error":               errStr,
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

func applyRoutingReport(provider Provider, result *Result) {
	reporter, ok := provider.(RoutingReporter)
	if !ok {
		return
	}
	report := reporter.RoutingReport()
	if report.SelectedProvider != "" {
		result.SelectedProvider = report.SelectedProvider
	}
	if report.SelectedRoute != "" {
		result.SelectedRoute = report.SelectedRoute
	}
	if len(report.AttemptedProviders) > 0 {
		result.AttemptedProviders = append([]string(nil), report.AttemptedProviders...)
	}
	result.FailoverCount = report.FailoverCount
}

func applyRoutingSpanAttributes(span trace.Span, result Result) {
	if span == nil {
		return
	}
	attrs := make([]attribute.KeyValue, 0, 7)
	attrs = appendStringAttr(attrs, telemetry.KeyProviderName, result.SelectedProvider)
	attrs = appendStringAttr(attrs, telemetry.KeyProviderRoute, result.SelectedRoute)
	attrs = appendStringAttr(attrs, telemetry.KeyRequestModel, result.RequestedModel)
	attrs = appendStringAttr(attrs, telemetry.KeyRequestedModelRef, result.RequestedModelRef)
	attrs = appendStringAttr(attrs, telemetry.KeyProviderModelResolved, result.ResolvedModel)
	if len(result.AttemptedProviders) > 0 {
		attrs = append(attrs, attribute.String(telemetry.KeyAttemptedProviders, strings.Join(result.AttemptedProviders, ",")))
	}
	attrs = append(attrs, attribute.Int(telemetry.KeyFailoverCount, result.FailoverCount))
	span.SetAttributes(attrs...)
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

// toolCallFingerprint returns a string that uniquely identifies a set of tool
// calls by name and arguments, in order. Used to detect identical consecutive turns.
func toolCallFingerprint(calls []ToolCall) string {
	parts := make([]string, len(calls))
	for i, c := range calls {
		parts[i] = c.Name + "\x00" + string(c.Arguments)
	}
	return strings.Join(parts, "\x01")
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
	attrs = append(attrs, attemptCostAttributes(resp.Attempt)...)
	span.SetAttributes(attrs...)
}

func attemptCostAttributes(attempt *AttemptMetadata) []attribute.KeyValue {
	if attempt == nil || attempt.Cost == nil || attempt.Cost.Amount == nil {
		return []attribute.KeyValue{attribute.String(telemetry.KeyCostSource, string(CostSourceUnknown))}
	}

	source := attempt.Cost.Source
	if source == "" || source == CostSourceUnknown {
		return []attribute.KeyValue{attribute.String(telemetry.KeyCostSource, string(CostSourceUnknown))}
	}

	attrs := []attribute.KeyValue{
		attribute.String(telemetry.KeyCostSource, string(source)),
		attribute.Float64(telemetry.KeyCostAmount, *attempt.Cost.Amount),
	}
	if attempt.Cost.Currency != "" {
		attrs = append(attrs, attribute.String(telemetry.KeyCostCurrency, attempt.Cost.Currency))
	}
	if attempt.Cost.InputAmount != nil {
		attrs = append(attrs, attribute.Float64(telemetry.KeyCostInputAmount, *attempt.Cost.InputAmount))
	}
	if attempt.Cost.OutputAmount != nil {
		attrs = append(attrs, attribute.Float64(telemetry.KeyCostOutputAmount, *attempt.Cost.OutputAmount))
	}
	if attempt.Cost.CacheReadAmount != nil {
		attrs = append(attrs, attribute.Float64(telemetry.KeyCostCacheReadAmount, *attempt.Cost.CacheReadAmount))
	}
	if attempt.Cost.CacheWriteAmount != nil {
		attrs = append(attrs, attribute.Float64(telemetry.KeyCostCacheWriteAmount, *attempt.Cost.CacheWriteAmount))
	}
	if attempt.Cost.ReasoningAmount != nil {
		attrs = append(attrs, attribute.Float64(telemetry.KeyCostReasoningAmount, *attempt.Cost.ReasoningAmount))
	}
	if attempt.Cost.PricingRef != "" {
		attrs = append(attrs, attribute.String(telemetry.KeyCostPricingRef, attempt.Cost.PricingRef))
	}
	if len(attempt.Cost.Raw) > 0 {
		attrs = append(attrs, attribute.String(telemetry.KeyCostRaw, string(attempt.Cost.Raw)))
	}
	return attrs
}

func applySessionCostAttributes(span trace.Span, observed, known bool, source string, amount float64, currency, pricingRef string, stable bool) {
	if span == nil || !observed {
		return
	}

	if !known {
		span.SetAttributes(attribute.String(telemetry.KeyCostSource, string(CostSourceUnknown)))
		return
	}

	attrs := []attribute.KeyValue{
		attribute.Float64(telemetry.KeyCostAmount, amount),
	}
	if stable && source != "" {
		attrs = append(attrs, attribute.String(telemetry.KeyCostSource, source))
	}
	if stable && currency != "" {
		attrs = append(attrs, attribute.String(telemetry.KeyCostCurrency, currency))
	}
	if stable && pricingRef != "" {
		attrs = append(attrs, attribute.String(telemetry.KeyCostPricingRef, pricingRef))
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
