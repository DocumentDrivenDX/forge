// Package agent provides an embeddable Go agent runtime — a tool-calling LLM
// loop with file read/write, shell execution, and structured I/O.
package agent

import (
	"context"
	"encoding/json"
	"time"

	"github.com/DocumentDrivenDX/agent/telemetry"
)

// Status represents the outcome of an agent run.
type Status string

const (
	StatusSuccess        Status = "success"
	StatusIterationLimit Status = "iteration_limit"
	StatusCancelled      Status = "cancelled"
	StatusError          Status = "error"
)

// Role identifies the sender of a message in the conversation.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// TokenUsage tracks input and output token counts.
type TokenUsage struct {
	Input      int `json:"input"`
	Output     int `json:"output"`
	CacheRead  int `json:"cache_read,omitempty"`
	CacheWrite int `json:"cache_write,omitempty"`
	Total      int `json:"total"`
}

// Add accumulates token counts from another TokenUsage.
func (u *TokenUsage) Add(other TokenUsage) {
	u.Input += other.Input
	u.Output += other.Output
	u.CacheRead += other.CacheRead
	u.CacheWrite += other.CacheWrite
	u.Total += other.Total
}

// ToolCall represents a tool invocation requested by the model.
type ToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// Message is a single message in the conversation history.
type Message struct {
	Role       Role       `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

// ToolDef describes a tool for the LLM provider.
type ToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"` // JSON Schema
}

// Options configures a single provider Chat call.
type Options struct {
	Model       string   `json:"model,omitempty"`
	Temperature *float64 `json:"temperature,omitempty"`
	MaxTokens   int      `json:"max_tokens,omitempty"`
	Stop        []string `json:"stop,omitempty"`
}

// Response is the result of a single provider Chat call.
type Response struct {
	Content      string           `json:"content"`
	ToolCalls    []ToolCall       `json:"tool_calls,omitempty"`
	Usage        TokenUsage       `json:"usage"`
	Model        string           `json:"model"`
	FinishReason string           `json:"finish_reason"`
	Attempt      *AttemptMetadata `json:"attempt,omitempty"`
}

// Provider is the interface that LLM backends implement.
// Define it in the consuming package per Go idiom.
type Provider interface {
	Chat(ctx context.Context, messages []Message, tools []ToolDef, opts Options) (Response, error)
}

// Tool is the interface that agent tools implement.
type Tool interface {
	// Name returns the tool's identifier.
	Name() string
	// Description returns a human-readable description for the LLM.
	Description() string
	// Schema returns the JSON Schema for the tool's parameters.
	Schema() json.RawMessage
	// Execute runs the tool with the given parameters and returns the result.
	Execute(ctx context.Context, params json.RawMessage) (string, error)
}

// ToolCallLog records one tool execution during an agent run.
type ToolCallLog struct {
	Tool     string          `json:"tool"`
	Input    json.RawMessage `json:"input"`
	Output   string          `json:"output"`
	Duration time.Duration   `json:"duration_ms"`
	Error    string          `json:"error,omitempty"`
}

// EventType identifies the kind of event emitted during an agent run.
type EventType string

const (
	EventSessionStart    EventType = "session.start"
	EventLLMRequest      EventType = "llm.request"
	EventLLMResponse     EventType = "llm.response"
	EventToolCall        EventType = "tool.call"
	EventSessionEnd      EventType = "session.end"
	EventLLMDelta        EventType = "llm.delta"
	EventCompactionStart EventType = "compaction.start"
	EventCompactionEnd   EventType = "compaction.end"
)

// Event is a structured event emitted during an agent run.
type Event struct {
	SessionID string          `json:"session_id"`
	Seq       int             `json:"seq"`
	Type      EventType       `json:"type"`
	Timestamp time.Time       `json:"ts"`
	Data      json.RawMessage `json:"data"`
}

// EventCallback receives events during an agent run. The session logger is
// one implementation; callers can also use it for progress reporting.
type EventCallback func(Event)

// CostSource identifies where the recorded cost originated.
type CostSource string

const (
	CostSourceProviderReported CostSource = "provider_reported"
	CostSourceGatewayReported  CostSource = "gateway_reported"
	CostSourceConfigured       CostSource = "configured"
	CostSourceUnknown          CostSource = "unknown"
)

// CostAttribution captures the provenance of the cost associated with one
// provider attempt.
type CostAttribution struct {
	Source           CostSource      `json:"source,omitempty"`
	Currency         string          `json:"currency,omitempty"`
	Amount           *float64        `json:"amount,omitempty"`
	InputAmount      *float64        `json:"input_amount,omitempty"`
	OutputAmount     *float64        `json:"output_amount,omitempty"`
	CacheReadAmount  *float64        `json:"cache_read_amount,omitempty"`
	CacheWriteAmount *float64        `json:"cache_write_amount,omitempty"`
	ReasoningAmount  *float64        `json:"reasoning_amount,omitempty"`
	PricingRef       string          `json:"pricing_ref,omitempty"`
	Raw              json.RawMessage `json:"raw,omitempty"`
}

// TimingBreakdown captures optional provider timing windows for one attempt.
type TimingBreakdown struct {
	FirstToken *time.Duration `json:"first_token,omitempty"`
	Queue      *time.Duration `json:"queue,omitempty"`
	Prefill    *time.Duration `json:"prefill,omitempty"`
	Generation *time.Duration `json:"generation,omitempty"`
	CacheRead  *time.Duration `json:"cache_read,omitempty"`
	CacheWrite *time.Duration `json:"cache_write,omitempty"`
}

// AttemptMetadata captures the structured identity and attribution data for a
// single provider attempt.
type AttemptMetadata struct {
	AttemptIndex   int              `json:"attempt_index,omitempty"`
	ProviderName   string           `json:"provider_name,omitempty"`
	ProviderSystem string           `json:"provider_system,omitempty"`
	Route          string           `json:"route,omitempty"`
	ServerAddress  string           `json:"server_address,omitempty"`
	ServerPort     int              `json:"server_port,omitempty"`
	RequestedModel string           `json:"requested_model,omitempty"`
	ResponseModel  string           `json:"response_model,omitempty"`
	ResolvedModel  string           `json:"resolved_model,omitempty"`
	Cost           *CostAttribution `json:"cost,omitempty"`
	Timing         *TimingBreakdown `json:"timing,omitempty"`
}

// Request configures a single agent run.
type Request struct {
	// Prompt is the user's task description.
	Prompt string

	// SystemPrompt is prepended to the conversation as a system message.
	SystemPrompt string

	// History carries prior conversation messages into this run.
	// Use Result.Messages from a previous Run call to continue a session.
	History []Message

	// Provider is the configured LLM backend.
	Provider Provider

	// Tools are the tools available to the agent.
	Tools []Tool

	// MaxIterations limits the number of tool-call rounds. Zero means no limit.
	MaxIterations int

	// WorkDir is the working directory for file operations and bash commands.
	WorkDir string

	// Callback receives events in real time. May be nil.
	Callback EventCallback

	// Metadata is correlation data (e.g., bead_id) stored on session events.
	Metadata map[string]string

	// NoStream disables streaming even if the provider supports it.
	NoStream bool

	// Telemetry carries the runtime telemetry implementation. If nil, the
	// agent loop falls back to a no-op runtime.
	Telemetry telemetry.Telemetry

	// Compactor is called before each agent loop iteration (and after tool
	// results). If non-nil, it may compact the message history to fit within
	// the context window. Returns the (possibly compacted) messages and result.
	// The compaction package provides a ready-made implementation.
	Compactor func(ctx context.Context, messages []Message, provider Provider, toolCalls []ToolCallLog) ([]Message, *CompactionResult, error)
}

// Result is the outcome of an agent run.
type Result struct {
	// Status indicates whether the run succeeded.
	Status Status `json:"status"`

	// Output is the final text response from the model.
	Output string `json:"output"`

	// ToolCalls logs every tool execution during the run.
	ToolCalls []ToolCallLog `json:"tool_calls,omitempty"`

	// Messages is the conversation history for this run, excluding the
	// system prompt. Feed this back into Request.History to continue a session.
	Messages []Message `json:"messages,omitempty"`

	// Tokens is the accumulated token usage across all iterations.
	Tokens TokenUsage `json:"tokens"`

	// Duration is the total wall-clock time of the run.
	Duration time.Duration `json:"duration_ms"`

	// CostUSD is the estimated cost. -1 means unknown (model not in pricing table).
	// 0 means free (local model with $0 pricing entry).
	CostUSD float64 `json:"cost_usd"`

	// Model is the model that was used.
	Model string `json:"model"`

	// Error is non-nil when Status is StatusError.
	Error error `json:"-"`

	// SessionID identifies the session log for this run.
	SessionID string `json:"session_id"`
}

// CompactionResult holds the output of a compaction pass.
type CompactionResult struct {
	// Summary is the generated summary text.
	Summary string `json:"summary"`
	// FileOps tracks files read and modified.
	FileOps map[string]any `json:"file_ops,omitempty"`
	// TokensBefore is the estimated token count before compaction.
	TokensBefore int `json:"tokens_before"`
	// TokensAfter is the estimated token count after compaction.
	TokensAfter int `json:"tokens_after"`
	// Warning is a degradation warning, if any.
	Warning string `json:"warning,omitempty"`
}
