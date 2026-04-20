package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"
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
		ID               string          `json:"id"`
		Type             string          `json:"type"`
		Text             string          `json:"text"`
		Content          json.RawMessage `json:"content"`
		Command          string          `json:"command"`
		AggregatedOutput string          `json:"aggregated_output"`
		ExitCode         *int            `json:"exit_code"`
		Status           string          `json:"status"`
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
//   - codex output/agent_message              -> EventTypeTextDelta
//   - codex item.started command_execution    -> EventTypeToolCall
//   - codex item.completed command_execution  -> EventTypeToolResult
//   - codex item.completed text item          -> EventTypeTextDelta
//   - codex turn.completed                    -> (no event; aggregate populated with usage)
//   - all other types                         -> skipped
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
		case "item.completed":
			if ev.Item.Type == "command_execution" {
				if err := emit(harnesses.EventTypeToolResult, harnesses.ToolResultData{
					ID:     codexToolID(ev.Item.ID),
					Output: ev.Item.AggregatedOutput,
					Error:  codexCommandError(ev.Item.Status, ev.Item.ExitCode),
				}); err != nil {
					return agg, err
				}
				continue
			}
			text := codexCompletedItemText(ev.Item.Text, ev.Item.Content)
			if text != "" {
				if err := emit(harnesses.EventTypeTextDelta, harnesses.TextDeltaData{Text: text}); err != nil {
					return agg, err
				}
				agg.FinalText = text
			}
		case "item.started":
			if ev.Item.Type == "command_execution" {
				input, err := codexCommandInput(ev.Item.Command)
				if err != nil {
					return agg, err
				}
				if err := emit(harnesses.EventTypeToolCall, harnesses.ToolCallData{
					ID:    codexToolID(ev.Item.ID),
					Name:  "command_execution",
					Input: input,
				}); err != nil {
					return agg, err
				}
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

func codexToolID(id string) string {
	if id != "" {
		return id
	}
	return "codex-command"
}

func codexCommandInput(command string) (json.RawMessage, error) {
	raw, err := json.Marshal(map[string]string{"command": command})
	return raw, err
}

func codexCommandError(status string, exitCode *int) string {
	if exitCode != nil && *exitCode != 0 {
		return "exit status " + strconv.Itoa(*exitCode)
	}
	if status != "" && status != "completed" {
		return status
	}
	return ""
}

func codexCompletedItemText(text string, content json.RawMessage) string {
	if text != "" {
		return text
	}
	if len(content) == 0 {
		return ""
	}
	var contentString string
	if err := json.Unmarshal(content, &contentString); err == nil {
		return contentString
	}
	var blocks []struct {
		Text    string `json:"text"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(content, &blocks); err != nil {
		return ""
	}
	var b strings.Builder
	for _, block := range blocks {
		if block.Text != "" {
			b.WriteString(block.Text)
		} else if block.Content != "" {
			b.WriteString(block.Content)
		}
	}
	return b.String()
}
