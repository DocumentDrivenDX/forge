package codex

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

// codexEvent is a minimal view of a codex exec --json JSONL event.
// Codex emits one JSON object per line on stdout.
//
// Observed event types (from DDx ExtractUsage and ExtractOutput):
//
//	type=output, item.type=agent_message, item.text=<response text>
//	type=turn.completed, usage.input_tokens=N, usage.output_tokens=N
//	(other types are passed through silently)
type codexEvent struct {
	Type string `json:"type"`
	Item struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"item"`
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// streamAggregate captures running totals from the codex stream.
type streamAggregate struct {
	FinalText    string
	InputTokens  int
	OutputTokens int
}

// parseCodexStream reads newline-delimited codex exec --json events from r
// and emits harness Events on out. Mapping per CONTRACT-003:
//
//   - codex output/agent_message -> EventTypeTextDelta
//   - codex turn.completed       -> (no event; aggregate populated with usage)
//   - all other types            -> skipped
func parseCodexStream(ctx context.Context, r io.Reader, out chan<- harnesses.Event, metadata map[string]string, seq *int64) (*streamAggregate, error) {
	agg := &streamAggregate{}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 256*1024), 16*1024*1024)

	emit := func(t harnesses.EventType, data any) error {
		raw, err := json.Marshal(data)
		if err != nil {
			return err
		}
		ev := harnesses.Event{
			Type:     t,
			Sequence: *seq,
			Time:     time.Now().UTC(),
			Metadata: metadata,
			Data:     raw,
		}
		*seq++
		select {
		case out <- ev:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return agg, ctx.Err()
		default:
		}

		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var ev codexEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			// Non-JSON line — skip silently.
			continue
		}

		switch ev.Type {
		case "output":
			if ev.Item.Type == "agent_message" && ev.Item.Text != "" {
				if err := emit(harnesses.EventTypeTextDelta, harnesses.TextDeltaData{Text: ev.Item.Text}); err != nil {
					return agg, err
				}
				agg.FinalText = ev.Item.Text
			}
		case "turn.completed":
			if ev.Usage.InputTokens > 0 {
				agg.InputTokens = ev.Usage.InputTokens
			}
			if ev.Usage.OutputTokens > 0 {
				agg.OutputTokens = ev.Usage.OutputTokens
			}
		}
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
		return agg, err
	}
	return agg, nil
}
