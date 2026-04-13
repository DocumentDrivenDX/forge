package prompt

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/DocumentDrivenDX/agent"
	"github.com/stretchr/testify/assert"
)

type stubTool struct {
	name string
	desc string
}

func (t *stubTool) Name() string            { return t.name }
func (t *stubTool) Description() string     { return t.desc }
func (t *stubTool) Schema() json.RawMessage { return nil }
func (t *stubTool) Execute(ctx context.Context, params json.RawMessage) (string, error) {
	return "", nil
}
func (t *stubTool) Parallel() bool { return false }

func TestBuilder_BaseOnly(t *testing.T) {
	result := New("You are a helpful assistant.").
		WithDate("2026-04-06").
		Build()

	assert.Contains(t, result, "You are a helpful assistant.")
	assert.Contains(t, result, "Current date: 2026-04-06")
}

func TestBuilder_WithTools(t *testing.T) {
	tools := []agent.Tool{
		&stubTool{name: "read", desc: "Read file contents"},
		&stubTool{name: "bash", desc: "Execute shell commands"},
	}

	result := New("Base prompt.").
		WithTools(tools).
		WithDate("2026-04-06").
		Build()

	assert.Contains(t, result, "# Tools")
	assert.Contains(t, result, "- read: Read file contents")
	assert.Contains(t, result, "- bash: Execute shell commands")
}

func TestBuilder_WithGuidelines(t *testing.T) {
	result := New("Base.").
		WithGuidelines("Be concise", "Show file paths clearly").
		WithDate("2026-04-06").
		Build()

	assert.Contains(t, result, "Guidelines:")
	assert.Contains(t, result, "- Be concise")
	assert.Contains(t, result, "- Show file paths clearly")
}

func TestBuilder_WithContextFiles(t *testing.T) {
	files := []ContextFile{
		{Path: "AGENTS.md", Content: "Use Go 1.23+\nFollow TDD."},
		{Path: "CLAUDE.md", Content: "Project-specific rules."},
	}

	result := New("Base.").
		WithContextFiles(files).
		WithDate("2026-04-06").
		Build()

	assert.Contains(t, result, "# Project Context")
	assert.Contains(t, result, "## AGENTS.md")
	assert.Contains(t, result, "Use Go 1.23+")
	assert.Contains(t, result, "## CLAUDE.md")
	assert.Contains(t, result, "Project-specific rules.")
}

func TestBuilder_WithAppend(t *testing.T) {
	result := New("Base.").
		WithAppend("Extra instructions from caller.").
		WithDate("2026-04-06").
		Build()

	assert.Contains(t, result, "Extra instructions from caller.")
}

func TestBuilder_WithWorkDirAndMetadata(t *testing.T) {
	result := New("Base.").
		WithWorkDir("/home/user/project").
		WithMetadata("bead_id", "agent-abc123").
		WithDate("2026-04-06").
		Build()

	assert.Contains(t, result, "Current working directory: /home/user/project")
	assert.Contains(t, result, "bead_id: agent-abc123")
}

func TestBuilder_FullComposition(t *testing.T) {
	tools := []agent.Tool{
		&stubTool{name: "read", desc: "Read files"},
	}
	files := []ContextFile{
		{Path: "AGENTS.md", Content: "Project rules."},
	}

	result := New("You are a coding assistant.").
		WithTools(tools).
		WithGuidelines("Be concise").
		WithAppend("Additional context.").
		WithContextFiles(files).
		WithWorkDir("/tmp/test").
		WithDate("2026-04-06").
		WithMetadata("session", "test-001").
		Build()

	// Verify section order: base, tools, guidelines, append, context, dynamic
	baseIdx := indexOf(result, "You are a coding assistant.")
	toolsIdx := indexOf(result, "# Tools")
	guidelinesIdx := indexOf(result, "Guidelines:")
	appendIdx := indexOf(result, "Additional context.")
	contextIdx := indexOf(result, "# Project Context")
	dateIdx := indexOf(result, "Current date:")

	assert.True(t, baseIdx < toolsIdx, "base before tools")
	assert.True(t, toolsIdx < guidelinesIdx, "tools before guidelines")
	assert.True(t, guidelinesIdx < appendIdx, "guidelines before append")
	assert.True(t, appendIdx < contextIdx, "append before context")
	assert.True(t, contextIdx < dateIdx, "context before dynamic")
}

func TestBuilder_EmptyBase(t *testing.T) {
	result := New("").
		WithGuidelines("Be helpful").
		WithDate("2026-04-06").
		Build()

	assert.Contains(t, result, "Guidelines:")
	assert.Contains(t, result, "Current date: 2026-04-06")
}

func TestBuilderDeterminism(t *testing.T) {
	b1 := New("base").
		WithDate("2026-04-12").
		WithWorkDir("/work").
		WithMetadata("z-key", "z-val").
		WithMetadata("a-key", "a-val").
		WithContextFiles([]ContextFile{
			{Path: "z.md", Content: "z content"},
			{Path: "a.md", Content: "a content"},
		})
	b2 := New("base").
		WithDate("2026-04-12").
		WithWorkDir("/work").
		WithMetadata("a-key", "a-val").
		WithMetadata("z-key", "z-val").
		WithContextFiles([]ContextFile{
			{Path: "a.md", Content: "a content"},
			{Path: "z.md", Content: "z content"},
		})
	assert.Equal(t, b1.Build(), b2.Build(), "Build() must be deterministic regardless of insertion order")
}

func indexOf(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}
