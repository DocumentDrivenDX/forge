package pi

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/DocumentDrivenDX/agent/internal/harnesses"
)

// piEvent is a minimal view of a pi --mode json --print JSONL event.
// From DDx ExtractUsage("pi"):
//
//	type=text_end or text_delta: message.usage.{input,output,cost.total}
//	                             partial.usage.{input,output,cost.total}
//
// The last line with a "response" field carries the final answer text
// (per DDx extractOutputPiGemini).
type piEvent struct {
	Type     string `json:"type"`
	Response string `json:"response,omitempty"`
	Message  struct {
		Usage struct {
			Input  int `json:"input"`
			Output int `json:"output"`
			Cost   struct {
				Total float64 `json:"total"`
			} `json:"cost"`
		} `json:"usage"`
	} `json:"message"`
	Partial struct {
		Usage struct {
			Input  int `json:"input"`
			Output int `json:"output"`
			Cost   struct {
				Total float64 `json:"total"`
			} `json:"cost"`
		} `json:"usage"`
	} `json:"partial"`
}

// streamAggregate captures running totals from the pi stream.
type streamAggregate struct {
	FinalText    string
	InputTokens  int
	OutputTokens int
	CostUSD      float64
}

// parsePiStream reads newline-delimited pi --mode json events from r and
// emits harness Events on out. Mapping per CONTRACT-003:
//
//   - pi events with response text -> EventTypeTextDelta (last response line)
//   - pi events with usage         -> aggregate (no event; drives final Usage)
//
// Pi doesn't emit real-time tool_call/tool_result events in JSONL mode,
// so only text_delta events are emitted during the stream. Usage is captured
// from the last line with usage fields, per DDx behavior.
func parsePiStream(ctx context.Context, r io.Reader, out chan<- harnesses.Event, metadata map[string]string, seq *int64) (*streamAggregate, error) {
	agg := &streamAggregate{}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 256*1024), 16*1024*1024)

	// Collect all lines; we need to scan backwards for usage and emit
	// response text as we go.
	var lines []string
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return agg, ctx.Err()
		default:
		}
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			lines = append(lines, line)
		}
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
		return agg, err
	}

	// Scan backwards for usage (per DDx ExtractUsage("pi")).
	for i := len(lines) - 1; i >= 0; i-- {
		var ev piEvent
		if err := json.Unmarshal([]byte(lines[i]), &ev); err != nil {
			continue
		}
		if ev.Message.Usage.Input > 0 || ev.Message.Usage.Output > 0 {
			agg.InputTokens = ev.Message.Usage.Input
			agg.OutputTokens = ev.Message.Usage.Output
			agg.CostUSD = ev.Message.Usage.Cost.Total
			break
		}
		if ev.Partial.Usage.Input > 0 || ev.Partial.Usage.Output > 0 {
			agg.InputTokens = ev.Partial.Usage.Input
			agg.OutputTokens = ev.Partial.Usage.Output
			agg.CostUSD = ev.Partial.Usage.Cost.Total
			break
		}
	}

	// Extract final response text (last line with a non-empty "response" field,
	// per DDx extractOutputPiGemini). If no structured response, concatenate
	// any text delta lines.
	for i := len(lines) - 1; i >= 0; i-- {
		var ev piEvent
		if err := json.Unmarshal([]byte(lines[i]), &ev); err != nil {
			continue
		}
		if ev.Response != "" {
			agg.FinalText = ev.Response
			break
		}
	}
	if agg.FinalText == "" && len(lines) > 0 {
		// Fallback: use all lines joined as the output.
		agg.FinalText = strings.Join(lines, "\n")
	}

	// Emit a single text_delta with the final response.
	if agg.FinalText != "" {
		raw, err := json.Marshal(harnesses.TextDeltaData{Text: agg.FinalText})
		if err != nil {
			return agg, err
		}
		ev := harnesses.Event{
			Type:     harnesses.EventTypeTextDelta,
			Sequence: *seq,
			Time:     time.Now().UTC(),
			Metadata: metadata,
			Data:     raw,
		}
		*seq++
		select {
		case out <- ev:
		case <-ctx.Done():
			return agg, ctx.Err()
		}
	}

	return agg, nil
}
