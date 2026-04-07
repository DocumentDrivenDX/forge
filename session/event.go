package session

import (
	"encoding/json"
	"time"

	"github.com/anthropics/forge"
)

// SessionStartData is the data payload for a session.start event.
type SessionStartData struct {
	Provider      string            `json:"provider"`
	Model         string            `json:"model"`
	WorkDir       string            `json:"work_dir"`
	MaxIterations int               `json:"max_iterations"`
	Prompt        string            `json:"prompt"`
	SystemPrompt  string            `json:"system_prompt,omitempty"`
	Metadata      map[string]string `json:"metadata,omitempty"`
}

// LLMRequestData is the data payload for an llm.request event.
type LLMRequestData struct {
	Messages []forge.Message `json:"messages"`
	Tools    []forge.ToolDef `json:"tools,omitempty"`
}

// LLMResponseData is the data payload for an llm.response event.
type LLMResponseData struct {
	Content      string           `json:"content,omitempty"`
	ToolCalls    []forge.ToolCall `json:"tool_calls,omitempty"`
	Usage        forge.TokenUsage `json:"usage"`
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
	Status     forge.Status     `json:"status"`
	Output     string           `json:"output"`
	Tokens     forge.TokenUsage `json:"tokens"`
	CostUSD    float64          `json:"cost_usd"`
	DurationMs int64            `json:"duration_ms"`
	Error      string           `json:"error,omitempty"`
}

// NewEvent creates an Event with the given type and data, auto-assigning
// the timestamp.
func NewEvent(sessionID string, seq int, eventType forge.EventType, data any) forge.Event {
	raw, _ := json.Marshal(data)
	return forge.Event{
		SessionID: sessionID,
		Seq:       seq,
		Type:      eventType,
		Timestamp: time.Now().UTC(),
		Data:      raw,
	}
}
