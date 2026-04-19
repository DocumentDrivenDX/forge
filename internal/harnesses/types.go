package harnesses

import (
	"context"
	"encoding/json"
	"time"
)

// AccountInfo captures provider account metadata from local auth files.
type AccountInfo struct {
	Email    string `json:"email,omitempty"`
	PlanType string `json:"plan_type,omitempty"`
	OrgName  string `json:"org_name,omitempty"`
}

// QuotaWindow captures one quota window (e.g. 5h, weekly, model-specific).
type QuotaWindow struct {
	Name          string  `json:"name"`               // e.g. "5h", "7d", "spark"
	LimitID       string  `json:"limit_id,omitempty"` // provider limit_id
	WindowMinutes int     `json:"window_minutes"`
	UsedPercent   float64 `json:"used_percent"`
	ResetsAt      string  `json:"resets_at,omitempty"`      // human-readable
	ResetsAtUnix  int64   `json:"resets_at_unix,omitempty"` // unix timestamp
	State         string  `json:"state"`
}

// QuotaStateFromUsedPercent maps a usage percentage to a quota state string.
func QuotaStateFromUsedPercent(usedPercent int) string {
	if usedPercent >= 95 {
		return "blocked"
	}
	if usedPercent >= 0 {
		return "ok"
	}
	return "unknown"
}

// EventType identifies the kind of event a harness emits during execution.
//
// The set is the closed union defined by CONTRACT-003 ("Event JSON shapes"):
// every backend (native + subprocess) emits these identically so the agent
// loop can multiplex them onto a single channel.
type EventType string

const (
	EventTypeTextDelta       EventType = "text_delta"
	EventTypeToolCall        EventType = "tool_call"
	EventTypeToolResult      EventType = "tool_result"
	EventTypeCompaction      EventType = "compaction"
	EventTypeRoutingDecision EventType = "routing_decision"
	EventTypeStall           EventType = "stall"
	EventTypeFinal           EventType = "final"
)

// Event is the structured event a harness emits during Execute. It mirrors
// the shape defined in CONTRACT-003 §"Event JSON shapes". The Data field is
// a JSON-encoded payload whose schema is determined by Type.
type Event struct {
	Type     EventType         `json:"type"`
	Sequence int64             `json:"sequence"`
	Time     time.Time         `json:"time"`
	Metadata map[string]string `json:"metadata,omitempty"`
	Data     json.RawMessage   `json:"data"`
}

// TextDeltaData is the payload for type=text_delta events.
type TextDeltaData struct {
	Text string `json:"text"`
}

// ToolCallData is the payload for type=tool_call events.
type ToolCallData struct {
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input,omitempty"`
}

// ToolResultData is the payload for type=tool_result events.
type ToolResultData struct {
	ID         string `json:"id"`
	Output     string `json:"output,omitempty"`
	Error      string `json:"error,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
}

// FinalData is the payload for type=final events.
type FinalData struct {
	Status         string            `json:"status"` // success|failed|stalled|timed_out|cancelled
	ExitCode       int               `json:"exit_code"`
	Error          string            `json:"error,omitempty"`
	DurationMS     int64             `json:"duration_ms"`
	Usage          *FinalUsage       `json:"usage,omitempty"`
	CostUSD        float64           `json:"cost_usd,omitempty"`
	SessionLogPath string            `json:"session_log_path,omitempty"`
	RoutingActual  *RoutingActual    `json:"routing_actual,omitempty"`
	Extra          map[string]string `json:"-"`
}

// FinalUsage carries token totals on a final event.
type FinalUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

// RoutingActual captures the resolved fallback chain on a final event.
type RoutingActual struct {
	Harness            string   `json:"harness"`
	Provider           string   `json:"provider,omitempty"`
	Model              string   `json:"model"`
	FallbackChainFired []string `json:"fallback_chain_fired,omitempty"`
}

// HarnessInfo describes a registered harness. Mirrors the public
// HarnessInfo type defined in CONTRACT-003. Internal callers use this to
// implement the public ListHarnesses surface without re-declaring the shape.
type HarnessInfo struct {
	Name                 string
	Type                 string // "native" | "subprocess"
	Available            bool
	Path                 string
	Error                string
	IsLocal              bool
	IsSubscription       bool
	ExactPinSupport      bool
	SupportedPermissions []string
	SupportedReasoning   []string
	CostClass            string
}

// ExecuteRequest is the internal request carried into Harness.Execute. It
// is intentionally narrower than the public ExecuteRequest in CONTRACT-003:
// the agent's routing layer is expected to resolve provider/model/reasoning
// /permissions/timeouts before invoking a harness, so the harness sees a
// concrete, ready-to-run request.
type ExecuteRequest struct {
	// Prompt is the resolved user prompt sent to the model.
	Prompt string

	// SystemPrompt is the resolved system prompt; empty means harness default.
	SystemPrompt string

	// Provider is the resolved provider identifier when applicable. May be
	// empty for harnesses that have no provider concept (e.g. claude CLI).
	Provider string

	// Model is the resolved model identifier; empty means harness default.
	Model string

	// WorkDir is the working directory for tool operations. Required when
	// the chosen harness uses tools.
	WorkDir string

	// Permissions is "safe" | "supervised" | "unrestricted". Empty defaults to "safe".
	Permissions string

	// Temperature is the model sampling temperature requested by the caller.
	// Harness adapters may ignore it when their CLI has no equivalent control.
	Temperature float32

	// Seed is the requested sampling seed. Zero means unset/provider chooses.
	// Harness adapters may ignore it when their CLI has no equivalent control.
	Seed int64

	// Reasoning is the normalized public reasoning scalar. Empty/off means no
	// adapter flag should be emitted.
	Reasoning string

	// Timeout is the wall-clock cap for the entire request. 0 disables.
	Timeout time.Duration

	// IdleTimeout is the streaming-quiet cap. 0 uses harness default.
	IdleTimeout time.Duration

	// SessionLogDir overrides the per-run session-log directory; harness
	// uses this to direct progress traces into a per-bundle evidence dir.
	SessionLogDir string

	// SessionID is a stable identifier for the run, used in progress log
	// filenames and event metadata. Empty means the harness generates one.
	SessionID string

	// Metadata is echoed back into Event.Metadata (e.g. bead_id, attempt_id).
	Metadata map[string]string
}

// Harness is the internal contract every harness implementation in
// internal/harnesses/<name> satisfies. It is the minimal surface the agent
// dispatcher needs to route a resolved request into a backend.
//
// A Harness is responsible for emitting events on the returned channel until
// execution completes; the channel MUST be closed after the final event so
// downstream consumers can detect end-of-stream. The final event is always
// of type EventTypeFinal.
type Harness interface {
	// Info returns identity + capability metadata for this harness.
	Info() HarnessInfo

	// HealthCheck triggers a fresh probe (binary present, auth ok, etc.)
	// and returns nil if the harness is ready to execute.
	HealthCheck(ctx context.Context) error

	// Execute runs one resolved request. Events stream on the returned
	// channel; a single final event closes the stream. The first error
	// return is reserved for setup failures (binary missing, etc.) — once
	// the channel is returned, all per-run failures are reported via a
	// final event with Status != "success".
	Execute(ctx context.Context, req ExecuteRequest) (<-chan Event, error)
}
