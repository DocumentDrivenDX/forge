package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/DocumentDrivenDX/agent"
)

// LsParams are the parameters for the ls tool.
type LsParams struct {
	Path string `json:"path,omitempty"` // defaults to WorkDir
}

// LsTool lists directory contents.
type LsTool struct {
	WorkDir string
}

func (t *LsTool) Name() string { return "ls" }
func (t *LsTool) Description() string {
	return "List directory contents. Use instead of ls."
}
func (t *LsTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"path": {"type": "string", "description": "Directory to list (relative to working directory or absolute; defaults to working directory)"}
		}
	}`)
}

func (t *LsTool) Execute(_ context.Context, params json.RawMessage) (string, error) {
	var p LsParams
	if err := json.Unmarshal(params, &p); err != nil {
		return "", fmt.Errorf("ls: invalid params: %w", err)
	}

	dir := t.WorkDir
	if p.Path != "" {
		dir = resolvePath(t.WorkDir, p.Path)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("ls: %w", err)
	}

	// Separate dirs and files; sort each group alphabetically.
	var dirs, files []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, e.Name()+"/")
		} else {
			files = append(files, e.Name())
		}
	}
	sort.Strings(dirs)
	sort.Strings(files)

	all := append(dirs, files...)
	if len(all) == 0 {
		return "(empty directory)", nil
	}

	// Show relative path header only when listing a non-default directory.
	rel, _ := filepath.Rel(t.WorkDir, dir)
	header := rel
	if header == "." || header == "" {
		header = "."
	}

	return fmt.Sprintf("%s/\n%s", header, strings.Join(all, "\n")), nil
}

func (t *LsTool) Parallel() bool { return true }

var _ agent.Tool = (*LsTool)(nil)
