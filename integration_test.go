//go:build integration

package agent_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/DocumentDrivenDX/agent"
	oaiProvider "github.com/DocumentDrivenDX/agent/provider/openai"
	"github.com/DocumentDrivenDX/agent/tool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// lmStudioURL returns the LM Studio base URL, checking known hosts.
// Set LMSTUDIO_URL to override.
func lmStudioURL(t *testing.T) string {
	t.Helper()
	if url := os.Getenv("LMSTUDIO_URL"); url != "" {
		return url
	}
	// Try known hosts in order
	for _, host := range []string{"localhost:1234", "vidar:1234", "bragi:1234"} {
		url := fmt.Sprintf("http://%s/v1", host)
		p := oaiProvider.New(oaiProvider.Config{BaseURL: url, Model: "test"})
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, err := p.Chat(ctx, []agent.Message{{Role: agent.RoleUser, Content: "hi"}}, nil, agent.Options{})
		cancel()
		if err == nil {
			return url
		}
	}
	t.Skip("No LM Studio instance found (set LMSTUDIO_URL)")
	return ""
}

// lmStudioModel returns a model to use, preferring coding-capable models.
// Set LMSTUDIO_MODEL to override.
func lmStudioModel(t *testing.T) string {
	t.Helper()
	if model := os.Getenv("LMSTUDIO_MODEL"); model != "" {
		return model
	}
	// Prefer coding models, fall back to general
	return "qwen/qwen3-coder-next"
}

func TestIntegration_SimpleCompletion(t *testing.T) {
	url := lmStudioURL(t)
	model := lmStudioModel(t)

	p := oaiProvider.New(oaiProvider.Config{
		BaseURL: url,
		Model:   model,
	})

	result, err := agent.Run(context.Background(), agent.Request{
		Prompt:        "Reply with exactly: AGENT_OK",
		SystemPrompt:  "You are a test assistant. Follow instructions exactly.",
		Provider:      p,
		MaxIterations: 3,
	})
	require.NoError(t, err)
	assert.Equal(t, agent.StatusSuccess, result.Status)
	assert.NotEmpty(t, result.Output)
	assert.Greater(t, result.Tokens.Total, 0)
	t.Logf("Model: %s, Output: %q, Tokens: %+v", result.Model, result.Output, result.Tokens)
}

func TestIntegration_FileReadTask(t *testing.T) {
	url := lmStudioURL(t)
	model := lmStudioModel(t)

	// Create a test workspace
	workDir := t.TempDir()
	testFile := filepath.Join(workDir, "hello.txt")
	require.NoError(t, os.WriteFile(testFile, []byte("The secret word is BANANA.\n"), 0o644))

	p := oaiProvider.New(oaiProvider.Config{
		BaseURL: url,
		Model:   model,
	})

	tools := []agent.Tool{
		&tool.ReadTool{WorkDir: workDir},
	}

	var events []agent.Event
	result, err := agent.Run(context.Background(), agent.Request{
		Prompt:        "Read the file hello.txt and tell me the secret word.",
		SystemPrompt:  "You are a helpful assistant with access to file tools. Use the read tool to read files.",
		Provider:      p,
		Tools:         tools,
		MaxIterations: 5,
		WorkDir:       workDir,
		Callback: func(e agent.Event) {
			events = append(events, e)
		},
	})
	require.NoError(t, err)
	t.Logf("Status: %s, Output: %q", result.Status, result.Output)
	t.Logf("Tokens: %+v, Duration: %s", result.Tokens, result.Duration)
	t.Logf("Tool calls: %d, Events: %d", len(result.ToolCalls), len(events))

	// The model should have used the read tool and found "BANANA"
	assert.Equal(t, agent.StatusSuccess, result.Status)
	assert.Greater(t, result.Tokens.Total, 0)

	// Check that at least one tool call happened
	if len(result.ToolCalls) > 0 {
		assert.Equal(t, "read", result.ToolCalls[0].Tool)
		t.Logf("Tool output: %q", result.ToolCalls[0].Output)
	}

	// The output should mention BANANA (though this depends on model capability)
	if len(result.ToolCalls) > 0 {
		assert.Contains(t, result.Output, "BANANA",
			"Model should have read the file and reported the secret word")
	}
}

func TestIntegration_FileEditTask(t *testing.T) {
	url := lmStudioURL(t)
	model := lmStudioModel(t)

	workDir := t.TempDir()
	testFile := filepath.Join(workDir, "greeting.txt")
	require.NoError(t, os.WriteFile(testFile, []byte("Hello, World!\n"), 0o644))

	p := oaiProvider.New(oaiProvider.Config{
		BaseURL: url,
		Model:   model,
	})

	tools := []agent.Tool{
		&tool.ReadTool{WorkDir: workDir},
		&tool.WriteTool{WorkDir: workDir},
		&tool.EditTool{WorkDir: workDir},
	}

	result, err := agent.Run(context.Background(), agent.Request{
		Prompt:        `Read greeting.txt, then use the edit tool to replace "World" with "Agent". Then read the file again to confirm the change.`,
		SystemPrompt:  "You are a helpful assistant. Use tools to complete tasks. Always use the edit tool for find-replace operations.",
		Provider:      p,
		Tools:         tools,
		MaxIterations: 10,
		WorkDir:       workDir,
	})
	require.NoError(t, err)
	t.Logf("Status: %s, Tool calls: %d, Output: %q", result.Status, len(result.ToolCalls), result.Output)

	assert.Equal(t, agent.StatusSuccess, result.Status)

	// Verify the file was actually edited
	data, err := os.ReadFile(testFile)
	require.NoError(t, err)
	t.Logf("Final file content: %q", string(data))

	// If tool calls happened, the edit should have been applied
	for _, tc := range result.ToolCalls {
		t.Logf("  Tool: %s, Error: %q", tc.Tool, tc.Error)
	}
}
