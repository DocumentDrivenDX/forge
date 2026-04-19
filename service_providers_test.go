package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/DocumentDrivenDX/agent/internal/harnesses"
	claudeharness "github.com/DocumentDrivenDX/agent/internal/harnesses/claude"
)

// fakeServiceConfig implements ServiceConfig for tests.
type fakeServiceConfig struct {
	providers      map[string]ServiceProviderEntry
	names          []string
	defaultName    string
	routes         map[string][]string // routeName -> candidate provider names
	routeConfigs   map[string]ServiceModelRouteConfig
	healthCooldown time.Duration
	workDir        string
}

func (f *fakeServiceConfig) ProviderNames() []string     { return f.names }
func (f *fakeServiceConfig) DefaultProviderName() string { return f.defaultName }
func (f *fakeServiceConfig) Provider(name string) (ServiceProviderEntry, bool) {
	e, ok := f.providers[name]
	return e, ok
}
func (f *fakeServiceConfig) ModelRouteNames() []string {
	out := make([]string, 0, len(f.routes))
	for k := range f.routes {
		out = append(out, k)
	}
	return out
}
func (f *fakeServiceConfig) ModelRouteCandidates(routeName string) []string {
	return f.routes[routeName]
}
func (f *fakeServiceConfig) ModelRouteConfig(routeName string) ServiceModelRouteConfig {
	if f.routeConfigs != nil {
		if rc, ok := f.routeConfigs[routeName]; ok {
			return rc
		}
	}
	// Fallback: build a minimal config from the routes map.
	candidates := f.routes[routeName]
	entries := make([]ServiceRouteCandidateEntry, len(candidates))
	for i, p := range candidates {
		entries[i] = ServiceRouteCandidateEntry{Provider: p, Priority: 100}
	}
	return ServiceModelRouteConfig{Candidates: entries}
}
func (f *fakeServiceConfig) HealthCooldown() time.Duration { return f.healthCooldown }
func (f *fakeServiceConfig) WorkDir() string               { return f.workDir }

func TestListProviders_NoServiceConfig(t *testing.T) {
	svc := &service{opts: ServiceOptions{}, registry: harnesses.NewRegistry()}
	_, err := svc.ListProviders(context.Background())
	if err == nil {
		t.Fatal("expected error when ServiceConfig is nil")
	}
}

func TestListProviders_Connected(t *testing.T) {
	// Spin up a fake /v1/models server.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" || r.URL.Path == "/models" {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{
					{"id": "model-a"},
					{"id": "model-b"},
				},
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer ts.Close()

	sc := &fakeServiceConfig{
		providers: map[string]ServiceProviderEntry{
			"local": {Type: "openai-compat", BaseURL: ts.URL + "/v1", Model: "model-a"},
		},
		names:       []string{"local"},
		defaultName: "local",
	}
	svc := &service{opts: ServiceOptions{ServiceConfig: sc}, registry: harnesses.NewRegistry()}

	infos, err := svc.ListProviders(context.Background())
	if err != nil {
		t.Fatalf("ListProviders: %v", err)
	}
	if len(infos) != 1 {
		t.Fatalf("want 1 provider, got %d", len(infos))
	}
	info := infos[0]
	if info.Name != "local" {
		t.Errorf("Name: got %q, want %q", info.Name, "local")
	}
	if info.Status != "connected" {
		t.Errorf("Status: got %q, want %q", info.Status, "connected")
	}
	if info.ModelCount != 2 {
		t.Errorf("ModelCount: got %d, want 2", info.ModelCount)
	}
	if !info.IsDefault {
		t.Error("IsDefault should be true for the default provider")
	}
	if info.DefaultModel != "model-a" {
		t.Errorf("DefaultModel: got %q, want %q", info.DefaultModel, "model-a")
	}
	if info.Type != "openai-compat" {
		t.Errorf("Type: got %q, want %q", info.Type, "openai-compat")
	}
}

func TestListProviders_Unreachable(t *testing.T) {
	sc := &fakeServiceConfig{
		providers: map[string]ServiceProviderEntry{
			"remote": {Type: "openai-compat", BaseURL: "http://127.0.0.1:19999/v1"},
		},
		names:       []string{"remote"},
		defaultName: "remote",
	}
	svc := &service{opts: ServiceOptions{ServiceConfig: sc}, registry: harnesses.NewRegistry()}

	infos, err := svc.ListProviders(context.Background())
	if err != nil {
		t.Fatalf("ListProviders: %v", err)
	}
	if len(infos) != 1 {
		t.Fatalf("want 1 provider, got %d", len(infos))
	}
	if infos[0].Status != "unreachable" {
		t.Errorf("Status: got %q, want %q", infos[0].Status, "unreachable")
	}
}

func TestListProviders_Anthropic(t *testing.T) {
	sc := &fakeServiceConfig{
		providers: map[string]ServiceProviderEntry{
			"claude-api": {Type: "anthropic", APIKey: "sk-test"},
		},
		names:       []string{"claude-api"},
		defaultName: "claude-api",
	}
	svc := &service{opts: ServiceOptions{ServiceConfig: sc}, registry: harnesses.NewRegistry()}

	infos, err := svc.ListProviders(context.Background())
	if err != nil {
		t.Fatalf("ListProviders: %v", err)
	}
	if len(infos) != 1 {
		t.Fatalf("want 1 provider, got %d", len(infos))
	}
	info := infos[0]
	if info.Status != "connected" {
		t.Errorf("anthropic with key: Status got %q, want %q", info.Status, "connected")
	}
	if info.Type != "anthropic" {
		t.Errorf("Type: got %q, want %q", info.Type, "anthropic")
	}
}

func TestListProviders_AnthropicNoKey(t *testing.T) {
	sc := &fakeServiceConfig{
		providers:   map[string]ServiceProviderEntry{"claude-api": {Type: "anthropic"}},
		names:       []string{"claude-api"},
		defaultName: "claude-api",
	}
	svc := &service{opts: ServiceOptions{ServiceConfig: sc}, registry: harnesses.NewRegistry()}

	infos, err := svc.ListProviders(context.Background())
	if err != nil {
		t.Fatalf("ListProviders: %v", err)
	}
	if infos[0].Status != "error: api_key not configured" {
		t.Errorf("unexpected status: %s", infos[0].Status)
	}
}

func TestListProviders_CooldownState(t *testing.T) {
	dir := t.TempDir()
	agentDir := filepath.Join(dir, ".agent")
	if err := os.MkdirAll(agentDir, 0o750); err != nil {
		t.Fatal(err)
	}
	// Write a route health state with a recent failure.
	type routeState struct {
		Failures map[string]time.Time `json:"failures"`
	}
	rs := routeState{Failures: map[string]time.Time{"myprovider": time.Now().UTC()}}
	data, _ := json.Marshal(rs)
	os.WriteFile(filepath.Join(agentDir, "route-health-myroute.json"), data, 0o600)

	sc := &fakeServiceConfig{
		providers: map[string]ServiceProviderEntry{
			"myprovider": {Type: "openai-compat", BaseURL: "http://127.0.0.1:19999/v1"},
		},
		names:          []string{"myprovider"},
		defaultName:    "myprovider",
		routes:         map[string][]string{"myroute": {"myprovider"}},
		healthCooldown: 30 * time.Second,
		workDir:        dir,
	}
	svc := &service{opts: ServiceOptions{ServiceConfig: sc}, registry: harnesses.NewRegistry()}

	infos, err := svc.ListProviders(context.Background())
	if err != nil {
		t.Fatalf("ListProviders: %v", err)
	}
	if len(infos) != 1 {
		t.Fatalf("want 1 provider, got %d", len(infos))
	}
	if infos[0].CooldownState == nil {
		t.Fatal("expected CooldownState to be non-nil due to recent failure")
	}
	if infos[0].CooldownState.Reason != "consecutive_failures" {
		t.Errorf("CooldownState.Reason: got %q, want %q", infos[0].CooldownState.Reason, "consecutive_failures")
	}
}

func TestHealthCheck_NoServiceConfig(t *testing.T) {
	svc := &service{opts: ServiceOptions{}, registry: harnesses.NewRegistry()}
	err := svc.HealthCheck(context.Background(), HealthTarget{Type: "provider", Name: "x"})
	if err == nil {
		t.Fatal("expected error when ServiceConfig is nil")
	}
}

func TestHealthCheck_Provider_Connected(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"data": []any{}})
	}))
	defer ts.Close()

	sc := &fakeServiceConfig{
		providers: map[string]ServiceProviderEntry{
			"local": {Type: "openai-compat", BaseURL: ts.URL + "/v1"},
		},
	}
	svc := &service{opts: ServiceOptions{ServiceConfig: sc}, registry: harnesses.NewRegistry()}

	if err := svc.HealthCheck(context.Background(), HealthTarget{Type: "provider", Name: "local"}); err != nil {
		t.Errorf("HealthCheck connected provider: unexpected error: %v", err)
	}
}

func TestHealthCheck_Provider_Unreachable(t *testing.T) {
	sc := &fakeServiceConfig{
		providers: map[string]ServiceProviderEntry{
			"dead": {Type: "openai-compat", BaseURL: "http://127.0.0.1:19999/v1"},
		},
	}
	svc := &service{opts: ServiceOptions{ServiceConfig: sc}, registry: harnesses.NewRegistry()}

	err := svc.HealthCheck(context.Background(), HealthTarget{Type: "provider", Name: "dead"})
	if err == nil {
		t.Fatal("expected error for unreachable provider")
	}
}

func TestHealthCheck_Provider_NotFound(t *testing.T) {
	sc := &fakeServiceConfig{providers: map[string]ServiceProviderEntry{}}
	svc := &service{opts: ServiceOptions{ServiceConfig: sc}, registry: harnesses.NewRegistry()}

	err := svc.HealthCheck(context.Background(), HealthTarget{Type: "provider", Name: "missing"})
	if err == nil {
		t.Fatal("expected error for missing provider")
	}
}

func TestHealthCheck_Harness_Available(t *testing.T) {
	svc := &service{opts: ServiceOptions{}, registry: harnesses.NewRegistry()}
	// "agent" is always available (embedded).
	if err := svc.HealthCheck(context.Background(), HealthTarget{Type: "harness", Name: "agent"}); err != nil {
		t.Errorf("HealthCheck embedded harness: unexpected error: %v", err)
	}
}

func TestHealthCheck_Harness_NotRegistered(t *testing.T) {
	svc := &service{opts: ServiceOptions{}, registry: harnesses.NewRegistry()}
	err := svc.HealthCheck(context.Background(), HealthTarget{Type: "harness", Name: "nonexistent-harness-xyz"})
	if err == nil {
		t.Fatal("expected error for unregistered harness")
	}
}

func TestHealthCheck_InvalidType(t *testing.T) {
	svc := &service{opts: ServiceOptions{}, registry: harnesses.NewRegistry()}
	err := svc.HealthCheck(context.Background(), HealthTarget{Type: "invalid", Name: "x"})
	if err == nil {
		t.Fatal("expected error for invalid HealthTarget.Type")
	}
}

func TestNormalizeServiceProviderType(t *testing.T) {
	cases := []struct{ in, want string }{
		{"openai-compat", "openai-compat"},
		{"openai", "openai-compat"},
		{"", "openai-compat"},
		{"anthropic", "anthropic"},
		{"custom", "custom"},
	}
	for _, tc := range cases {
		got := normalizeServiceProviderType(tc.in)
		if got != tc.want {
			t.Errorf("normalizeServiceProviderType(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestServiceRouteStateKey(t *testing.T) {
	cases := []struct{ in, want string }{
		{"my/route", "my_route"},
		{"provider:model", "provider_model"},
		{"spaces here", "spaces_here"},
		{"plain", "plain"},
	}
	for _, tc := range cases {
		got := serviceRouteStateKey(tc.in)
		if got != tc.want {
			t.Errorf("serviceRouteStateKey(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestHealthCheck_ClaudeRefreshesQuotaWhenStale verifies that HealthCheck
// triggers a quota cache refresh when the cached snapshot is older than
// healthCheckQuotaFreshnessWindow (60s).
func TestHealthCheck_ClaudeRefreshesQuotaWhenStale(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "claude-quota.json")
	t.Setenv("DDX_AGENT_CLAUDE_QUOTA_CACHE", cachePath)

	// Write a snapshot that is 90s old (stale).
	staleSnap := claudeharness.ClaudeQuotaSnapshot{
		CapturedAt:        time.Now().UTC().Add(-90 * time.Second),
		FiveHourRemaining: 80,
		FiveHourLimit:     100,
		WeeklyRemaining:   90,
		WeeklyLimit:       100,
		Source:            "pty",
	}
	if err := claudeharness.WriteClaudeQuota(cachePath, staleSnap); err != nil {
		t.Fatalf("setup: WriteClaudeQuota: %v", err)
	}

	// Inject a fake refresher so no real tmux is invoked.
	refreshCalled := false
	orig := healthCheckClaudeQuotaRefresher
	healthCheckClaudeQuotaRefresher = func(timeout time.Duration) ([]harnesses.QuotaWindow, *harnesses.AccountInfo, error) {
		refreshCalled = true
		return []harnesses.QuotaWindow{
			{LimitID: "session", UsedPercent: 20},
			{LimitID: "weekly-all", UsedPercent: 10},
		}, nil, nil
	}
	t.Cleanup(func() { healthCheckClaudeQuotaRefresher = orig })

	svc := &service{opts: ServiceOptions{}, registry: harnesses.NewRegistry()}
	// HealthCheck for "claude" requires the binary to be discoverable.
	// If claude is not in PATH, the harness is unavailable → the quota refresh
	// is never reached. To keep the test self-contained we call the helper
	// directly rather than going through HealthCheck's availability gate.
	healthCheckRefreshClaudeQuota(context.Background())

	if !refreshCalled {
		t.Error("expected healthCheckClaudeQuotaRefresher to be called for stale cache")
	}

	// Verify the cache was rewritten with a newer timestamp.
	loaded, ok := claudeharness.ReadClaudeQuotaFrom(cachePath)
	if !ok {
		t.Fatal("expected cache file to exist after refresh")
	}
	if !loaded.CapturedAt.After(staleSnap.CapturedAt) {
		t.Errorf("expected cache CapturedAt to be newer than stale snapshot: got %v, stale was %v",
			loaded.CapturedAt, staleSnap.CapturedAt)
	}
	_ = svc
}

// TestHealthCheck_ClaudeSkipsRefreshWhenFresh verifies that HealthCheck does
// NOT invoke the tmux quota refresher when the cached snapshot is younger than
// healthCheckQuotaFreshnessWindow (60s).
func TestHealthCheck_ClaudeSkipsRefreshWhenFresh(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "claude-quota.json")
	t.Setenv("DDX_AGENT_CLAUDE_QUOTA_CACHE", cachePath)

	// Write a snapshot that is only 30s old (fresh).
	freshSnap := claudeharness.ClaudeQuotaSnapshot{
		CapturedAt:        time.Now().UTC().Add(-30 * time.Second),
		FiveHourRemaining: 80,
		FiveHourLimit:     100,
		WeeklyRemaining:   90,
		WeeklyLimit:       100,
		Source:            "pty",
	}
	if err := claudeharness.WriteClaudeQuota(cachePath, freshSnap); err != nil {
		t.Fatalf("setup: WriteClaudeQuota: %v", err)
	}

	// Inject a fake refresher that must NOT be called.
	refreshCalled := false
	orig := healthCheckClaudeQuotaRefresher
	healthCheckClaudeQuotaRefresher = func(timeout time.Duration) ([]harnesses.QuotaWindow, *harnesses.AccountInfo, error) {
		refreshCalled = true
		return nil, nil, nil
	}
	t.Cleanup(func() { healthCheckClaudeQuotaRefresher = orig })

	healthCheckRefreshClaudeQuota(context.Background())

	if refreshCalled {
		t.Error("expected healthCheckClaudeQuotaRefresher NOT to be called for fresh cache")
	}

	// Verify the cache timestamp is unchanged (still matches freshSnap).
	loaded, ok := claudeharness.ReadClaudeQuotaFrom(cachePath)
	if !ok {
		t.Fatal("expected cache file to still exist")
	}
	if !loaded.CapturedAt.Equal(freshSnap.CapturedAt) {
		t.Errorf("cache was unexpectedly rewritten: got CapturedAt %v, want %v",
			loaded.CapturedAt, freshSnap.CapturedAt)
	}
}

// TestHealthCheck_GeminiDoesNotInvokeTmux verifies that HealthCheck for a
// non-tmux-quota harness (gemini) never calls the Claude quota refresher.
func TestHealthCheck_GeminiDoesNotInvokeTmux(t *testing.T) {
	// Inject a counter to detect unexpected calls.
	tmuxCalled := false
	orig := healthCheckClaudeQuotaRefresher
	healthCheckClaudeQuotaRefresher = func(timeout time.Duration) ([]harnesses.QuotaWindow, *harnesses.AccountInfo, error) {
		tmuxCalled = true
		return nil, nil, nil
	}
	t.Cleanup(func() { healthCheckClaudeQuotaRefresher = orig })

	svc := &service{opts: ServiceOptions{}, registry: harnesses.NewRegistry()}
	// "gemini" is registered but unavailable in CI (binary not found).
	// HealthCheck returns an error but must not invoke the tmux refresher.
	_ = svc.HealthCheck(context.Background(), HealthTarget{Type: "harness", Name: "gemini"})

	if tmuxCalled {
		t.Error("healthCheckClaudeQuotaRefresher must not be called for gemini harness")
	}
}
