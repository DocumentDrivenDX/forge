package session

import (
	"encoding/json"
	"time"

	"github.com/DocumentDrivenDX/agent"
)

// SessionStartData is the data payload for a session.start event.
type SessionStartData struct {
	Provider           string            `json:"provider"`
	Model              string            `json:"model"`
	SelectedProvider   string            `json:"selected_provider,omitempty"`
	SelectedRoute      string            `json:"selected_route,omitempty"`
	RequestedModel     string            `json:"requested_model,omitempty"`
	RequestedModelRef  string            `json:"requested_model_ref,omitempty"`
	ResolvedModelRef   string            `json:"resolved_model_ref,omitempty"`
	ResolvedModel      string            `json:"resolved_model,omitempty"`
	AttemptedProviders []string          `json:"attempted_providers,omitempty"`
	FailoverCount      int               `json:"failover_count,omitempty"`
	WorkDir            string            `json:"work_dir"`
	MaxIterations      int               `json:"max_iterations"`
	Prompt             string            `json:"prompt"`
	SystemPrompt       string            `json:"system_prompt,omitempty"`
	Metadata           map[string]string `json:"metadata,omitempty"`
}

// LLMRequestData is the data payload for an llm.request event.
type LLMRequestData struct {
	Messages []agent.Message `json:"messages"`
	Tools    []agent.ToolDef `json:"tools,omitempty"`
}

// LLMResponseData is the data payload for an llm.response event.
type LLMResponseData struct {
	Content      string           `json:"content,omitempty"`
	ToolCalls    []agent.ToolCall `json:"tool_calls,omitempty"`
	Usage        agent.TokenUsage `json:"usage"`
	CostUSD      float64          `json:"cost_usd"`
	LatencyMs    int64            `json:"latency_ms"`
	Model        string           `json:"model"`
	FinishReason string           `json:"finish_reason"`
}

// ToolCallData is the data payload for a tool.call event.
type ToolCallData struct {
	Tool       string          `json:"tool"`
	Input      json.RawMessage `json:"input"`
	Output     string          `json:"output"`
	DurationMs int64           `json:"duration_ms"`
	Error      string          `json:"error,omitempty"`
}

// SessionEndData is the data payload for a session.end event.
type SessionEndData struct {
	Status             agent.Status      `json:"status"`
	Output             string            `json:"output"`
	Tokens             agent.TokenUsage  `json:"tokens"`
	CostUSD            *float64          `json:"cost_usd,omitempty"`
	DurationMs         int64             `json:"duration_ms"`
	Model              string            `json:"model,omitempty"`
	SelectedProvider   string            `json:"selected_provider,omitempty"`
	SelectedRoute      string            `json:"selected_route,omitempty"`
	RequestedModel     string            `json:"requested_model,omitempty"`
	RequestedModelRef  string            `json:"requested_model_ref,omitempty"`
	ResolvedModelRef   string            `json:"resolved_model_ref,omitempty"`
	ResolvedModel      string            `json:"resolved_model,omitempty"`
	AttemptedProviders []string          `json:"attempted_providers,omitempty"`
	FailoverCount      int               `json:"failover_count,omitempty"`
	Metadata           map[string]string `json:"metadata,omitempty"`
	Error              string            `json:"error,omitempty"`
}

// NewEvent creates an Event with the given type and data, auto-assigning
// the timestamp.
func NewEvent(sessionID string, seq int, eventType agent.EventType, data any) agent.Event {
	raw, _ := json.Marshal(data)
	return agent.Event{
		SessionID: sessionID,
		Seq:       seq,
		Type:      eventType,
		Timestamp: time.Now().UTC(),
		Data:      raw,
	}
}
