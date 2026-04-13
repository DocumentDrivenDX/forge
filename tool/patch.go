package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/DocumentDrivenDX/agent"
	"github.com/DocumentDrivenDX/agent/internal/safefs"
)

// PatchParams are the parameters for the patch tool.
// The model provides search (context from the file) and content (the replacement).
// When search is omitted or empty, the operation is a full-file replace,
// prepend, or append depending on the operation field.
type PatchParams struct {
	Path      string `json:"path"`
	Search    string `json:"search,omitempty"`    // unique text to locate; must appear exactly once
	Content   string `json:"content"`             // replacement text, or text to prepend/append
	Operation string `json:"operation,omitempty"` // "replace" (default), "replace_all", "prepend", "append"
}

// PatchTool performs search-and-replace edits on files with robust line-ending
// handling and clear error messages. Inspired by ForgeCode's fs_patch design.
type PatchTool struct {
	WorkDir string
}

func (t *PatchTool) Name() string { return "patch" }

func (t *PatchTool) Description() string {
	return "Make targeted edits to a file. Supports: replace (default), replace_all, prepend, and append operations."
}

func (t *PatchTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"additionalProperties": false,
		"properties": {
			"path":      {"type": "string", "description": "File path (relative to working directory or absolute)"},
			"search":    {"type": "string", "description": "Exact text to find in the file. Must match exactly (including whitespace). Will match first occurrence."},
			"content":   {"type": "string", "description": "Replacement text, or text to prepend/append"},
			"operation": {"type": "string", "description": "One of: 'replace' (default, search must be unique), 'replace_all' (replace every occurrence), 'prepend' (insert at file start), 'append' (insert at file end)", "enum": ["replace", "replace_all", "prepend", "append"]}
		},
		"required": ["path", "content"]
	}`)
}

func (t *PatchTool) Execute(_ context.Context, params json.RawMessage) (string, error) {
	var p PatchParams
	if err := json.Unmarshal(params, &p); err != nil {
		return "", fmt.Errorf("patch: invalid params: %w", err)
	}
	if p.Path == "" {
		return "", fmt.Errorf("patch: path is required")
	}

	op := normalizeOp(p.Operation)

	resolved := resolvePath(t.WorkDir, p.Path)

	data, err := safefs.ReadFile(resolved)
	if err != nil {
		return "", fmt.Errorf("patch: %w", err)
	}

	// Preserve original line ending for normalization
	lineEnding := detectLineEnding(string(data))
	search := normalizeLineEndings(p.Search, lineEnding)
	content := normalizeLineEndings(p.Content, lineEnding)
	original := string(data)

	switch op {
	case opReplace:
		return t.replace(resolved, original, search, content)
	case opReplaceAll:
		return t.replaceAll(resolved, original, search, content)
	case opPrepend:
		return t.prepend(resolved, original, search, content, lineEnding)
	case opAppend:
		return t.append(resolved, original, search, content, lineEnding)
	default:
		return "", fmt.Errorf("patch: unknown operation %q — valid: replace, replace_all, prepend, append", p.Operation)
	}
}

type patchOp int

const (
	opReplace patchOp = iota
	opReplaceAll
	opPrepend
	opAppend
)

func normalizeOp(s string) patchOp {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "replace":
		return opReplace
	case "replace_all", "replaceall", "replaceAll", "replace-all":
		return opReplaceAll
	case "prepend":
		return opPrepend
	case "append":
		return opAppend
	}
	return opReplace
}

// replace performs a single exact-match replacement. search must appear exactly once.
func (t *PatchTool) replace(path, original, search, content string) (string, error) {
	if search == "" {
		return "", fmt.Errorf("patch: search text is required for 'replace' operation. Read the file first to find the exact text to replace.")
	}
	count := strings.Count(original, search)
	if count == 0 {
		return "", fmt.Errorf("patch: search text not found in %s.\n\nThe file may have changed since you last read it. Re-read the file with the read tool to get the current content, then try again.", path)
	}
	if count > 1 {
		return "", fmt.Errorf("patch: search text appears %d times in %s. Provide more context to make the search unique, or use 'replace_all' to replace every occurrence.", count, path)
	}

	newContent := searchReplace(original, search, content)
	if err := safefs.WriteFile(path, []byte(newContent), 0o600); err != nil {
		return "", fmt.Errorf("patch: writing: %w", err)
	}
	return fmt.Sprintf("Replaced 1 occurrence in %s", path), nil
}

// replaceAll replaces every occurrence of search.
func (t *PatchTool) replaceAll(path, original, search, content string) (string, error) {
	if search == "" {
		return "", fmt.Errorf("patch: search text is required for 'replace_all' operation")
	}
	count := strings.Count(original, search)
	if count == 0 {
		return "", fmt.Errorf("patch: search text not found in %s. Re-read the file and try again.", path)
	}

	newContent := strings.ReplaceAll(original, search, content)
	if err := safefs.WriteFile(path, []byte(newContent), 0o600); err != nil {
		return "", fmt.Errorf("patch: writing: %w", err)
	}
	return fmt.Sprintf("Replaced %d occurrences in %s", count, path), nil
}

// prepend inserts content at the top of the file. If search is provided, the
// content is inserted immediately before the first occurrence of search.
func (t *PatchTool) prepend(path, original, search, content, lineEnding string) (string, error) {
	var newContent string
	if search != "" {
		if !strings.Contains(original, search) {
			return "", fmt.Errorf("patch: search text not found in %s for prepend. Re-read the file and try again.", path)
		}
		idx := strings.Index(original, search)
		newContent = original[:idx] + content + lineEnding + original[idx:]
	} else {
		newContent = content + lineEnding + original
	}
	if err := safefs.WriteFile(path, []byte(newContent), 0o600); err != nil {
		return "", fmt.Errorf("patch: writing: %w", err)
	}
	return fmt.Sprintf("Prepended content to %s", path), nil
}

// append inserts content at the end of the file. If search is provided, the
// content is inserted immediately after the first occurrence of search.
func (t *PatchTool) append(path, original, search, content, lineEnding string) (string, error) {
	var newContent string
	if search != "" {
		if !strings.Contains(original, search) {
			return "", fmt.Errorf("patch: search text not found in %s for append. Re-read the file and try again.", path)
		}
		idx := strings.Index(original, search) + len(search)
		newContent = original[:idx] + lineEnding + content + original[idx:]
	} else {
		newContent = original + lineEnding + content
	}
	if err := safefs.WriteFile(path, []byte(newContent), 0o600); err != nil {
		return "", fmt.Errorf("patch: writing: %w", err)
	}
	return fmt.Sprintf("Appended content to %s", path), nil
}

// searchReplace replaces the first occurrence of search in original with content.
func searchReplace(original, search, content string) string {
	idx := strings.Index(original, search)
	if idx == -1 {
		return original
	}
	return original[:idx] + content + original[idx+len(search):]
}

// detectLineEnding returns the dominant line ending used in the text.
func detectLineEnding(s string) string {
	crlf := strings.Count(s, "\r\n")
	lf := strings.Count(s, "\n") - crlf
	if crlf > lf {
		return "\r\n"
	}
	return "\n"
}

// normalizeLineEndings converts \r\n to the target line ending, and if target is
// \r\n also converts bare \n to \r\n.
func normalizeLineEndings(s, target string) string {
	if target == "\r\n" {
		// First normalize all to \n, then convert to \r\n.
		s = strings.ReplaceAll(s, "\r\n", "\n")
		s = strings.ReplaceAll(s, "\n", "\r\n")
	} else {
		s = strings.ReplaceAll(s, "\r\n", "\n")
	}
	return s
}

func (t *PatchTool) Parallel() bool { return false }

var _ agent.Tool = (*PatchTool)(nil)
