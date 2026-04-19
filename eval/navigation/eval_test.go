// Package navigation_test contains micro-evals for structured navigation tool usage.
// These run without a live LLM and verify that:
//   - The navigation tools (glob, grep, ls, read) are available to the agent
//   - The agent loop correctly executes navigation tool calls end-to-end
//   - Tool descriptions explicitly guide away from bash anti-patterns
//
// See docs/helix/02-design/solution-designs/SD-009-benchmark-mode.md §6
package navigation_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/DocumentDrivenDX/agent"
	"github.com/DocumentDrivenDX/agent/internal/tool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seqProvider returns responses in order. After the sequence is exhausted it
// returns a terminal text response so the agent loop exits cleanly.
type seqProvider struct {
	responses []agent.Response
	idx       atomic.Int32
}

func (p *seqProvider) Chat(_ context.Context, _ []agent.Message, _ []agent.ToolDef, _ agent.Options) (agent.Response, error) {
	i := int(p.idx.Add(1)) - 1
	if i < len(p.responses) {
		return p.responses[i], nil
	}
	return agent.Response{Content: "done", FinishReason: "stop", Model: "seq"}, nil
}

// toolCallResp returns a Response that calls the named tool with args.
func toolCallResp(toolName string, args map[string]any) agent.Response {
	argsJSON, _ := json.Marshal(args)
	return agent.Response{
		ToolCalls:    []agent.ToolCall{{ID: "c1", Name: toolName, Arguments: argsJSON}},
		Usage:        agent.TokenUsage{Input: 10, Output: 10, Total: 20},
		Model:        "seq",
		FinishReason: "tool_calls",
	}
}

// finalResp returns a terminal text Response.
func finalResp(content string) agent.Response {
	return agent.Response{
		Content:      content,
		Usage:        agent.TokenUsage{Input: 5, Output: 5, Total: 10},
		Model:        "seq",
		FinishReason: "stop",
	}
}

// navTools returns all navigation + standard tools for the given workDir.
func navTools(workDir string) []agent.Tool {
	return []agent.Tool{
		&tool.ReadTool{WorkDir: workDir},
		&tool.WriteTool{WorkDir: workDir},
		&tool.EditTool{WorkDir: workDir},
		&tool.BashTool{WorkDir: workDir},
		&tool.GlobTool{WorkDir: workDir},
		&tool.GrepTool{WorkDir: workDir},
		&tool.LsTool{WorkDir: workDir},
	}
}

// calledToolNames extracts tool names from result.ToolCalls.
func calledToolNames(result agent.Result) []string {
	names := make([]string, len(result.ToolCalls))
	for i, tc := range result.ToolCalls {
		names[i] = tc.Tool
	}
	return names
}

// TestNoBashFileRead — agent uses read tool, not bash, to inspect a known file.
// SD-009 §6 micro-eval: "no-bash file read".
func TestNoBashFileRead(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "hello.go"), []byte(
		"package main\n\nfunc Greet(name string) string { return name }\n",
	), 0o644))

	prov := &seqProvider{responses: []agent.Response{
		toolCallResp("read", map[string]any{"path": "hello.go"}),
		finalResp("The file exports function Greet."),
	}}

	result, err := agent.Run(context.Background(), agent.Request{
		Prompt:        "Read the file hello.go and tell me what function it exports.",
		Provider:      prov,
		Tools:         navTools(dir),
		MaxIterations: 5,
		WorkDir:       dir,
	})
	require.NoError(t, err)
	assert.Equal(t, agent.StatusSuccess, result.Status)

	called := calledToolNames(result)
	assert.Contains(t, called, "read", "read tool must be called")
	assert.NotContains(t, called, "bash", "bash must not be called for a pure file read")
}

// TestGlobNotFind — agent uses glob to list files instead of bash find.
// SD-009 §6 micro-eval: structured navigation tool usage.
func TestGlobNotFind(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"src/auth.go", "src/user.go", "main.go"} {
		full := filepath.Join(dir, filepath.FromSlash(f))
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, []byte("package main"), 0o644))
	}

	prov := &seqProvider{responses: []agent.Response{
		toolCallResp("glob", map[string]any{"pattern": "src/**/*.go"}),
		finalResp("Found: src/auth.go, src/user.go"),
	}}

	result, err := agent.Run(context.Background(), agent.Request{
		Prompt:        "List all .go files in the src/ directory.",
		Provider:      prov,
		Tools:         navTools(dir),
		MaxIterations: 5,
		WorkDir:       dir,
	})
	require.NoError(t, err)
	assert.Equal(t, agent.StatusSuccess, result.Status)

	called := calledToolNames(result)
	assert.Contains(t, called, "glob", "glob tool must be called")
	assert.NotContains(t, called, "bash", "bash must not be called for file listing")

	// Verify glob tool actually returned the right files.
	var globOutput string
	for _, tc := range result.ToolCalls {
		if tc.Tool == "glob" {
			globOutput = tc.Output
		}
	}
	assert.Contains(t, globOutput, "auth.go")
	assert.Contains(t, globOutput, "user.go")
}

// TestGrepNotBashGrep — agent uses grep tool instead of bash grep.
func TestGrepNotBashGrep(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.go"), []byte(
		"package main\n\n// timeout controls request timeout in seconds.\nconst timeout = 30\n",
	), 0o644))

	prov := &seqProvider{responses: []agent.Response{
		toolCallResp("grep", map[string]any{"pattern": "timeout"}),
		finalResp("Found 'timeout' in config.go:3"),
	}}

	result, err := agent.Run(context.Background(), agent.Request{
		Prompt:        "Find all files that contain the word 'timeout'.",
		Provider:      prov,
		Tools:         navTools(dir),
		MaxIterations: 5,
		WorkDir:       dir,
	})
	require.NoError(t, err)
	assert.Equal(t, agent.StatusSuccess, result.Status)

	called := calledToolNames(result)
	assert.Contains(t, called, "grep")
	assert.NotContains(t, called, "bash")

	// Verify grep tool found the right file.
	for _, tc := range result.ToolCalls {
		if tc.Tool == "grep" {
			assert.Contains(t, tc.Output, "config.go")
			assert.Contains(t, tc.Output, "timeout")
		}
	}
}

// TestLsNotBashLs — agent uses ls tool instead of bash ls.
func TestLsNotBashLs(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "cmd", "server")
	require.NoError(t, os.MkdirAll(sub, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sub, "main.go"), []byte("package main"), 0o644))

	prov := &seqProvider{responses: []agent.Response{
		toolCallResp("ls", map[string]any{"path": "cmd/server"}),
		finalResp("cmd/server contains: main.go"),
	}}

	result, err := agent.Run(context.Background(), agent.Request{
		Prompt:        "What files are in the cmd/server directory?",
		Provider:      prov,
		Tools:         navTools(dir),
		MaxIterations: 5,
		WorkDir:       dir,
	})
	require.NoError(t, err)
	assert.Equal(t, agent.StatusSuccess, result.Status)

	called := calledToolNames(result)
	assert.Contains(t, called, "ls")
	assert.NotContains(t, called, "bash")

	for _, tc := range result.ToolCalls {
		if tc.Tool == "ls" {
			assert.Contains(t, tc.Output, "main.go")
		}
	}
}

// TestToolDescriptionsDiscourageBash verifies that navigation tool descriptions
// explicitly guide the agent away from bash for file navigation.
// Static check — no LLM call required.
func TestToolDescriptionsDiscourageBash(t *testing.T) {
	dir := t.TempDir()

	globDesc := (&tool.GlobTool{WorkDir: dir}).Description()
	grepDesc := (&tool.GrepTool{WorkDir: dir}).Description()
	lsDesc := (&tool.LsTool{WorkDir: dir}).Description()
	readDesc := (&tool.ReadTool{WorkDir: dir}).Description()

	assert.True(t, strings.Contains(globDesc, "find") || strings.Contains(globDesc, "ls"),
		"glob description should mention 'find' or 'ls' as the shell commands it replaces")
	assert.True(t, strings.Contains(grepDesc, "grep") || strings.Contains(grepDesc, "rg"),
		"grep description should mention 'grep' or 'rg' as the shell commands it replaces")
	assert.True(t, strings.Contains(lsDesc, "ls"),
		"ls description should mention 'ls' as the shell command it replaces")
	assert.True(t, strings.Contains(readDesc, "cat") || strings.Contains(readDesc, "shell"),
		"read description should mention 'cat' or 'shell' as alternatives it replaces")
}
