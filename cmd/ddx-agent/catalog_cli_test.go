package main_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type recordedChatRequest struct {
	Model string `json:"model"`
}

type fakeOpenAIServer struct {
	server     *httptest.Server
	mu         sync.Mutex
	modelsSeen []string
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
    type: openai-compat
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
    type: openai-compat
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
    type: openai-compat
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
    type: openai-compat
    base_url: `+fake.baseURL()+`
    api_key: test
default: local
`)

	out, err := runAgentCLI(t, "-p", "say hi", "--work-dir", workDir, "--model-ref", "code-fast")
	require.NoError(t, err, string(out))
	assert.Contains(t, string(out), "[success]")
	assert.Equal(t, "override-fast-model", fake.lastModel())
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
    type: openai-compat
    base_url: `+fake.baseURL()+`
    api_key: test
default: local
`)

	out, err := runAgentCLI(t, "-p", "say hi", "--work-dir", workDir, "--model-ref", "code-smart", "--model", "explicit-model")
	require.NoError(t, err, string(out))
	assert.Contains(t, string(out), "[success]")
	assert.Equal(t, "explicit-model", fake.lastModel())
}

func TestCLI_Providers_Check_ModelsUseConfiguredProviderWithoutRunningModelResolution(t *testing.T) {
	fake := newFakeOpenAIServer(t)
	workDir := t.TempDir()
	writeTempConfig(t, workDir, `
providers:
  local:
    type: openai-compat
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
