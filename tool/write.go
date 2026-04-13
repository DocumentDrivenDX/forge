package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/DocumentDrivenDX/agent"
	"github.com/DocumentDrivenDX/agent/internal/safefs"
)

// WriteParams are the parameters for the write tool.
type WriteParams struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// WriteTool creates or overwrites a file.
type WriteTool struct {
	WorkDir string
}

func (t *WriteTool) Name() string { return "write" }
func (t *WriteTool) Description() string {
	return "Create or overwrite a file with the given content. Creates parent directories as needed."
}
func (t *WriteTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"path":    {"type": "string", "description": "File path (relative to working directory or absolute)"},
			"content": {"type": "string", "description": "Content to write to the file"}
		},
		"required": ["path", "content"]
	}`)
}

func (t *WriteTool) Execute(ctx context.Context, params json.RawMessage) (string, error) {
	var p WriteParams
	if err := json.Unmarshal(params, &p); err != nil {
		return "", fmt.Errorf("write: invalid params: %w", err)
	}

	resolved := resolvePath(t.WorkDir, p.Path)

	dir := filepath.Dir(resolved)
	if err := safefs.MkdirAll(dir, 0o750); err != nil {
		return "", fmt.Errorf("write: creating directories: %w", err)
	}

	if err := safefs.WriteFile(resolved, []byte(p.Content), 0o600); err != nil {
		return "", fmt.Errorf("write: %w", err)
	}

	return fmt.Sprintf("Wrote %d bytes to %s", len(p.Content), resolved), nil
}

func (t *WriteTool) Parallel() bool { return false }

var _ agent.Tool = (*WriteTool)(nil)
