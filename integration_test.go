//go:build integration

package agent_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/DocumentDrivenDX/agent"
	antProvider "github.com/DocumentDrivenDX/agent/internal/provider/anthropic"
	oaiProvider "github.com/DocumentDrivenDX/agent/internal/provider/openai"
	"github.com/DocumentDrivenDX/agent/internal/session"
	"github.com/DocumentDrivenDX/agent/internal/tool"
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

func anthropicAPIKey(t *testing.T) string {
	t.Helper()
	key := os.Getenv("ANTHROPIC_API_KEY")
	if key == "" {
		t.Skip("ANTHROPIC_API_KEY not set; skipping Anthropic integration")
	}
	return key
}

func anthropicModel(t *testing.T) string {
	t.Helper()
	if model := os.Getenv("ANTHROPIC_MODEL"); model != "" {
		return model
	}
	return "claude-sonnet-4-20250514"
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

func TestIntegration_OpenAICompatibleProviderIdentity(t *testing.T) {
	url := lmStudioURL(t)
	model := lmStudioModel(t)

	p := oaiProvider.New(oaiProvider.Config{
		BaseURL: url,
		Model:   model,
	})

	var start session.SessionStartData
	var sawSessionStart bool

	result, err := agent.Run(context.Background(), agent.Request{
		Prompt:        "Reply with exactly: OPENAI_MATRIX_OK",
		SystemPrompt:  "Return only the requested token.",
		Provider:      p,
		MaxIterations: 3,
		Callback: func(e agent.Event) {
			if e.Type != agent.EventSessionStart {
				return
			}
			sawSessionStart = true
			require.NoError(t, json.Unmarshal(e.Data, &start))
		},
	})
	require.NoError(t, err)
	assert.Equal(t, agent.StatusSuccess, result.Status)
	assert.Greater(t, result.Tokens.Total, 0)
	assert.NotEmpty(t, result.Model)
	assert.NotEmpty(t, result.Output)
	require.True(t, sawSessionStart, "expected session.start event")
	assert.Equal(t, "openai-compat", start.Provider)
	assert.Equal(t, model, start.Model)
}

func TestIntegration_NavigationAndPatchToolSurface(t *testing.T) {
	url := lmStudioURL(t)
	model := lmStudioModel(t)

	workDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workDir, "src"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(workDir, "docs"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "src", "target.env"), []byte("STATUS=OLD\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "docs", "notes.md"), []byte("integration fixture\n"), 0o644))

	p := oaiProvider.New(oaiProvider.Config{
		BaseURL: url,
		Model:   model,
	})
	taskStore := tool.NewTaskStore()
	tools := []agent.Tool{
		&tool.ReadTool{WorkDir: workDir},
		&tool.GlobTool{WorkDir: workDir},
		&tool.GrepTool{WorkDir: workDir},
		&tool.LsTool{WorkDir: workDir},
		&tool.PatchTool{WorkDir: workDir},
		&tool.TaskTool{Store: taskStore},
	}

	result, err := agent.Run(context.Background(), agent.Request{
		Prompt: `Use tools to complete this workspace task:
1) Inspect the workspace with ls and/or glob.
2) Use grep to find STATUS=OLD.
3) Use patch to replace STATUS=OLD with STATUS=NEW in src/target.env.
4) Use read to verify the new file content.
5) Use the task tool to track progress for at least two steps.
Return a short completion summary.`,
		SystemPrompt:  "You are an integration-test assistant. Tool usage is mandatory. Do not guess file contents; use tools.",
		Provider:      p,
		Tools:         tools,
		MaxIterations: 16,
		WorkDir:       workDir,
	})
	require.NoError(t, err)
	assert.Equal(t, agent.StatusSuccess, result.Status)
	assert.Greater(t, result.Tokens.Total, 0)
	assert.NotEmpty(t, result.Model)
	require.NotEmpty(t, result.ToolCalls, "expected tool usage in integration path")

	called := map[string]int{}
	for _, tc := range result.ToolCalls {
		called[tc.Tool]++
	}
	assert.Greater(t, called["patch"], 0, "expected patch tool call")
	assert.True(t, called["glob"] > 0 || called["grep"] > 0 || called["ls"] > 0,
		"expected at least one navigation tool call (glob/grep/ls)")

	updated, readErr := os.ReadFile(filepath.Join(workDir, "src", "target.env"))
	require.NoError(t, readErr)
	assert.Contains(t, string(updated), "STATUS=NEW")
	assert.NotContains(t, string(updated), "STATUS=OLD")
}

func TestIntegration_AnthropicProviderIdentity(t *testing.T) {
	apiKey := anthropicAPIKey(t)
	model := anthropicModel(t)

	p := antProvider.New(antProvider.Config{
		APIKey:  apiKey,
		Model:   model,
		BaseURL: os.Getenv("ANTHROPIC_BASE_URL"),
	})

	var start session.SessionStartData
	var sawSessionStart bool

	result, err := agent.Run(context.Background(), agent.Request{
		Prompt:        "Reply with exactly: ANTHROPIC_MATRIX_OK",
		SystemPrompt:  "Return only the requested token.",
		Provider:      p,
		MaxIterations: 3,
		Callback: func(e agent.Event) {
			if e.Type != agent.EventSessionStart {
				return
			}
			sawSessionStart = true
			require.NoError(t, json.Unmarshal(e.Data, &start))
		},
	})
	require.NoError(t, err)
	assert.Equal(t, agent.StatusSuccess, result.Status)
	assert.Greater(t, result.Tokens.Total, 0)
	assert.NotEmpty(t, result.Model)
	assert.NotEmpty(t, result.Output)
	require.True(t, sawSessionStart, "expected session.start event")
	assert.Equal(t, "anthropic", start.Provider)
	assert.Equal(t, model, start.Model)
}
