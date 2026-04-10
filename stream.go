package agent

import "context"
import "time"

// StreamDelta is a single chunk from a streaming response.
type StreamDelta struct {
	// ArrivedAt records when the provider produced the delta.
	// It is omitted from JSON and used only for local timing measurements.
	ArrivedAt time.Time `json:"-"`

	// Content is a text fragment (may be empty for tool call chunks).
	Content string `json:"content,omitempty"`

	// ToolCallID is set when a new tool call starts.
	ToolCallID string `json:"tool_call_id,omitempty"`
	// ToolCallName is set on the first delta of a tool call.
	ToolCallName string `json:"tool_call_name,omitempty"`
	// ToolCallArgs is a fragment of the tool call's JSON arguments.
	ToolCallArgs string `json:"tool_call_args,omitempty"`

	// Usage may be set on any delta, including before Done.
	// Providers can emit incremental usage updates; consumers should merge them.
	Usage *TokenUsage `json:"usage,omitempty"`

	// FinishReason is set on the final delta.
	FinishReason string `json:"finish_reason,omitempty"`

	// Model is set on the first or final delta.
	Model string `json:"model,omitempty"`

	// Attempt carries provider identity and attribution metadata when known.
	Attempt *AttemptMetadata `json:"attempt,omitempty"`

	// Done signals the end of the stream.
	Done bool `json:"done,omitempty"`

	// Err is set when the stream terminated with an error.
	// consumeStream returns this error and discards any partial content.
	Err error `json:"err,omitempty"`
}

// StreamingProvider extends Provider with streaming support.
// Providers that implement this interface will be used in streaming mode
// by the agent loop when Request.NoStream is false.
type StreamingProvider interface {
	Provider
	ChatStream(ctx context.Context, messages []Message, tools []ToolDef, opts Options) (<-chan StreamDelta, error)
}
