package main_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeCatalogServer struct {
	server         *httptest.Server
	indexByPath    map[string]string
	manifestByPath map[string]string
}

type recordedChatRequest struct {
	Model    string         `json:"model"`
	Thinking map[string]any `json:"thinking,omitempty"`
}

type fakeOpenAIServer struct {
	server          *httptest.Server
	mu              sync.Mutex
	modelsSeen      []string
	thinkingBudgets []int
}

func newFakeOpenAIServer(t *testing.T) *fakeOpenAIServer {
	t.Helper()
	fake := &fakeOpenAIServer{}
	fake.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"stub-model"}]}`))
		case "/v1/chat/completions":
			require.Equal(t, http.MethodPost, r.Method)
			defer r.Body.Close()

			var req recordedChatRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))

			fake.mu.Lock()
			fake.modelsSeen = append(fake.modelsSeen, req.Model)
			if budget, ok := req.Thinking["budget_tokens"].(float64); ok {
				fake.thinkingBudgets = append(fake.thinkingBudgets, int(budget))
			} else {
				fake.thinkingBudgets = append(fake.thinkingBudgets, 0)
			}
			fake.mu.Unlock()

			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"id":"chatcmpl-test",
				"object":"chat.completion",
				"created":1712534400,
				"model":"stub-model",
				"choices":[{"index":0,"message":{"role":"assistant","content":"stub ok"},"finish_reason":"stop"}],
				"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}
			}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(fake.server.Close)
	return fake
}

func (f *fakeOpenAIServer) lastReasoningBudget() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.thinkingBudgets) == 0 {
		return 0
	}
	return f.thinkingBudgets[len(f.thinkingBudgets)-1]
}

func newFakeCatalogServer(t *testing.T, files map[string]string) *fakeCatalogServer {
	t.Helper()
	fake := &fakeCatalogServer{
		indexByPath:    make(map[string]string),
		manifestByPath: make(map[string]string),
	}
	for name, body := range files {
		switch filepath.Ext(name) {
		case ".json":
			fake.indexByPath["/"+name] = body
		default:
			fake.manifestByPath["/"+name] = body
		}
	}
	fake.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if body, ok := fake.indexByPath[r.URL.Path]; ok {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(body))
			return
		}
		if body, ok := fake.manifestByPath[r.URL.Path]; ok {
			w.Header().Set("Content-Type", "application/x-yaml")
			_, _ = w.Write([]byte(body))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(fake.server.Close)
	return fake
}

func (f *fakeOpenAIServer) baseURL() string {
	return f.server.URL + "/v1"
}

func (f *fakeOpenAIServer) lastModel() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.modelsSeen) == 0 {
		return ""
	}
	return f.modelsSeen[len(f.modelsSeen)-1]
}

func (f *fakeCatalogServer) baseURL() string {
	return f.server.URL
}

func writeTempConfig(t *testing.T, workDir, configBody string) {
	t.Helper()
	cfgDir := filepath.Join(workDir, ".agent")
	require.NoError(t, os.MkdirAll(cfgDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cfgDir, "config.yaml"), []byte(configBody), 0o644))
}

func writeTempManifest(t *testing.T, path, body string) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
}

func catalogIndexJSON(manifestPath, manifestBody, catalogVersion string, schemaVersion int) string {
	sum := sha256.Sum256([]byte(manifestBody))
	payload := map[string]any{
		"schema_version":    schemaVersion,
		"catalog_version":   catalogVersion,
		"channel":           "stable",
		"published_at":      "2026-04-10T12:00:00Z",
		"manifest_path":     manifestPath,
		"manifest_sha256":   hex.EncodeToString(sum[:]),
		"min_agent_version": "0.2.0",
	}
	data, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	return string(data)
}

func TestCLI_SessionLogs_UseWorkDirWhenRelative(t *testing.T) {
	workDir := t.TempDir()
	callerDir := t.TempDir()
	fake := newFakeOpenAIServer(t)

	writeTempConfig(t, workDir, `
providers:
  local:
    type: lmstudio
    base_url: `+fake.baseURL()+`
    api_key: test
    model: gpt-4o
default: local
session_log_dir: sessions
`)

	exe := buildAgentCLI(t)
	run := func(args ...string) ([]byte, error) {
		t.Helper()
		cmd := exec.Command(exe, args...)
		cmd.Dir = callerDir
		home := t.TempDir()
		cmd.Env = append(os.Environ(),
			"HOME="+home,
			"XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
		)
		return cmd.CombinedOutput()
	}

	out, err := run("--work-dir", workDir, "-p", "hello")
	require.NoError(t, err, string(out))
	assert.Contains(t, string(out), "[success]")

	workSessions, err := filepath.Glob(filepath.Join(workDir, "sessions", "*.jsonl"))
	require.NoError(t, err)
	require.Len(t, workSessions, 1)

	callerSessions, err := filepath.Glob(filepath.Join(callerDir, "sessions", "*.jsonl"))
	require.NoError(t, err)
	assert.Len(t, callerSessions, 0)

	sessionID := strings.TrimSuffix(filepath.Base(workSessions[0]), ".jsonl")

	out, err = run("--work-dir", workDir, "log")
	require.NoError(t, err, string(out))
	assert.Contains(t, string(out), sessionID)

	out, err = run("--work-dir", workDir, "usage", "--json")
	require.NoError(t, err, string(out))
	var report struct {
		Rows []struct {
			Provider string `json:"provider"`
			Model    string `json:"model"`
		} `json:"rows"`
	}
	require.NoError(t, json.Unmarshal(out, &report))
	require.NotEmpty(t, report.Rows)
	assert.Equal(t, "lmstudio", report.Rows[0].Provider)
	assert.Equal(t, "gpt-4o", report.Rows[0].Model)

	out, err = run("--work-dir", workDir, "replay", sessionID)
	require.NoError(t, err, string(out))
	assert.Contains(t, string(out), "Provider: lmstudio | Model: gpt-4o")
	assert.Contains(t, string(out), "Work dir: "+workDir)
	assert.Contains(t, string(out), "hello")
}

func TestCLI_ModelRef_ResolvesThroughExternalManifest(t *testing.T) {
	fake := newFakeOpenAIServer(t)
	workDir := t.TempDir()
	manifestPath := filepath.Join(workDir, "models.yaml")
	writeTempManifest(t, manifestPath, `
version: 1
generated_at: 2026-04-09T00:00:00Z
profiles:
  code-smart:
    target: external-smart
targets:
  external-smart:
    family: external
    aliases: [external-alias]
    surfaces:
      agent.openai: external-model-v1
`)
	writeTempConfig(t, workDir, `
model_catalog:
  manifest: `+manifestPath+`
providers:
  local:
    type: lmstudio
    base_url: `+fake.baseURL()+`
    api_key: test
default: local
`)

	out, err := runAgentCLI(t, "-p", "say hi", "--work-dir", workDir, "--model-ref", "code-smart")
	require.NoError(t, err, string(out))
	assert.Contains(t, string(out), "[success]")
	assert.Equal(t, "external-model-v1", fake.lastModel())
}

func TestCLI_ModelRef_DeprecatedRejectedByDefault(t *testing.T) {
	fake := newFakeOpenAIServer(t)
	workDir := t.TempDir()
	manifestPath := filepath.Join(workDir, "models.yaml")
	writeTempManifest(t, manifestPath, `
version: 1
generated_at: 2026-04-09T00:00:00Z
targets:
  legacy:
    family: demo
    status: deprecated
    replacement: current
    surfaces:
      agent.openai: old-model
  current:
    family: demo
    surfaces:
      agent.openai: new-model
`)
	writeTempConfig(t, workDir, `
model_catalog:
  manifest: `+manifestPath+`
providers:
  local:
    type: lmstudio
    base_url: `+fake.baseURL()+`
    api_key: test
default: local
`)

	out, err := runAgentCLI(t, "-p", "say hi", "--work-dir", workDir, "--model-ref", "legacy")
	require.Error(t, err)
	assert.Contains(t, string(out), `target "legacy" is deprecated; use "current"`)
	assert.Equal(t, "", fake.lastModel())
}

func TestCLI_ModelRef_DeprecatedAllowed(t *testing.T) {
	fake := newFakeOpenAIServer(t)
	workDir := t.TempDir()
	manifestPath := filepath.Join(workDir, "models.yaml")
	writeTempManifest(t, manifestPath, `
version: 1
generated_at: 2026-04-09T00:00:00Z
targets:
  legacy:
    family: demo
    status: deprecated
    replacement: current
    surfaces:
      agent.openai: old-model
  current:
    family: demo
    surfaces:
      agent.openai: new-model
`)
	writeTempConfig(t, workDir, `
model_catalog:
  manifest: `+manifestPath+`
providers:
  local:
    type: lmstudio
    base_url: `+fake.baseURL()+`
    api_key: test
default: local
`)

	out, err := runAgentCLI(t, "-p", "say hi", "--work-dir", workDir, "--model-ref", "legacy", "--allow-deprecated-model")
	require.NoError(t, err, string(out))
	assert.Contains(t, string(out), "[success]")
	assert.Equal(t, "old-model", fake.lastModel())
}

func TestCLI_ModelRef_ExternalManifestOverridesEmbeddedCatalog(t *testing.T) {
	fake := newFakeOpenAIServer(t)
	workDir := t.TempDir()
	manifestPath := filepath.Join(workDir, "models.yaml")
	writeTempManifest(t, manifestPath, `
version: 2
generated_at: 2026-04-09T00:00:00Z
profiles:
  code-fast:
    target: custom-fast
targets:
  custom-fast:
    family: custom
    surfaces:
      agent.openai: override-fast-model
`)
	writeTempConfig(t, workDir, `
model_catalog:
  manifest: `+manifestPath+`
providers:
  local:
    type: lmstudio
    base_url: `+fake.baseURL()+`
    api_key: test
default: local
`)

	out, err := runAgentCLI(t, "-p", "say hi", "--work-dir", workDir, "--model-ref", "code-fast")
	require.NoError(t, err, string(out))
	assert.Contains(t, string(out), "[success]")
	assert.Equal(t, "override-fast-model", fake.lastModel())
}

func TestCLI_ReasoningCatalogDefaultsAndOverrides(t *testing.T) {
	fake := newFakeOpenAIServer(t)
	workDir := t.TempDir()
	manifestPath := filepath.Join(workDir, "models.yaml")
	writeTempManifest(t, manifestPath, `
version: 4
generated_at: 2026-04-19T00:00:00Z
models:
  cheap-model:
    reasoning_max_tokens: 32768
  smart-model:
    reasoning_max_tokens: 32768
profiles:
  cheap:
    target: cheap-target
  smart:
    target: smart-target
targets:
  cheap-target:
    family: demo
    surfaces:
      agent.openai: cheap-model
    surface_policy:
      agent.openai:
        reasoning_default: off
  smart-target:
    family: demo
    surfaces:
      agent.openai: smart-model
    surface_policy:
      agent.openai:
        reasoning_default: high
`)
	writeTempConfig(t, workDir, `
model_catalog:
  manifest: `+manifestPath+`
providers:
  local:
    type: lmstudio
    base_url: `+fake.baseURL()+`
    api_key: test
default: local
`)

	out, err := runAgentCLI(t, "-p", "say hi", "--work-dir", workDir, "--model-ref", "cheap")
	require.NoError(t, err, string(out))
	assert.Equal(t, "cheap-model", fake.lastModel())
	assert.Equal(t, 0, fake.lastReasoningBudget())

	out, err = runAgentCLI(t, "-p", "say hi", "--work-dir", workDir, "--model-ref", "smart")
	require.NoError(t, err, string(out))
	assert.Equal(t, "smart-model", fake.lastModel())
	assert.Equal(t, 32768, fake.lastReasoningBudget())

	out, err = runAgentCLI(t, "-p", "say hi", "--work-dir", workDir, "--model-ref", "smart", "--reasoning", "8192")
	require.NoError(t, err, string(out))
	assert.Equal(t, 8192, fake.lastReasoningBudget())

	out, err = runAgentCLI(t, "-p", "say hi", "--work-dir", workDir, "--model-ref", "smart", "--reasoning", "max")
	require.NoError(t, err, string(out))
	assert.Equal(t, 32768, fake.lastReasoningBudget())
}

func TestCLI_ReasoningOffAliasesOverrideCatalogDefault(t *testing.T) {
	for _, value := range []string{"off", "none", "false", "0"} {
		t.Run(value, func(t *testing.T) {
			fake := newFakeOpenAIServer(t)
			workDir := t.TempDir()
			manifestPath := filepath.Join(workDir, "models.yaml")
			writeTempManifest(t, manifestPath, `
version: 4
generated_at: 2026-04-19T00:00:00Z
models:
  smart-model:
    reasoning_max_tokens: 32768
profiles:
  smart:
    target: smart-target
targets:
  smart-target:
    family: demo
    surfaces:
      agent.openai: smart-model
    surface_policy:
      agent.openai:
        reasoning_default: high
`)
			writeTempConfig(t, workDir, `
model_catalog:
  manifest: `+manifestPath+`
providers:
  local:
    type: lmstudio
    base_url: `+fake.baseURL()+`
    api_key: test
default: local
`)

			out, err := runAgentCLI(t, "-p", "say hi", "--work-dir", workDir, "--model-ref", "smart", "--reasoning", value)
			require.NoError(t, err, string(out))
			assert.Equal(t, 0, fake.lastReasoningBudget())
		})
	}
}

func TestCLI_ReasoningValidation(t *testing.T) {
	fake := newFakeOpenAIServer(t)
	workDir := t.TempDir()
	manifestPath := filepath.Join(workDir, "models.yaml")
	writeTempManifest(t, manifestPath, `
version: 4
generated_at: 2026-04-19T00:00:00Z
models:
  smart-model:
    reasoning_max_tokens: 32768
profiles:
  smart:
    target: smart-target
targets:
  smart-target:
    family: demo
    surfaces:
      agent.openai: smart-model
    surface_policy:
      agent.openai:
        reasoning_default: high
`)
	writeTempConfig(t, workDir, `
model_catalog:
  manifest: `+manifestPath+`
providers:
  local:
    type: lmstudio
    base_url: `+fake.baseURL()+`
    api_key: test
default: local
`)

	out, err := runAgentCLI(t, "-p", "say hi", "--work-dir", workDir, "--model-ref", "smart", "--reasoning", "bogus")
	require.Error(t, err)
	assert.Contains(t, string(out), `unsupported value "bogus"`)

	out, err = runAgentCLI(t, "-p", "say hi", "--work-dir", workDir, "--model-ref", "smart", "--reasoning", "99999")
	require.Error(t, err)
	assert.Contains(t, string(out), "exceeds maximum 32768")
}

func TestCLI_ModelRef_ExplicitModelBypassesCatalog(t *testing.T) {
	fake := newFakeOpenAIServer(t)
	workDir := t.TempDir()
	manifestPath := filepath.Join(workDir, "models.yaml")
	writeTempManifest(t, manifestPath, `
version: 1
generated_at: 2026-04-09T00:00:00Z
profiles:
  code-smart:
    target: external-smart
targets:
  external-smart:
    family: external
    surfaces:
      agent.openai: external-model-v1
`)
	writeTempConfig(t, workDir, `
model_catalog:
  manifest: `+manifestPath+`
providers:
  local:
    type: lmstudio
    base_url: `+fake.baseURL()+`
    api_key: test
default: local
`)

	out, err := runAgentCLI(t, "-p", "say hi", "--work-dir", workDir, "--model-ref", "code-smart", "--model", "explicit-model")
	require.NoError(t, err, string(out))
	assert.Contains(t, string(out), "[success]")
	assert.Equal(t, "explicit-model", fake.lastModel())
}

func TestCLI_CatalogShow_EmbeddedFallback(t *testing.T) {
	workDir := t.TempDir()
	home := t.TempDir()

	out, err := runAgentCLIWithHome(t, home, "--work-dir", workDir, "catalog", "show")
	require.NoError(t, err, string(out))
	output := string(out)
	assert.Contains(t, output, "source: embedded")
	assert.Contains(t, output, "catalog_version: 2026-04-12.3")
	assert.Contains(t, output, "code-high:")
	assert.Contains(t, output, "agent.openai: gpt-5.4")
	assert.Contains(t, output, "agent.anthropic: opus-4.6")
}

func TestCLI_CatalogCheck_ShowsUpdateAvailable(t *testing.T) {
	workDir := t.TempDir()
	home := t.TempDir()
	manifest := `
version: 2
generated_at: 2026-04-11T00:00:00Z
catalog_version: 2026-04-11.1
profiles:
  code-high:
    target: code-high
targets:
  code-high:
    family: coding-tier
    surfaces:
      agent.openai: gpt-5.4
    surface_policy:
      agent.openai:
        reasoning_default: high
`
	server := newFakeCatalogServer(t, map[string]string{
		"stable/index.json":  catalogIndexJSON("models.yaml", manifest, "2026-04-11.1", 2),
		"stable/models.yaml": manifest,
	})

	out, err := runAgentCLIWithHome(t, home, "--work-dir", workDir, "catalog", "check", "--base-url", server.baseURL())
	require.NoError(t, err, string(out))
	output := string(out)
	assert.Contains(t, output, "remote_catalog_version: 2026-04-11.1")
	assert.Contains(t, output, "status: update-available")
}

func TestCLI_CatalogUpdate_InstallsVerifiedManifest(t *testing.T) {
	workDir := t.TempDir()
	home := t.TempDir()
	manifest := `
version: 2
generated_at: 2026-04-11T00:00:00Z
catalog_version: 2026-04-11.1
profiles:
  code-high:
    target: code-high
targets:
  code-high:
    family: coding-tier
    surfaces:
      agent.openai: gpt-5.4
    surface_policy:
      agent.openai:
        reasoning_default: high
`
	server := newFakeCatalogServer(t, map[string]string{
		"stable/index.json":  catalogIndexJSON("models.yaml", manifest, "2026-04-11.1", 2),
		"stable/models.yaml": manifest,
	})

	out, err := runAgentCLIWithHome(t, home, "--work-dir", workDir, "catalog", "update", "--base-url", server.baseURL())
	require.NoError(t, err, string(out))
	assert.Contains(t, string(out), "installed catalog 2026-04-11.1")

	installedPath := filepath.Join(home, ".config", "agent", "models.yaml")
	data, readErr := os.ReadFile(installedPath)
	require.NoError(t, readErr)
	assert.Contains(t, string(data), "catalog_version: 2026-04-11.1")

	showOut, showErr := runAgentCLIWithHome(t, home, "--work-dir", workDir, "catalog", "show")
	require.NoError(t, showErr, string(showOut))
	assert.Contains(t, string(showOut), "source: "+installedPath)
	assert.Contains(t, string(showOut), "catalog_version: 2026-04-11.1")
}

func TestCLI_CatalogUpdate_RejectsChecksumMismatch(t *testing.T) {
	workDir := t.TempDir()
	home := t.TempDir()
	manifest := `
version: 2
generated_at: 2026-04-11T00:00:00Z
catalog_version: 2026-04-11.1
targets:
  code-high:
    family: coding-tier
    surfaces:
      agent.openai: gpt-5.4
`
	index := `{
  "schema_version": 2,
  "catalog_version": "2026-04-11.1",
  "channel": "stable",
  "published_at": "2026-04-11T12:00:00Z",
  "manifest_path": "models.yaml",
  "manifest_sha256": "deadbeef",
  "min_agent_version": "0.2.0"
}`
	server := newFakeCatalogServer(t, map[string]string{
		"stable/index.json":  index,
		"stable/models.yaml": manifest,
	})

	out, err := runAgentCLIWithHome(t, home, "--work-dir", workDir, "catalog", "update", "--base-url", server.baseURL())
	require.Error(t, err)
	assert.Contains(t, string(out), "checksum mismatch")

	_, statErr := os.Stat(filepath.Join(home, ".config", "agent", "models.yaml"))
	assert.Error(t, statErr)
}

func TestCLI_CatalogUpdate_RejectsUnsupportedSchemaVersion(t *testing.T) {
	workDir := t.TempDir()
	home := t.TempDir()
	manifest := `
version: 5
generated_at: 2026-04-11T00:00:00Z
catalog_version: 2026-04-11.1
targets:
  code-high:
    family: coding-tier
    surfaces:
      agent.openai: gpt-5.4
`
	server := newFakeCatalogServer(t, map[string]string{
		"stable/index.json":  catalogIndexJSON("models.yaml", manifest, "2026-04-11.1", 5),
		"stable/models.yaml": manifest,
	})

	out, err := runAgentCLIWithHome(t, home, "--work-dir", workDir, "catalog", "update", "--base-url", server.baseURL())
	require.Error(t, err)
	assert.Contains(t, string(out), "unsupported schema version 5")

	_, statErr := os.Stat(filepath.Join(home, ".config", "agent", "models.yaml"))
	assert.Error(t, statErr)
}

func TestCLI_Providers_Check_ModelsUseConfiguredProviderWithoutRunningModelResolution(t *testing.T) {
	fake := newFakeOpenAIServer(t)
	workDir := t.TempDir()
	writeTempConfig(t, workDir, `
providers:
  local:
    type: lmstudio
    base_url: `+fake.baseURL()+`
    api_key: test
    model: configured-model
default: local
`)

	out, err := runAgentCLI(t, "--work-dir", workDir, "models")
	require.NoError(t, err, string(out))
	assert.True(t, strings.Contains(string(out), "stub-model") || strings.Contains(string(out), "configured-model"))
	assert.Equal(t, "", fake.lastModel())
}
