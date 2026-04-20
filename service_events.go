package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const (
	ServiceEventTypeTextDelta       = "text_delta"
	ServiceEventTypeToolCall        = "tool_call"
	ServiceEventTypeToolResult      = "tool_result"
	ServiceEventTypeCompaction      = "compaction"
	ServiceEventTypeRoutingDecision = "routing_decision"
	ServiceEventTypeStall           = "stall"
	ServiceEventTypeFinal           = "final"
)

type ServiceTextDeltaData struct {
	Text string `json:"text"`
}

type ServiceToolCallData struct {
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input,omitempty"`
}

type ServiceToolResultData struct {
	ID         string `json:"id"`
	Output     string `json:"output,omitempty"`
	Error      string `json:"error,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
}

type ServiceCompactionData struct {
	MessagesBefore int `json:"messages_before"`
	MessagesAfter  int `json:"messages_after"`
	TokensFreed    int `json:"tokens_freed"`
}

type ServiceRoutingDecisionData struct {
	Harness       string   `json:"harness"`
	Provider      string   `json:"provider,omitempty"`
	Model         string   `json:"model"`
	Reason        string   `json:"reason"`
	FallbackChain []string `json:"fallback_chain,omitempty"`
	SessionID     string   `json:"session_id,omitempty"`
}

type ServiceStallData struct {
	Reason string `json:"reason"`
	Count  int64  `json:"count"`
}

type ServiceFinalData struct {
	Status         string                `json:"status"`
	ExitCode       int                   `json:"exit_code"`
	Error          string                `json:"error,omitempty"`
	FinalText      string                `json:"final_text,omitempty"`
	DurationMS     int64                 `json:"duration_ms"`
	Usage          *ServiceFinalUsage    `json:"usage,omitempty"`
	CostUSD        float64               `json:"cost_usd,omitempty"`
	SessionLogPath string                `json:"session_log_path,omitempty"`
	RoutingActual  *ServiceRoutingActual `json:"routing_actual,omitempty"`
}

type ServiceFinalUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

type ServiceRoutingActual struct {
	Harness            string   `json:"harness"`
	Provider           string   `json:"provider,omitempty"`
	Model              string   `json:"model"`
	FallbackChainFired []string `json:"fallback_chain_fired,omitempty"`
}

// ServiceDecodedEvent is a typed view of one ServiceEvent. Exactly one payload
// pointer is non-nil for a known event type.
type ServiceDecodedEvent struct {
	Type     string
	Sequence int64
	Time     time.Time
	Metadata map[string]string

	TextDelta       *ServiceTextDeltaData
	ToolCall        *ServiceToolCallData
	ToolResult      *ServiceToolResultData
	Compaction      *ServiceCompactionData
	RoutingDecision *ServiceRoutingDecisionData
	Stall           *ServiceStallData
	Final           *ServiceFinalData
}

func DecodeServiceEvent(ev ServiceEvent) (ServiceDecodedEvent, error) {
	decoded := ServiceDecodedEvent{
		Type:     string(ev.Type),
		Sequence: ev.Sequence,
		Time:     ev.Time,
		Metadata: ev.Metadata,
	}
	switch string(ev.Type) {
	case ServiceEventTypeTextDelta:
		var payload ServiceTextDeltaData
		if err := decodeServicePayload(ev, &payload); err != nil {
			return decoded, err
		}
		decoded.TextDelta = &payload
	case ServiceEventTypeToolCall:
		var payload ServiceToolCallData
		if err := decodeServicePayload(ev, &payload); err != nil {
			return decoded, err
		}
		decoded.ToolCall = &payload
	case ServiceEventTypeToolResult:
		var payload ServiceToolResultData
		if err := decodeServicePayload(ev, &payload); err != nil {
			return decoded, err
		}
		decoded.ToolResult = &payload
	case ServiceEventTypeCompaction:
		var payload ServiceCompactionData
		if err := decodeServicePayload(ev, &payload); err != nil {
			return decoded, err
		}
		decoded.Compaction = &payload
	case ServiceEventTypeRoutingDecision:
		var payload ServiceRoutingDecisionData
		if err := decodeServicePayload(ev, &payload); err != nil {
			return decoded, err
		}
		decoded.RoutingDecision = &payload
	case ServiceEventTypeStall:
		var payload ServiceStallData
		if err := decodeServicePayload(ev, &payload); err != nil {
			return decoded, err
		}
		decoded.Stall = &payload
	case ServiceEventTypeFinal:
		var payload ServiceFinalData
		if err := decodeServicePayload(ev, &payload); err != nil {
			return decoded, err
		}
		decoded.Final = &payload
	default:
		return decoded, fmt.Errorf("decode service event %q: unknown type", ev.Type)
	}
	return decoded, nil
}

func decodeServicePayload(ev ServiceEvent, dst any) error {
	if len(ev.Data) == 0 {
		return fmt.Errorf("decode service event %q: empty data", ev.Type)
	}
	if err := json.Unmarshal(ev.Data, dst); err != nil {
		return fmt.Errorf("decode service event %q: %w", ev.Type, err)
	}
	return nil
}

// DrainExecuteResult is a typed aggregate of one Execute event stream.
type DrainExecuteResult struct {
	Events          []ServiceDecodedEvent
	TextDeltas      []ServiceTextDeltaData
	ToolCalls       []ServiceToolCallData
	ToolResults     []ServiceToolResultData
	Compactions     []ServiceCompactionData
	Stalls          []ServiceStallData
	RoutingDecision *ServiceRoutingDecisionData
	Final           *ServiceFinalData

	FinalStatus    string
	FinalText      string
	Usage          *ServiceFinalUsage
	CostUSD        float64
	SessionLogPath string
	RoutingActual  *ServiceRoutingActual
	TerminalError  string
}

func DrainExecute(ctx context.Context, events <-chan ServiceEvent) (*DrainExecuteResult, error) {
	result := &DrainExecuteResult{}
	for {
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		case ev, ok := <-events:
			if !ok {
				if result.Final == nil {
					return result, errors.New("execute event stream closed without final event")
				}
				return result, nil
			}
			decoded, err := DecodeServiceEvent(ev)
			if err != nil {
				return result, err
			}
			result.append(decoded)
		}
	}
}

func (r *DrainExecuteResult) append(ev ServiceDecodedEvent) {
	r.Events = append(r.Events, ev)
	switch {
	case ev.TextDelta != nil:
		r.TextDeltas = append(r.TextDeltas, *ev.TextDelta)
	case ev.ToolCall != nil:
		r.ToolCalls = append(r.ToolCalls, *ev.ToolCall)
	case ev.ToolResult != nil:
		r.ToolResults = append(r.ToolResults, *ev.ToolResult)
	case ev.Compaction != nil:
		r.Compactions = append(r.Compactions, *ev.Compaction)
	case ev.RoutingDecision != nil:
		r.RoutingDecision = ev.RoutingDecision
	case ev.Stall != nil:
		r.Stalls = append(r.Stalls, *ev.Stall)
	case ev.Final != nil:
		r.Final = ev.Final
		r.FinalStatus = ev.Final.Status
		r.FinalText = ev.Final.FinalText
		r.Usage = ev.Final.Usage
		r.CostUSD = ev.Final.CostUSD
		r.SessionLogPath = ev.Final.SessionLogPath
		r.RoutingActual = ev.Final.RoutingActual
		r.TerminalError = ev.Final.Error
	}
}
