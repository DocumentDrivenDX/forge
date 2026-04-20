package main_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	agent "github.com/DocumentDrivenDX/agent/internal/core"
	"github.com/DocumentDrivenDX/agent/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type routingChatRequest struct {
	Model string `json:"model"`
}

type countedOpenAIServer struct {
	server          *httptest.Server
	mu              sync.Mutex
	chatCalls       int
	lastModel       string
	responseStatus  int
	modelsStatus    int
	responseModel   string
	responseContent string
	models          []string
	chatDelay       time.Duration
}

func newCountedOpenAIServer(t *testing.T, responseStatus int, responseModel, responseContent string) *countedOpenAIServer {
	t.Helper()
	s := &countedOpenAIServer{
		responseStatus:  responseStatus,
		responseModel:   responseModel,
		responseContent: responseContent,
	}
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			s.mu.Lock()
			models := append([]string(nil), s.models...)
			responseModel := s.responseModel
			modelsStatus := s.modelsStatus
			s.mu.Unlock()
			if modelsStatus == 0 {
				modelsStatus = http.StatusOK
			}
			if modelsStatus != http.StatusOK {
				w.WriteHeader(modelsStatus)
				_, _ = w.Write([]byte(`{"error":{"message":"models unavailable"}}`))
				return
			}
			if len(models) == 0 {
				if responseModel != "" {
					models = []string{responseModel}
				} else {
					models = []string{"stub-model"}
				}
			}
			w.Header().Set("Content-Type", "application/json")
			payload := struct {
				Data []map[string]string `json:"data"`
			}{Data: make([]map[string]string, 0, len(models))}
			for _, model := range models {
				payload.Data = append(payload.Data, map[string]string{"id": model})
			}
			require.NoError(t, json.NewEncoder(w).Encode(payload))
		case "/v1/chat/completions":
			s.mu.Lock()
			s.chatCalls++
			delay := s.chatDelay
			s.mu.Unlock()
			if delay > 0 {
				time.Sleep(delay)
			}

			defer r.Body.Close()
			var req routingChatRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err == nil {
				s.mu.Lock()
				s.lastModel = req.Model
				s.mu.Unlock()
			}

			if s.responseStatus != http.StatusOK {
				w.WriteHeader(s.responseStatus)
				_, _ = w.Write([]byte(`{"error":{"message":"upstream failed"}}`))
				return
			}

			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"id":"chatcmpl-route",
				"object":"chat.completion",
				"created":1712534400,
				"model":"` + s.responseModel + `",
				"choices":[{"index":0,"message":{"role":"assistant","content":"` + s.responseContent + `"},"finish_reason":"stop"}],
				"usage":{"prompt_tokens":8,"completion_tokens":4,"total_tokens":12}
			}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(s.server.Close)
	return s
}

func (s *countedOpenAIServer) baseURL() string {
	return s.server.URL + "/v1"
}

func (s *countedOpenAIServer) chatCallCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.chatCalls
}

func (s *countedOpenAIServer) requestedModel() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastModel
}

func (s *countedOpenAIServer) setModels(models ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.models = append([]string(nil), models...)
}

func (s *countedOpenAIServer) setChatDelay(delay time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.chatDelay = delay
}

func (s *countedOpenAIServer) setModelsStatus(status int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.modelsStatus = status
}

func eventDataByType(t *testing.T, events []agent.Event, eventType agent.EventType) map[string]any {
	t.Helper()
	for _, e := range events {
		if e.Type != eventType {
			continue
		}
		var payload map[string]any
		require.NoError(t, json.Unmarshal(e.Data, &payload))
		return payload
	}
	t.Fatalf("event %s not found", eventType)
	return nil
}

func latestSessionLogPath(t *testing.T, workDir string) string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(workDir, ".agent", "sessions", "*.jsonl"))
	require.NoError(t, err)
	require.NotEmpty(t, paths, "expected at least one session log")

	latest := paths[0]
	latestInfo, err := os.Stat(latest)
	require.NoError(t, err)
	for _, path := range paths[1:] {
		info, statErr := os.Stat(path)
		require.NoError(t, statErr)
		if info.ModTime().After(latestInfo.ModTime()) {
			latest = path
			latestInfo = info
		}
	}
	return latest
}

func writeRoutingHistorySession(t *testing.T, workDir, sessionID string, ts time.Time, data session.SessionEndData) {
	t.Helper()
	logDir := filepath.Join(workDir, ".agent", "sessions")
	require.NoError(t, os.MkdirAll(logDir, 0o755))
	event := agent.Event{
		SessionID: sessionID,
		Seq:       0,
		Type:      agent.EventSessionEnd,
		Timestamp: ts.UTC(),
	}
	raw, err := json.Marshal(data)
	require.NoError(t, err)
	event.Data = raw
	line, err := json.Marshal(event)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(logDir, sessionID+".jsonl"), append(line, '\n'), 0o644))
}

func TestCLI_Run_ModelRouteByModelName(t *testing.T) {
	exe := buildAgentCLI(t)
	workDir := t.TempDir()
	home := t.TempDir()

	bragi := newCountedOpenAIServer(t, http.StatusOK, "bragi-runtime-model", "bragi ok")
	grendel := newCountedOpenAIServer(t, http.StatusOK, "grendel-runtime-model", "grendel ok")

	writeTempConfig(t, workDir, `
providers:
  bragi:
    type: lmstudio
    base_url: `+bragi.baseURL()+`
    api_key: test
  grendel:
    type: lmstudio
    base_url: `+grendel.baseURL()+`
    api_key: test
routing:
  default_model: qwen3.5-27b
model_routes:
  qwen3.5-27b:
    strategy: priority-round-robin
    candidates:
      - provider: bragi
        model: qwen3.5-27b
        priority: 100
      - provider: grendel
        model: qwen3.5-27b
        priority: 100
default: bragi
`)

	type routingResult struct {
		Status            string `json:"status"`
		SessionID         string `json:"session_id"`
		Model             string `json:"model"`
		SelectedProvider  string `json:"selected_provider"`
		SelectedRoute     string `json:"selected_route"`
		RequestedModel    string `json:"requested_model"`
		RequestedModelRef string `json:"requested_model_ref"`
		ResolvedModelRef  string `json:"resolved_model_ref"`
		ResolvedModel     string `json:"resolved_model"`
	}

	first := runBuiltCLI(t, exe, workDir, testEnvWithHome(home, nil), "--json", "--work-dir", workDir, "run", "--model", "qwen3.5-27b", "first request")
	require.Equal(t, 0, first.exitCode, "stderr=%s", first.stderr)
	var firstResult routingResult
	require.NoError(t, json.Unmarshal([]byte(first.stdout), &firstResult), "stdout=%s", first.stdout)
	assert.Equal(t, "success", firstResult.Status)
	assert.Equal(t, "qwen3.5-27b", firstResult.SelectedRoute)
	assert.Equal(t, "bragi", firstResult.SelectedProvider)
	assert.Equal(t, "qwen3.5-27b", firstResult.RequestedModel)
	assert.Equal(t, "", firstResult.RequestedModelRef)
	assert.Equal(t, "qwen3.5-27b", firstResult.ResolvedModel)
	assert.Equal(t, "qwen3.5-27b", bragi.requestedModel())

	firstSessionPath := latestSessionLogPath(t, workDir)
	firstEvents, err := session.ReadEvents(firstSessionPath)
	require.NoError(t, err)
	firstStart := eventDataByType(t, firstEvents, agent.EventSessionStart)
	assert.Equal(t, "qwen3.5-27b", firstStart["requested_model"])
	assert.Equal(t, "qwen3.5-27b", firstStart["selected_route"])
	firstEnd := eventDataByType(t, firstEvents, agent.EventSessionEnd)
	assert.Equal(t, "qwen3.5-27b", firstEnd["requested_model"])
	assert.Equal(t, "bragi", firstEnd["selected_provider"])

	second := runBuiltCLI(t, exe, workDir, testEnvWithHome(home, nil), "--json", "--work-dir", workDir, "run", "second request")
	require.Equal(t, 0, second.exitCode, "stderr=%s", second.stderr)
	var secondResult routingResult
	require.NoError(t, json.Unmarshal([]byte(second.stdout), &secondResult), "stdout=%s", second.stdout)
	assert.Equal(t, "qwen3.5-27b", secondResult.SelectedRoute)
	assert.Equal(t, "grendel", secondResult.SelectedProvider)
	assert.Equal(t, "qwen3.5-27b", secondResult.RequestedModel)
	assert.Equal(t, "qwen3.5-27b", secondResult.ResolvedModel)
	assert.Equal(t, "qwen3.5-27b", grendel.requestedModel())

	secondSessionPath := latestSessionLogPath(t, workDir)
	secondEvents, err := session.ReadEvents(secondSessionPath)
	require.NoError(t, err)
	secondEnd := eventDataByType(t, secondEvents, agent.EventSessionEnd)
	assert.Equal(t, "qwen3.5-27b", secondEnd["requested_model"])
	assert.Equal(t, "grendel", secondEnd["selected_provider"])

	assert.Equal(t, 1, bragi.chatCallCount())
	assert.Equal(t, 1, grendel.chatCallCount())
}

func TestCLI_ModelRouteFailoverOnAvailabilityError(t *testing.T) {
	exe := buildAgentCLI(t)
	workDir := t.TempDir()
	home := t.TempDir()

	dead := newCountedOpenAIServer(t, http.StatusServiceUnavailable, "", "")
	healthy := newCountedOpenAIServer(t, http.StatusOK, "healthy-runtime-model", "healthy ok")

	writeTempConfig(t, workDir, `
providers:
  bragi:
    type: lmstudio
    base_url: `+dead.baseURL()+`
    api_key: test
  openrouter:
    type: lmstudio
    base_url: `+healthy.baseURL()+`
    api_key: test
model_routes:
  qwen3.5-27b:
    strategy: ordered-failover
    candidates:
      - provider: bragi
        model: qwen3.5-27b
        priority: 100
      - provider: openrouter
        model: qwen/qwen3.5-27b
        priority: 10
`)

	type routingResult struct {
		Status             string   `json:"status"`
		Model              string   `json:"model"`
		SelectedProvider   string   `json:"selected_provider"`
		SelectedRoute      string   `json:"selected_route"`
		RequestedModel     string   `json:"requested_model"`
		AttemptedProviders []string `json:"attempted_providers"`
		FailoverCount      int      `json:"failover_count"`
	}

	res := runBuiltCLI(t, exe, workDir, testEnvWithHome(home, nil), "--json", "--work-dir", workDir, "run", "--model", "qwen3.5-27b", "route failover")
	require.Equal(t, 0, res.exitCode, "stderr=%s", res.stderr)

	var parsed routingResult
	require.NoError(t, json.Unmarshal([]byte(res.stdout), &parsed), "stdout=%s", res.stdout)
	assert.Equal(t, "success", parsed.Status)
	assert.Equal(t, "qwen3.5-27b", parsed.SelectedRoute)
	assert.Equal(t, "openrouter", parsed.SelectedProvider)
	assert.Equal(t, "qwen3.5-27b", parsed.RequestedModel)
	assert.Equal(t, []string{"bragi", "openrouter"}, parsed.AttemptedProviders)
	assert.Equal(t, 1, parsed.FailoverCount)

	sessionPath := latestSessionLogPath(t, workDir)
	events, err := session.ReadEvents(sessionPath)
	require.NoError(t, err)
	end := eventDataByType(t, events, agent.EventSessionEnd)
	assert.Equal(t, "openrouter", end["selected_provider"])
	assert.Equal(t, float64(1), end["failover_count"])

	assert.Equal(t, 1, dead.chatCallCount())
	assert.Equal(t, 1, healthy.chatCallCount())
}

func TestCLI_ModelRouteDoesNotFailoverOnDeterministic400(t *testing.T) {
	exe := buildAgentCLI(t)
	workDir := t.TempDir()
	home := t.TempDir()

	badRequest := newCountedOpenAIServer(t, http.StatusBadRequest, "", "")
	healthy := newCountedOpenAIServer(t, http.StatusOK, "healthy-runtime-model", "healthy ok")

	writeTempConfig(t, workDir, `
providers:
  bragi:
    type: lmstudio
    base_url: `+badRequest.baseURL()+`
    api_key: test
  openrouter:
    type: lmstudio
    base_url: `+healthy.baseURL()+`
    api_key: test
model_routes:
  qwen3.5-27b:
    strategy: ordered-failover
    candidates:
      - provider: bragi
        model: qwen3.5-27b
      - provider: openrouter
        model: qwen/qwen3.5-27b
`)

	res := runBuiltCLI(t, exe, workDir, testEnvWithHome(home, nil), "--work-dir", workDir, "run", "--model", "qwen3.5-27b", "no failover")
	require.Equal(t, 1, res.exitCode, "stdout=%s stderr=%s", res.stdout, res.stderr)
	assert.Contains(t, res.stderr, "agent: provider error")
	assert.Equal(t, 1, badRequest.chatCallCount(), "400 is non-transient: runtime should fail immediately without retry")
	assert.Equal(t, 0, healthy.chatCallCount())
}

func TestCLI_ModelIntentAutoRoutingSkipsUnhealthyDefaultAndChoosesBestHealthyProvider(t *testing.T) {
	exe := buildAgentCLI(t)
	workDir := t.TempDir()
	home := t.TempDir()

	dead := newCountedOpenAIServer(t, http.StatusServiceUnavailable, "", "")
	bragi := newCountedOpenAIServer(t, http.StatusOK, "qwen3.5-27b", "bragi ok")
	vidar := newCountedOpenAIServer(t, http.StatusOK, "qwen3.5-27b", "vidar ok")
	openrouter := newCountedOpenAIServer(t, http.StatusOK, "qwen/qwen3.5-27b-20260224", "openrouter ok")

	dead.setModelsStatus(http.StatusServiceUnavailable)
	dead.setModels("qwen3.5-27b")
	bragi.setModels("qwen3.5-27b")
	vidar.setModels("qwen3.5-27b")
	openrouter.setModels("qwen/qwen3.5-27b-20260224")

	knownCost := 0.09
	writeRoutingHistorySession(t, workDir, "vidar-win", time.Now().Add(-5*time.Minute), session.SessionEndData{
		Status:           agent.StatusSuccess,
		Tokens:           agent.TokenUsage{Input: 100, Output: 50, Total: 150},
		CostUSD:          nil,
		DurationMs:       800,
		SelectedProvider: "vidar",
		RequestedModel:   "qwen3.5-27b",
		ResolvedModel:    "qwen3.5-27b",
	})
	writeRoutingHistorySession(t, workDir, "bragi-slow", time.Now().Add(-4*time.Minute), session.SessionEndData{
		Status:           agent.StatusSuccess,
		Tokens:           agent.TokenUsage{Input: 100, Output: 50, Total: 150},
		CostUSD:          nil,
		DurationMs:       2400,
		SelectedProvider: "bragi",
		RequestedModel:   "qwen3.5-27b",
		ResolvedModel:    "qwen3.5-27b",
	})
	writeRoutingHistorySession(t, workDir, "openrouter-costly", time.Now().Add(-3*time.Minute), session.SessionEndData{
		Status:           agent.StatusSuccess,
		Tokens:           agent.TokenUsage{Input: 100, Output: 50, Total: 150},
		CostUSD:          &knownCost,
		DurationMs:       1500,
		SelectedProvider: "openrouter",
		RequestedModel:   "qwen3.5-27b",
		ResolvedModel:    "qwen/qwen3.5-27b-20260224",
	})

	writeTempConfig(t, workDir, `
providers:
  openrouter:
    type: lmstudio
    base_url: `+dead.baseURL()+`
    api_key: test
  bragi:
    type: lmstudio
    base_url: `+bragi.baseURL()+`
    api_key: test
  vidar:
    type: lmstudio
    base_url: `+vidar.baseURL()+`
    api_key: test
  grendel:
    type: lmstudio
    base_url: `+openrouter.baseURL()+`
    api_key: test
default: openrouter
routing:
  default_model: qwen3.5-27b
`)

	type routingResult struct {
		Status           string   `json:"status"`
		SelectedProvider string   `json:"selected_provider"`
		SelectedRoute    string   `json:"selected_route"`
		RequestedModel   string   `json:"requested_model"`
		Attempted        []string `json:"attempted_providers"`
	}

	res := runBuiltCLI(t, exe, workDir, testEnvWithHome(home, nil), "--json", "--work-dir", workDir, "run", "--model", "qwen3.5-27b", "smart route")
	require.Equal(t, 0, res.exitCode, "stdout=%s stderr=%s", res.stdout, res.stderr)

	var parsed routingResult
	require.NoError(t, json.Unmarshal([]byte(res.stdout), &parsed), "stdout=%s", res.stdout)
	assert.Equal(t, "success", parsed.Status)
	assert.Equal(t, "vidar", parsed.SelectedProvider)
	assert.Equal(t, "qwen3.5-27b", parsed.SelectedRoute)
	assert.Equal(t, "qwen3.5-27b", parsed.RequestedModel)
	assert.Equal(t, []string{"vidar"}, parsed.Attempted)
	assert.Equal(t, 0, dead.chatCallCount(), "unhealthy default provider should be excluded before execution")
	assert.Equal(t, 0, bragi.chatCallCount(), "slower healthy provider should lose to better observed candidate")
	assert.Equal(t, 1, vidar.chatCallCount())
	assert.Equal(t, 0, openrouter.chatCallCount(), "higher-cost healthy provider should lose when a faster healthy local candidate exists")
}

func TestCLI_RouteStatusShowsHealthAndScoringForModelIntent(t *testing.T) {
	exe := buildAgentCLI(t)
	workDir := t.TempDir()
	home := t.TempDir()

	dead := newCountedOpenAIServer(t, http.StatusServiceUnavailable, "", "")
	healthy := newCountedOpenAIServer(t, http.StatusOK, "qwen3.5-27b", "ok")
	expensive := newCountedOpenAIServer(t, http.StatusOK, "qwen/qwen3.5-27b-20260224", "ok")
	dead.setModelsStatus(http.StatusServiceUnavailable)
	dead.setModels("qwen3.5-27b")
	healthy.setModels("qwen3.5-27b")
	expensive.setModels("qwen/qwen3.5-27b-20260224")

	expensiveCost := 0.12
	writeRoutingHistorySession(t, workDir, "healthy-history", time.Now().Add(-2*time.Minute), session.SessionEndData{
		Status:           agent.StatusSuccess,
		Tokens:           agent.TokenUsage{Input: 100, Output: 50, Total: 150},
		DurationMs:       900,
		SelectedProvider: "vidar",
		RequestedModel:   "qwen3.5-27b",
		ResolvedModel:    "qwen3.5-27b",
	})
	writeRoutingHistorySession(t, workDir, "expensive-history", time.Now().Add(-2*time.Minute), session.SessionEndData{
		Status:           agent.StatusSuccess,
		Tokens:           agent.TokenUsage{Input: 100, Output: 50, Total: 150},
		CostUSD:          &expensiveCost,
		DurationMs:       1200,
		SelectedProvider: "openrouter",
		RequestedModel:   "qwen3.5-27b",
		ResolvedModel:    "qwen/qwen3.5-27b-20260224",
	})

	writeTempConfig(t, workDir, `
providers:
  bragi:
    type: lmstudio
    base_url: `+dead.baseURL()+`
    api_key: test
  vidar:
    type: lmstudio
    base_url: `+healthy.baseURL()+`
    api_key: test
  openrouter:
    type: lmstudio
    base_url: `+expensive.baseURL()+`
    api_key: test
`)

	out := runBuiltCLI(t, exe, workDir, testEnvWithHome(home, nil), "--work-dir", workDir, "route-status", "--model", "qwen3.5-27b", "--json")
	require.Equal(t, 0, out.exitCode, "stdout=%s stderr=%s", out.stdout, out.stderr)

	var parsed struct {
		RouteKey         string `json:"route_key"`
		SelectedProvider string `json:"selected_provider"`
		Candidates       []struct {
			Provider string  `json:"provider"`
			Model    string  `json:"model"`
			Healthy  bool    `json:"healthy"`
			Reason   string  `json:"reason"`
			Score    float64 `json:"score"`
		} `json:"candidates"`
	}
	require.NoError(t, json.Unmarshal([]byte(out.stdout), &parsed), "stdout=%s", out.stdout)
	assert.Equal(t, "qwen3.5-27b", parsed.RouteKey)
	assert.Equal(t, "vidar", parsed.SelectedProvider)
	require.Len(t, parsed.Candidates, 3)
	assert.Equal(t, "vidar", parsed.Candidates[0].Provider)
	assert.True(t, parsed.Candidates[0].Healthy)
	assert.Equal(t, "openrouter", parsed.Candidates[1].Provider)
	assert.True(t, parsed.Candidates[1].Healthy)
	assert.Equal(t, "bragi", parsed.Candidates[2].Provider)
	assert.False(t, parsed.Candidates[2].Healthy)
	assert.NotZero(t, parsed.Candidates[0].Score)
	assert.Contains(t, parsed.Candidates[2].Reason, "503")
}

func TestCLI_BackendRoutingAttributionFlowsIntoResultAndSession(t *testing.T) {
	exe := buildAgentCLI(t)
	workDir := t.TempDir()
	home := t.TempDir()

	vidar := newCountedOpenAIServer(t, http.StatusOK, "vidar-runtime-model", "vidar ok")
	bragi := newCountedOpenAIServer(t, http.StatusOK, "bragi-runtime-model", "bragi ok")

	writeTempConfig(t, workDir, `
providers:
  vidar:
    type: lmstudio
    base_url: `+vidar.baseURL()+`
    api_key: test
  bragi:
    type: lmstudio
    base_url: `+bragi.baseURL()+`
    api_key: test
backends:
  code-pool:
    model_ref: code-fast
    providers: [vidar, bragi]
    strategy: round-robin
default_backend: code-pool
`)

	type routingResult struct {
		Status           string `json:"status"`
		SessionID        string `json:"session_id"`
		Model            string `json:"model"`
		SelectedProvider string `json:"selected_provider"`
		SelectedRoute    string `json:"selected_route"`
		ResolvedModelRef string `json:"resolved_model_ref"`
		ResolvedModel    string `json:"resolved_model"`
	}

	first := runBuiltCLI(t, exe, workDir, testEnvWithHome(home, nil), "--json", "--work-dir", workDir, "-p", "first request")
	require.Equal(t, 0, first.exitCode, "stderr=%s", first.stderr)
	var firstResult routingResult
	require.NoError(t, json.Unmarshal([]byte(first.stdout), &firstResult), "stdout=%s", first.stdout)
	assert.Equal(t, "success", firstResult.Status)
	assert.Equal(t, "code-pool", firstResult.SelectedRoute)
	assert.Equal(t, "vidar", firstResult.SelectedProvider)
	assert.Equal(t, "code-medium", firstResult.ResolvedModelRef)
	assert.Equal(t, "gpt-5.4-mini", firstResult.ResolvedModel)
	assert.Equal(t, "gpt-5.4-mini", firstResult.Model)
	assert.Equal(t, "gpt-5.4-mini", vidar.requestedModel())

	firstSessionPath := latestSessionLogPath(t, workDir)
	firstEvents, err := session.ReadEvents(firstSessionPath)
	require.NoError(t, err)
	firstStart := eventDataByType(t, firstEvents, agent.EventSessionStart)
	assert.Equal(t, "vidar", firstStart["selected_provider"])
	assert.Equal(t, "code-pool", firstStart["selected_route"])
	assert.Equal(t, "code-medium", firstStart["resolved_model_ref"])
	assert.Equal(t, "gpt-5.4-mini", firstStart["resolved_model"])
	firstEnd := eventDataByType(t, firstEvents, agent.EventSessionEnd)
	assert.Equal(t, "vidar", firstEnd["selected_provider"])
	assert.Equal(t, "code-pool", firstEnd["selected_route"])
	assert.Equal(t, "code-medium", firstEnd["resolved_model_ref"])
	assert.Equal(t, "gpt-5.4-mini", firstEnd["resolved_model"])

	second := runBuiltCLI(t, exe, workDir, testEnvWithHome(home, nil), "--json", "--work-dir", workDir, "-p", "second request")
	require.Equal(t, 0, second.exitCode, "stderr=%s", second.stderr)
	var secondResult routingResult
	require.NoError(t, json.Unmarshal([]byte(second.stdout), &secondResult), "stdout=%s", second.stdout)
	assert.Equal(t, "success", secondResult.Status)
	assert.Equal(t, "code-pool", secondResult.SelectedRoute)
	assert.Equal(t, "bragi", secondResult.SelectedProvider)
	assert.Equal(t, "code-medium", secondResult.ResolvedModelRef)
	assert.Equal(t, "gpt-5.4-mini", secondResult.ResolvedModel)
	assert.Equal(t, "gpt-5.4-mini", secondResult.Model)
	assert.Equal(t, "gpt-5.4-mini", bragi.requestedModel())

	secondSessionPath := latestSessionLogPath(t, workDir)
	secondEvents, err := session.ReadEvents(secondSessionPath)
	require.NoError(t, err)
	secondStart := eventDataByType(t, secondEvents, agent.EventSessionStart)
	assert.Equal(t, "bragi", secondStart["selected_provider"])
	assert.Equal(t, "code-pool", secondStart["selected_route"])
	assert.Equal(t, "code-medium", secondStart["resolved_model_ref"])
	assert.Equal(t, "gpt-5.4-mini", secondStart["resolved_model"])
	secondEnd := eventDataByType(t, secondEvents, agent.EventSessionEnd)
	assert.Equal(t, "bragi", secondEnd["selected_provider"])
	assert.Equal(t, "code-pool", secondEnd["selected_route"])
	assert.Equal(t, "code-medium", secondEnd["resolved_model_ref"])
	assert.Equal(t, "gpt-5.4-mini", secondEnd["resolved_model"])

	assert.Equal(t, 1, vidar.chatCallCount())
	assert.Equal(t, 1, bragi.chatCallCount())
}

func TestCLI_BackendPoolFailureDoesNotFailoverToAnotherProvider(t *testing.T) {
	exe := buildAgentCLI(t)
	workDir := t.TempDir()
	home := t.TempDir()

	dead := newCountedOpenAIServer(t, http.StatusServiceUnavailable, "", "")
	healthy := newCountedOpenAIServer(t, http.StatusOK, "healthy-runtime-model", "healthy ok")

	writeTempConfig(t, workDir, `
providers:
  dead:
    type: lmstudio
    base_url: `+dead.baseURL()+`
    api_key: test
    model: local-dead
  healthy:
    type: lmstudio
    base_url: `+healthy.baseURL()+`
    api_key: test
    model: local-healthy
backends:
  local-pool:
    providers: [dead, healthy]
    strategy: first-available
default_backend: local-pool
`)

	res := runBuiltCLI(t, exe, workDir, testEnvWithHome(home, nil), "--work-dir", workDir, "-p", "fail request")
	require.Equal(t, 1, res.exitCode, "stdout=%s stderr=%s", res.stdout, res.stderr)
	assert.Contains(t, res.stderr, "agent: provider error")
	assert.Equal(t, 0, healthy.chatCallCount(), "phase 1 / 2A should not fail over to secondary provider")
	assert.Equal(t, 5, dead.chatCallCount(), "503 is transient: runtime should retry selected provider up to 5 times")
}
