package session

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/DocumentDrivenDX/agent"
)

// Replay reads a session log and renders a human-readable conversation.
func Replay(path string, w io.Writer) error {
	events, err := ReadEvents(path)
	if err != nil {
		return fmt.Errorf("replay: %w", err)
	}

	for _, e := range events {
		switch e.Type {
		case agent.EventLLMRequest:
			var data LLMRequestData
			if err := json.Unmarshal(e.Data, &data); err != nil {
				continue
			}
			fmt.Fprintf(w, "\n[LLM Request] (%d messages, %d tools)\n", len(data.Messages), len(data.Tools))
			for _, m := range data.Messages {
				switch m.Role {
				case agent.RoleSystem:
					fmt.Fprintf(w, "  [system] %s\n", m.Content)
				case agent.RoleUser:
					fmt.Fprintf(w, "  [user] %s\n", m.Content)
				case agent.RoleAssistant:
					if m.Content != "" {
						fmt.Fprintf(w, "  [assistant] %s\n", m.Content)
					}
					for _, tc := range m.ToolCalls {
						fmt.Fprintf(w, "  [assistant tool_call] %s(%s)\n", tc.Name, compactJSON(tc.Arguments))
					}
				case agent.RoleTool:
					content := m.Content
					if len(content) > 200 {
						content = content[:200] + "...[truncated]"
					}
					fmt.Fprintf(w, "  [tool result] %s\n", strings.ReplaceAll(content, "\n", "\n              "))
				}
			}

		case agent.EventSessionStart:
			var data SessionStartData
			if err := json.Unmarshal(e.Data, &data); err != nil {
				continue
			}
			fmt.Fprintf(w, "=== Session %s ===\n", e.SessionID)
			fmt.Fprintf(w, "Time: %s\n", e.Timestamp.Format("2006-01-02 15:04:05 UTC"))
			fmt.Fprintf(w, "Provider: %s | Model: %s\n", data.Provider, data.Model)
			fmt.Fprintf(w, "Max iterations: %d | Work dir: %s\n", data.MaxIterations, data.WorkDir)
			if data.SystemPrompt != "" {
				fmt.Fprintf(w, "\n[System]\n%s\n", data.SystemPrompt)
			}
			fmt.Fprintf(w, "\n[User]\n%s\n", data.Prompt)

		case agent.EventLLMResponse:
			var data LLMResponseData
			if err := json.Unmarshal(e.Data, &data); err != nil {
				continue
			}
			fmt.Fprintf(w, "\n[Assistant] (%dms, %d in / %d out tokens",
				data.LatencyMs, data.Usage.Input, data.Usage.Output)
			if data.CostUSD > 0 {
				fmt.Fprintf(w, ", $%.4f", data.CostUSD)
			}
			fmt.Fprintf(w, ")\n")
			if data.Content != "" {
				fmt.Fprintf(w, "%s\n", data.Content)
			}
			if len(data.ToolCalls) > 0 {
				fmt.Fprintf(w, "[%d tool call(s)]\n", len(data.ToolCalls))
			}

		case agent.EventToolCall:
			var data ToolCallData
			if err := json.Unmarshal(e.Data, &data); err != nil {
				continue
			}
			fmt.Fprintf(w, "\n  > %s (%dms)\n", data.Tool, data.DurationMs)
			fmt.Fprintf(w, "    Input:  %s\n", compactJSON(data.Input))
			output := data.Output
			if len(output) > 200 {
				output = output[:200] + "...[truncated]"
			}
			fmt.Fprintf(w, "    Output: %s\n", strings.ReplaceAll(output, "\n", "\n            "))
			if data.Error != "" {
				fmt.Fprintf(w, "    Error:  %s\n", data.Error)
			}

		case agent.EventSessionEnd:
			var data SessionEndData
			if err := json.Unmarshal(e.Data, &data); err != nil {
				continue
			}
			fmt.Fprintf(w, "\n=== End (%s) ===\n", data.Status)
			if data.Model != "" {
				fmt.Fprintf(w, "Model: %s\n", data.Model)
			}
			fmt.Fprintf(w, "Duration: %dms | Tokens: %d in / %d out",
				data.DurationMs, data.Tokens.Input, data.Tokens.Output)
			if data.CostUSD == nil || *data.CostUSD < 0 {
				fmt.Fprintf(w, " | Cost: unknown")
			} else if *data.CostUSD > 0 {
				fmt.Fprintf(w, " | Cost: $%.4f", *data.CostUSD)
			} else {
				fmt.Fprintf(w, " | Cost: $0 (local)")
			}
			fmt.Fprintln(w)
			if len(data.Metadata) > 0 {
				fmt.Fprintf(w, "Metadata:")
				for k, v := range data.Metadata {
					fmt.Fprintf(w, " %s=%s", k, v)
				}
				fmt.Fprintln(w)
			}
			if data.Error != "" {
				fmt.Fprintf(w, "Error: %s\n", data.Error)
			}
		}
	}
	return nil
}

func compactJSON(raw json.RawMessage) string {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return string(raw)
	}
	data, _ := json.Marshal(v)
	return string(data)
}
