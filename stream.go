package agent

import "context"

// StreamDelta is a single chunk from a streaming response.
type StreamDelta struct {
	// Content is a text fragment (may be empty for tool call chunks).
	Content string `json:"content,omitempty"`

	// ToolCallID is set when a new tool call starts.
	ToolCallID string `json:"tool_call_id,omitempty"`
	// ToolCallName is set on the first delta of a tool call.
	ToolCallName string `json:"tool_call_name,omitempty"`
	// ToolCallArgs is a fragment of the tool call's JSON arguments.
	ToolCallArgs string `json:"tool_call_args,omitempty"`

	// Usage is set on the final delta (when Done is true).
	Usage *TokenUsage `json:"usage,omitempty"`

	// FinishReason is set on the final delta.
	FinishReason string `json:"finish_reason,omitempty"`

	// Model is set on the first or final delta.
	Model string `json:"model,omitempty"`

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
