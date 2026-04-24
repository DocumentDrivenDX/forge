package main

import (
	"fmt"
	"time"

	"github.com/DocumentDrivenDX/agent"
	agentcore "github.com/DocumentDrivenDX/agent/internal/core"
	"github.com/DocumentDrivenDX/agent/internal/session"
)

type cliServiceSessionRecorder struct {
	dir    string
	logger *session.Logger
	sc     agent.ServiceConfig
}

func newCLIServiceSessionRecorder(dir string, sc agent.ServiceConfig) *cliServiceSessionRecorder {
	return &cliServiceSessionRecorder{dir: dir, sc: sc}
}

func (r *cliServiceSessionRecorder) Close() {
	if r.logger != nil {
		_ = r.logger.Close()
	}
}

func (r *cliServiceSessionRecorder) Record(req agent.ServiceExecuteRequest, selection providerSelection, decoded agent.ServiceDecodedEvent) {
	switch decoded.Type {
	case agent.ServiceEventTypeRoutingDecision:
		if decoded.RoutingDecision == nil || decoded.RoutingDecision.SessionID == "" || r.logger != nil {
			return
		}
		r.logger = session.NewLogger(r.dir, decoded.RoutingDecision.SessionID)
		r.logger.Emit(agentcore.EventSessionStart, session.SessionStartData{
			Provider:          r.providerLabel(valueOrFallback(decoded.RoutingDecision.Provider, selection.Provider)),
			Model:             valueOrFallback(decoded.RoutingDecision.Model, selection.ResolvedModel),
			SelectedProvider:  valueOrFallback(decoded.RoutingDecision.Provider, selection.Provider),
			SelectedRoute:     selection.Route,
			RequestedModel:    selection.RequestedModel,
			RequestedModelRef: selection.RequestedModelRef,
			ResolvedModelRef:  selection.ResolvedModelRef,
			ResolvedModel:     selection.ResolvedModel,
			Reasoning:         req.Reasoning,
			WorkDir:           req.WorkDir,
			MaxIterations:     req.MaxIterations,
			Prompt:            req.Prompt,
			SystemPrompt:      req.SystemPrompt,
			Metadata:          req.Metadata,
		})
	case agent.ServiceEventTypeFinal:
		if decoded.Final == nil || r.logger == nil {
			return
		}
		end := session.SessionEndData{
			Status:            cliServiceStatusToCoreStatus(decoded.Final.Status),
			Output:            decoded.Final.FinalText,
			Tokens:            cliTokenUsageToCore(decoded.Final.Usage),
			DurationMs:        decoded.Final.DurationMS,
			Model:             selection.ResolvedModel,
			SelectedProvider:  selection.Provider,
			SelectedRoute:     selection.Route,
			RequestedModel:    selection.RequestedModel,
			RequestedModelRef: selection.RequestedModelRef,
			ResolvedModelRef:  selection.ResolvedModelRef,
			ResolvedModel:     selection.ResolvedModel,
			Reasoning:         req.Reasoning,
			Metadata:          req.Metadata,
			Error:             decoded.Final.Error,
		}
		if decoded.Final.CostUSD > 0 {
			cost := decoded.Final.CostUSD
			end.CostUSD = &cost
		}
		if decoded.Final.RoutingActual != nil {
			end.Model = valueOrFallback(decoded.Final.RoutingActual.Model, end.Model)
			end.SelectedProvider = valueOrFallback(decoded.Final.RoutingActual.Provider, end.SelectedProvider)
			end.ResolvedModel = valueOrFallback(decoded.Final.RoutingActual.Model, end.ResolvedModel)
			end.AttemptedProviders = append([]string(nil), decoded.Final.RoutingActual.FallbackChainFired...)
			if len(end.AttemptedProviders) > 1 {
				end.FailoverCount = len(end.AttemptedProviders) - 1
			}
		}
		r.logger.Emit(agentcore.EventSessionEnd, end)
	}
}

func (r *cliServiceSessionRecorder) RecordSyntheticCancel(req agent.ServiceExecuteRequest, selection providerSelection, result cliExecutionResult) {
	if r.logger == nil {
		sessionID := result.SessionID
		if sessionID == "" {
			sessionID = fmt.Sprintf("svc-%d", time.Now().UnixNano())
		}
		r.logger = session.NewLogger(r.dir, sessionID)
		r.logger.Emit(agentcore.EventSessionStart, session.SessionStartData{
			Provider:          selection.Provider,
			Model:             selection.ResolvedModel,
			SelectedProvider:  selection.Provider,
			SelectedRoute:     selection.Route,
			RequestedModel:    selection.RequestedModel,
			RequestedModelRef: selection.RequestedModelRef,
			ResolvedModelRef:  selection.ResolvedModelRef,
			ResolvedModel:     selection.ResolvedModel,
			Reasoning:         req.Reasoning,
			WorkDir:           req.WorkDir,
			MaxIterations:     req.MaxIterations,
			Prompt:            req.Prompt,
			SystemPrompt:      req.SystemPrompt,
			Metadata:          req.Metadata,
		})
	}
	r.logger.Emit(agentcore.EventSessionEnd, session.SessionEndData{
		Status:            agentcore.StatusCancelled,
		Output:            result.Output,
		Tokens:            cliTokenUsageToCore(nil),
		DurationMs:        int64(result.Duration / time.Millisecond),
		Model:             result.Model,
		SelectedProvider:  result.SelectedProvider,
		SelectedRoute:     result.SelectedRoute,
		RequestedModel:    result.RequestedModel,
		RequestedModelRef: result.RequestedModelRef,
		ResolvedModelRef:  result.ResolvedModelRef,
		ResolvedModel:     result.ResolvedModel,
		Reasoning:         req.Reasoning,
		Metadata:          req.Metadata,
		Error:             result.Error,
	})
}

func cliServiceStatusToCoreStatus(status string) agentcore.Status {
	switch status {
	case "success":
		return agentcore.StatusSuccess
	case "iteration_limit":
		return agentcore.StatusIterationLimit
	case "cancelled":
		return agentcore.StatusCancelled
	default:
		return agentcore.StatusError
	}
}

func cliTokenUsageToCore(usage *agent.ServiceFinalUsage) agentcore.TokenUsage {
	if usage == nil {
		return agentcore.TokenUsage{}
	}
	return agentcore.TokenUsage{
		Input:      derefInt(usage.InputTokens),
		Output:     derefInt(usage.OutputTokens),
		CacheRead:  derefInt(usage.CacheReadTokens),
		CacheWrite: derefInt(usage.CacheWriteTokens),
		Total:      derefInt(usage.TotalTokens),
	}
}

func valueOrFallback(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

func (r *cliServiceSessionRecorder) providerLabel(name string) string {
	if r == nil || r.sc == nil || name == "" {
		return name
	}
	entry, ok := r.sc.Provider(name)
	if !ok || entry.Type == "" {
		return name
	}
	return entry.Type
}
