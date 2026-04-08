package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/DocumentDrivenDX/agent"
)

// EditEntry is a single oldText→newText replacement.
type EditEntry struct {
	OldText string `json:"oldText"`
	NewText string `json:"newText"`
}

// EditParams are the parameters for the edit tool.
// Supports two forms:
//   - Multi-edit (pi-style): path + edits[] array
//   - Single-edit (legacy): path + old_string + new_string
type EditParams struct {
	Path string `json:"path"`

	// Multi-edit form
	Edits []EditEntry `json:"edits,omitempty"`

	// Single-edit form (backward compat)
	OldString string `json:"old_string,omitempty"`
	NewString string `json:"new_string,omitempty"`
}

// EditTool performs find-replace edits on files.
type EditTool struct {
	WorkDir string
}

func (t *EditTool) Name() string { return "edit" }
func (t *EditTool) Description() string {
	return "Make targeted edits to a file using exact string replacement. Supports multiple disjoint edits in one call via the edits[] array. Each oldText must appear exactly once in the original file and must not overlap with other edits. Use this for precise changes to existing files — prefer edit over write when modifying files. Always read the file first to get the exact text to replace."
}
func (t *EditTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"additionalProperties": false,
		"properties": {
			"path": {"type": "string", "description": "File path (relative to working directory or absolute)"},
			"edits": {
				"type": "array",
				"description": "One or more targeted replacements. Each edit is matched against the original file, not incrementally. Do not include overlapping or nested edits. If two changes touch the same block or nearby lines, merge them into one edit instead.",
				"items": {
					"type": "object",
					"additionalProperties": false,
					"required": ["oldText", "newText"],
					"properties": {
						"oldText": {"type": "string", "description": "Exact text to find (must be unique in the file and must not overlap with other edits)"},
						"newText": {"type": "string", "description": "Replacement text"}
					}
				}
			},
			"old_string": {"type": "string", "description": "Legacy: exact string to find (use edits[] instead)"},
			"new_string": {"type": "string", "description": "Legacy: replacement string (use edits[] instead)"}
		},
		"required": ["path"]
	}`)
}

func (t *EditTool) Execute(ctx context.Context, params json.RawMessage) (string, error) {
	var p EditParams
	if err := json.Unmarshal(params, &p); err != nil {
		return "", fmt.Errorf("edit: invalid params: %w", err)
	}

	// Normalize: convert single-edit form to edits[] array
	edits := p.Edits
	if len(edits) == 0 && p.OldString != "" {
		edits = []EditEntry{{OldText: p.OldString, NewText: p.NewString}}
	}
	if len(edits) == 0 {
		return "", fmt.Errorf("edit: edits[] array or old_string/new_string required")
	}

	resolved := resolvePath(t.WorkDir, p.Path)

	data, err := os.ReadFile(resolved)
	if err != nil {
		return "", fmt.Errorf("edit: %w", err)
	}

	content := string(data)

	// Validate all edits against the original content before applying any.
	for i, e := range edits {
		if e.OldText == "" {
			return "", fmt.Errorf("edit: edits[%d].oldText must not be empty", i)
		}
		count := strings.Count(content, e.OldText)
		if count == 0 {
			return "", fmt.Errorf("edit: edits[%d].oldText not found in %s", i, resolved)
		}
		if count > 1 {
			return "", fmt.Errorf("edit: edits[%d].oldText appears %d times in %s (must be unique)", i, count, resolved)
		}
	}

	// Check for overlapping edits: each oldText's position must not overlap
	// with any other oldText's range in the original content.
	if len(edits) > 1 {
		type span struct{ start, end int }
		spans := make([]span, len(edits))
		for i, e := range edits {
			idx := strings.Index(content, e.OldText)
			spans[i] = span{idx, idx + len(e.OldText)}
		}
		for i := 0; i < len(spans); i++ {
			for j := i + 1; j < len(spans); j++ {
				if spans[i].start < spans[j].end && spans[j].start < spans[i].end {
					return "", fmt.Errorf("edit: edits[%d] and edits[%d] overlap", i, j)
				}
			}
		}
	}

	// Apply all edits (each against the evolving content, but since we
	// validated against the original and they don't overlap, order doesn't
	// matter for correctness — apply in reverse position order to preserve
	// offsets).
	type posEdit struct {
		pos  int
		edit EditEntry
	}
	positioned := make([]posEdit, len(edits))
	for i, e := range edits {
		positioned[i] = posEdit{strings.Index(content, e.OldText), e}
	}
	// Sort by position descending so replacements don't shift later offsets
	for i := 0; i < len(positioned); i++ {
		for j := i + 1; j < len(positioned); j++ {
			if positioned[j].pos > positioned[i].pos {
				positioned[i], positioned[j] = positioned[j], positioned[i]
			}
		}
	}
	for _, pe := range positioned {
		content = content[:pe.pos] + pe.edit.NewText + content[pe.pos+len(pe.edit.OldText):]
	}

	if err := os.WriteFile(resolved, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("edit: writing: %w", err)
	}

	if len(edits) == 1 {
		return fmt.Sprintf("Replaced 1 occurrence in %s", resolved), nil
	}
	return fmt.Sprintf("Applied %d edits to %s", len(edits), resolved), nil
}

var _ agent.Tool = (*EditTool)(nil)
