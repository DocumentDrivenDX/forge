package compaction

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/DocumentDrivenDX/agent"
)

// SerializeConversation renders a message history as compact text suitable
// for the summarization LLM. Tool calls are rendered inline as
// [Assistant → toolName(args)]: result. Tool results are truncated to maxChars.
func SerializeConversation(messages []agent.Message, maxToolResultChars int) string {
	var lines []string

	for _, msg := range messages {
		switch msg.Role {
		case agent.RoleSystem:
			lines = append(lines, fmt.Sprintf("[System]: %s", msg.Content))

		case agent.RoleUser:
			lines = append(lines, fmt.Sprintf("[User]: %s", msg.Content))

		case agent.RoleAssistant:
			if len(msg.ToolCalls) > 0 {
				for _, tc := range msg.ToolCalls {
					argsStr := formatToolArgs(tc.Arguments)
					lines = append(lines, fmt.Sprintf("[Assistant → %s(%s)]", tc.Name, argsStr))
				}
				if msg.Content != "" {
					lines = append(lines, fmt.Sprintf("[Assistant]: %s", msg.Content))
				}
			} else {
				lines = append(lines, fmt.Sprintf("[Assistant]: %s", msg.Content))
			}

		case agent.RoleTool:
			truncated := TruncateToolResult(msg.Content, maxToolResultChars)
			lines = append(lines, fmt.Sprintf("[Tool Result]: %s", truncated))
		}
	}

	return strings.Join(lines, "\n")
}

// formatToolArgs renders JSON arguments as key=value pairs.
func formatToolArgs(args json.RawMessage) string {
	if len(args) == 0 {
		return ""
	}

	var m map[string]interface{}
	if err := json.Unmarshal(args, &m); err != nil {
		return string(args) // fallback to raw JSON
	}

	var parts []string
	for k, v := range m {
		vStr, _ := json.Marshal(v)
		parts = append(parts, fmt.Sprintf("%s=%s", k, string(vStr)))
	}
	return strings.Join(parts, ", ")
}

// FileOps tracks files read and modified during a conversation.
type FileOps struct {
	Read     map[string]bool
	Modified map[string]bool
}

// NewFileOps creates an empty FileOps tracker.
func NewFileOps() *FileOps {
	return &FileOps{
		Read:     make(map[string]bool),
		Modified: make(map[string]bool),
	}
}

// ExtractFileOps scans tool call logs for file operations.
func ExtractFileOps(toolCalls []agent.ToolCallLog) *FileOps {
	ops := NewFileOps()
	for _, tc := range toolCalls {
		var args struct {
			Path string `json:"path"`
		}
		_ = json.Unmarshal(tc.Input, &args)
		if args.Path == "" {
			continue
		}
		switch tc.Tool {
		case "read":
			ops.Read[args.Path] = true
		case "write", "edit":
			ops.Modified[args.Path] = true
		}
	}
	return ops
}

// Merge combines another FileOps into this one.
func (f *FileOps) Merge(other *FileOps) {
	for path := range other.Read {
		f.Read[path] = true
	}
	for path := range other.Modified {
		f.Modified[path] = true
	}
}

// FormatXML returns the file lists as XML tags for appending to a summary.
func (f *FileOps) FormatXML() string {
	var sb strings.Builder

	// Read-only files (read but not modified)
	var readOnly []string
	for path := range f.Read {
		if !f.Modified[path] {
			readOnly = append(readOnly, path)
		}
	}
	if len(readOnly) > 0 {
		sb.WriteString("\n\n<read-files>\n")
		for _, path := range readOnly {
			sb.WriteString(path)
			sb.WriteByte('\n')
		}
		sb.WriteString("</read-files>")
	}

	if len(f.Modified) > 0 {
		sb.WriteString("\n\n<modified-files>\n")
		for path := range f.Modified {
			sb.WriteString(path)
			sb.WriteByte('\n')
		}
		sb.WriteString("</modified-files>")
	}

	return sb.String()
}
