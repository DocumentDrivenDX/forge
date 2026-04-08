package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/DocumentDrivenDX/agent"
)

// ReadParams are the parameters for the read tool.
type ReadParams struct {
	Path   string `json:"path"`
	Offset int    `json:"offset,omitempty"` // 0-based line offset
	Limit  int    `json:"limit,omitempty"`  // max lines to return
}

// ReadTool reads file contents relative to a working directory.
type ReadTool struct {
	WorkDir string
}

func (t *ReadTool) Name() string { return "read" }
func (t *ReadTool) Description() string {
	return "Read the contents of a file. Use this tool to examine files instead of cat or shell commands. For large files, use offset and limit to read specific sections. Always read a file before editing it."
}
func (t *ReadTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"path":   {"type": "string", "description": "File path (relative to working directory or absolute)"},
			"offset": {"type": "integer", "description": "0-based line offset to start reading from"},
			"limit":  {"type": "integer", "description": "Maximum number of lines to return"}
		},
		"required": ["path"]
	}`)
}

func (t *ReadTool) Execute(ctx context.Context, params json.RawMessage) (string, error) {
	var p ReadParams
	if err := json.Unmarshal(params, &p); err != nil {
		return "", fmt.Errorf("read: invalid params: %w", err)
	}

	resolved := resolvePath(t.WorkDir, p.Path)

	data, err := os.ReadFile(resolved)
	if err != nil {
		return "", fmt.Errorf("read: %w", err)
	}

	if !utf8.Valid(data) {
		return "", fmt.Errorf("read: file appears to be binary: %s", resolved)
	}

	content := string(data)

	if p.Offset > 0 || p.Limit > 0 {
		lines := strings.Split(content, "\n")
		start := p.Offset
		if start > len(lines) {
			start = len(lines)
		}
		end := len(lines)
		if p.Limit > 0 && start+p.Limit < end {
			end = start + p.Limit
		}
		content = strings.Join(lines[start:end], "\n")
	}

	return content, nil
}

// Verify ReadTool implements agent.Tool at compile time.
var _ agent.Tool = (*ReadTool)(nil)
