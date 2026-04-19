package compaction

import (
	"encoding/json"
	"testing"

	"github.com/DocumentDrivenDX/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSerializeConversation(t *testing.T) {
	msgs := []agent.Message{
		{Role: agent.RoleSystem, Content: "You are helpful."},
		{Role: agent.RoleUser, Content: "Read main.go"},
		{
			Role: agent.RoleAssistant,
			ToolCalls: []agent.ToolCall{
				{Name: "read", Arguments: json.RawMessage(`{"path":"main.go"}`)},
			},
		},
		{Role: agent.RoleTool, Content: "package main\n\nfunc main() {}\n", ToolCallID: "tc1"},
		{Role: agent.RoleAssistant, Content: "The package is main."},
	}

	result := SerializeConversation(msgs, 2000)

	assert.Contains(t, result, "[System]: You are helpful.")
	assert.Contains(t, result, "[User]: Read main.go")
	assert.Contains(t, result, "[Assistant → read(")
	assert.Contains(t, result, "[Tool Result]: package main")
	assert.Contains(t, result, "[Assistant]: The package is main.")
}

func TestSerializeConversation_TruncatesToolResults(t *testing.T) {
	longResult := string(make([]byte, 5000))
	msgs := []agent.Message{
		{Role: agent.RoleTool, Content: longResult, ToolCallID: "tc1"},
	}

	result := SerializeConversation(msgs, 100)
	assert.Contains(t, result, "more characters truncated")
}

func TestExtractFileOps(t *testing.T) {
	calls := []agent.ToolCallLog{
		{Tool: "read", Input: json.RawMessage(`{"path":"main.go"}`)},
		{Tool: "read", Input: json.RawMessage(`{"path":"go.mod"}`)},
		{Tool: "edit", Input: json.RawMessage(`{"path":"main.go","old_string":"a","new_string":"b"}`)},
		{Tool: "write", Input: json.RawMessage(`{"path":"new.go","content":"package new"}`)},
		{Tool: "bash", Input: json.RawMessage(`{"command":"go test"}`)},
	}

	ops := ExtractFileOps(calls)
	assert.True(t, ops.Read["main.go"])
	assert.True(t, ops.Read["go.mod"])
	assert.True(t, ops.Modified["main.go"])
	assert.True(t, ops.Modified["new.go"])
	assert.False(t, ops.Modified["go.mod"])
}

func TestFileOps_Merge(t *testing.T) {
	a := NewFileOps()
	a.Read["file1.go"] = true
	a.Modified["file2.go"] = true

	b := NewFileOps()
	b.Read["file3.go"] = true
	b.Modified["file1.go"] = true

	a.Merge(b)
	assert.True(t, a.Read["file1.go"])
	assert.True(t, a.Read["file3.go"])
	assert.True(t, a.Modified["file2.go"])
	assert.True(t, a.Modified["file1.go"])
}

func TestFileOps_FormatXML(t *testing.T) {
	ops := NewFileOps()
	ops.Read["main.go"] = true
	ops.Read["go.mod"] = true
	ops.Modified["main.go"] = true
	ops.Modified["new.go"] = true

	xml := ops.FormatXML()

	// main.go was both read and modified — should only appear in modified
	assert.Contains(t, xml, "<read-files>")
	assert.Contains(t, xml, "go.mod")
	assert.Contains(t, xml, "<modified-files>")
	assert.Contains(t, xml, "main.go")
	assert.Contains(t, xml, "new.go")

	// Verify go.mod is NOT in modified
	require.NotContains(t, xml[findIndex(xml, "<modified-files>"):], "go.mod")
}

func findIndex(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return 0
}
