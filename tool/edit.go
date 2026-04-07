package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/anthropics/forge"
)

// EditParams are the parameters for the edit tool.
type EditParams struct {
	Path      string `json:"path"`
	OldString string `json:"old_string"`
	NewString string `json:"new_string"`
}

// EditTool performs find-replace edits on files.
type EditTool struct {
	WorkDir string
}

func (t *EditTool) Name() string        { return "edit" }
func (t *EditTool) Description() string { return "Find and replace a unique string in a file." }
func (t *EditTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"path":       {"type": "string", "description": "File path (relative to working directory or absolute)"},
			"old_string": {"type": "string", "description": "The exact string to find (must appear exactly once)"},
			"new_string": {"type": "string", "description": "The replacement string"}
		},
		"required": ["path", "old_string", "new_string"]
	}`)
}

func (t *EditTool) Execute(ctx context.Context, params json.RawMessage) (string, error) {
	var p EditParams
	if err := json.Unmarshal(params, &p); err != nil {
		return "", fmt.Errorf("edit: invalid params: %w", err)
	}

	if p.OldString == "" {
		return "", fmt.Errorf("edit: old_string must not be empty")
	}

	resolved := resolvePath(t.WorkDir, p.Path)

	data, err := os.ReadFile(resolved)
	if err != nil {
		return "", fmt.Errorf("edit: %w", err)
	}

	content := string(data)
	count := strings.Count(content, p.OldString)
	switch count {
	case 0:
		return "", fmt.Errorf("edit: old_string not found in %s", resolved)
	case 1:
		// exactly one match — proceed
	default:
		return "", fmt.Errorf("edit: old_string appears %d times in %s (must be unique)", count, resolved)
	}

	newContent := strings.Replace(content, p.OldString, p.NewString, 1)
	if err := os.WriteFile(resolved, []byte(newContent), 0o644); err != nil {
		return "", fmt.Errorf("edit: writing: %w", err)
	}

	return fmt.Sprintf("Replaced 1 occurrence in %s", resolved), nil
}

var _ forge.Tool = (*EditTool)(nil)
