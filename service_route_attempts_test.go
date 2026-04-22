package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/DocumentDrivenDX/agent/internal/harnesses"
	codexharness "github.com/DocumentDrivenDX/agent/internal/harnesses/codex"
	"github.com/DocumentDrivenDX/agent/internal/routing"
)

func TestRecordRouteAttempt_DemotesFailedProvider(t *testing.T) {
	svc := routeAttemptTestService(30 * time.Second)

	before, err := svc.ResolveRoute(context.Background(), RouteRequest{
		Model:    "qwen",
		Provider: "bragi",
	})
	if err != nil {
		t.Fatalf("ResolveRoute before failure: %v", err)
	}
	if before.Provider != "bragi" {
		t.Fatalf("before failure Provider: got %q, want bragi", before.Provider)
	}

	if err := svc.RecordRouteAttempt(context.Background(), RouteAttempt{
		Harness:  "agent",
		Provider: "bragi",
		Model:    "qwen",
		Status:   "failed",
		Reason:   "timeout",
		Error:    "context deadline exceeded",
	}); err != nil {
		t.Fatalf("RecordRouteAttempt: %v", err)
	}

	after, err := svc.ResolveRoute(context.Background(), RouteRequest{
		Model:    "qwen",
		Provider: "bragi",
	})
	if err != nil {
		t.Fatalf("ResolveRoute after failure: %v", err)
	}
	if after.Provider == "bragi" {
		t.Fatalf("after failure Provider: got bragi, want a non-cooldown provider")
	}
}

func TestRecordRouteAttempt_SuccessClearsFailure(t *testing.T) {
	svc := routeAttemptTestService(30 * time.Second)
	if err := svc.RecordRouteAttempt(context.Background(), RouteAttempt{
		Harness:  "agent",
		Provider: "bragi",
		Model:    "qwen",
		Status:   "failed",
		Error:    "502 bad gateway",
	}); err != nil {
		t.Fatalf("RecordRouteAttempt failed: %v", err)
	}
	if err := svc.RecordRouteAttempt(context.Background(), RouteAttempt{
		Harness:  "agent",
		Provider: "bragi",
		Model:    "qwen",
		Status:   "success",
	}); err != nil {
		t.Fatalf("RecordRouteAttempt success: %v", err)
	}

	dec, err := svc.ResolveRoute(context.Background(), RouteRequest{
		Model:    "qwen",
		Provider: "bragi",
	})
	if err != nil {
		t.Fatalf("ResolveRoute: %v", err)
	}
	if dec.Provider != "bragi" {
		t.Fatalf("Provider after success clear: got %q, want bragi", dec.Provider)
	}
}

func TestRecordRouteAttempt_TTLExpiryRemovesDemotion(t *testing.T) {
	svc := routeAttemptTestService(10 * time.Millisecond)
	if err := svc.RecordRouteAttempt(context.Background(), RouteAttempt{
		Harness:   "agent",
		Provider:  "bragi",
		Model:     "qwen",
		Status:    "failed",
		Timestamp: time.Now().Add(-time.Second),
	}); err != nil {
		t.Fatalf("RecordRouteAttempt: %v", err)
	}

	dec, err := svc.ResolveRoute(context.Background(), RouteRequest{
		Model:    "qwen",
		Provider: "bragi",
	})
	if err != nil {
		t.Fatalf("ResolveRoute: %v", err)
	}
	if dec.Provider != "bragi" {
		t.Fatalf("Provider after TTL expiry: got %q, want bragi", dec.Provider)
	}
}

func TestRouteStatus_RouteAttemptCooldownSurfaces(t *testing.T) {
	svc := routeAttemptTestService(30 * time.Second)
	recordedAt := time.Now().Add(-time.Second).UTC()
	if err := svc.RecordRouteAttempt(context.Background(), RouteAttempt{
		Harness:   "agent",
		Provider:  "bragi",
		Model:     "qwen",
		Status:    "failed",
		Reason:    "rate_limit",
		Error:     "429 too many requests",
		Timestamp: recordedAt,
	}); err != nil {
		t.Fatalf("RecordRouteAttempt: %v", err)
	}

	report, err := svc.RouteStatus(context.Background())
	if err != nil {
		t.Fatalf("RouteStatus: %v", err)
	}
	if len(report.Routes) != 1 {
		t.Fatalf("Routes: got %d, want 1", len(report.Routes))
	}
	byProvider := make(map[string]RouteCandidateStatus)
	for _, cand := range report.Routes[0].Candidates {
		byProvider[cand.Provider] = cand
	}
	bragi := byProvider["bragi"]
	if bragi.Healthy {
		t.Fatal("bragi should be unhealthy while route-attempt cooldown is active")
	}
	if bragi.Cooldown == nil {
		t.Fatal("bragi cooldown should be populated")
	}
	if bragi.Cooldown.Reason != "rate_limit" {
		t.Fatalf("Cooldown.Reason: got %q, want rate_limit", bragi.Cooldown.Reason)
	}
	if bragi.Cooldown.LastError != "429 too many requests" {
		t.Fatalf("Cooldown.LastError: got %q", bragi.Cooldown.LastError)
	}
	if !bragi.Cooldown.LastAttempt.Equal(recordedAt) {
		t.Fatalf("Cooldown.LastAttempt: got %s, want %s", bragi.Cooldown.LastAttempt, recordedAt)
	}
	if !byProvider["openrouter"].Healthy {
		t.Fatal("openrouter should remain healthy")
	}
}

func TestResolveRoute_CodexUsesDurableQuotaCache(t *testing.T) {
	dir := t.TempDir()
	codexQuotaPath := filepath.Join(dir, "codex-quota.json")
	t.Setenv("DDX_AGENT_CODEX_QUOTA_CACHE", codexQuotaPath)
	t.Setenv("DDX_AGENT_CLAUDE_QUOTA_CACHE", filepath.Join(dir, "missing-claude-quota.json"))
	if err := codexharness.WriteCodexQuota(codexQuotaPath, codexharness.CodexQuotaSnapshot{
		CapturedAt: time.Now().UTC(),
		Source:     "pty",
		Account:    &harnesses.AccountInfo{PlanType: "ChatGPT Pro"},
		Windows: []harnesses.QuotaWindow{
			{Name: "5h", WindowMinutes: 300, UsedPercent: 25, State: "ok"},
		},
	}); err != nil {
		t.Fatalf("WriteCodexQuota: %v", err)
	}

	registry := harnesses.NewRegistry()
	registry.LookPath = func(file string) (string, error) {
		return filepath.Join(dir, file), nil
	}
	svc := &service{opts: ServiceOptions{}, registry: registry}
	dec, err := svc.ResolveRoute(context.Background(), RouteRequest{Profile: "smart"})
	if err != nil {
		t.Fatalf("ResolveRoute: %v", err)
	}
	if dec.Harness != "codex" || dec.Model != "gpt-5.4" {
		t.Fatalf("ResolveRoute: got harness=%q model=%q, want codex gpt-5.4", dec.Harness, dec.Model)
	}
}

func TestBuildRoutingInputs_CodexQuotaStaleOrBlockedIsIneligible(t *testing.T) {
	dir := t.TempDir()
	codexQuotaPath := filepath.Join(dir, "codex-quota.json")
	t.Setenv("DDX_AGENT_CODEX_QUOTA_CACHE", codexQuotaPath)
	registry := harnesses.NewRegistry()
	svc := &service{opts: ServiceOptions{}, registry: registry}

	if err := codexharness.WriteCodexQuota(codexQuotaPath, codexharness.CodexQuotaSnapshot{
		CapturedAt: time.Now().UTC().Add(-20 * time.Minute),
		Source:     "pty",
		Windows:    []harnesses.QuotaWindow{{Name: "5h", UsedPercent: 25, State: "ok"}},
	}); err != nil {
		t.Fatalf("WriteCodexQuota stale: %v", err)
	}
	codex := routingHarnessEntry(t, svc.buildRoutingInputs(context.Background()).Harnesses, "codex")
	if codex.SubscriptionOK || !codex.QuotaStale {
		t.Fatalf("stale codex quota: SubscriptionOK=%v QuotaStale=%v", codex.SubscriptionOK, codex.QuotaStale)
	}

	if err := codexharness.WriteCodexQuota(codexQuotaPath, codexharness.CodexQuotaSnapshot{
		CapturedAt: time.Now().UTC(),
		Source:     "pty",
		Windows:    []harnesses.QuotaWindow{{Name: "5h", UsedPercent: 96, State: "blocked"}},
	}); err != nil {
		t.Fatalf("WriteCodexQuota blocked: %v", err)
	}
	codex = routingHarnessEntry(t, svc.buildRoutingInputs(context.Background()).Harnesses, "codex")
	if codex.SubscriptionOK || codex.QuotaTrend != "exhausting" {
		t.Fatalf("blocked codex quota: SubscriptionOK=%v QuotaTrend=%q", codex.SubscriptionOK, codex.QuotaTrend)
	}
}

func TestBuildRoutingInputs_SecondaryHarnessesAndGeminiPromotion(t *testing.T) {
	registry := harnesses.NewRegistry()
	svc := &service{opts: ServiceOptions{}, registry: registry}
	inputs := svc.buildRoutingInputs(context.Background())

	opencode := routingHarnessEntry(t, inputs.Harnesses, "opencode")
	if opencode.AutoRoutingEligible || opencode.DefaultModel != "opencode/gpt-5.4" {
		t.Fatalf("opencode routing metadata: AutoRoutingEligible=%v DefaultModel=%q", opencode.AutoRoutingEligible, opencode.DefaultModel)
	}
	if !containsRouteString(opencode.SupportedModels, "opencode/gpt-5.4") {
		t.Fatalf("opencode supported models missing default: %v", opencode.SupportedModels)
	}
	if !containsRouteString(opencode.SupportedReasoning, "max") {
		t.Fatalf("opencode reasoning metadata missing max: %v", opencode.SupportedReasoning)
	}

	pi := routingHarnessEntry(t, inputs.Harnesses, "pi")
	if pi.AutoRoutingEligible || pi.DefaultModel != "gemini-2.5-flash" {
		t.Fatalf("pi routing metadata: AutoRoutingEligible=%v DefaultModel=%q", pi.AutoRoutingEligible, pi.DefaultModel)
	}
	if !containsRouteString(pi.SupportedReasoning, "xhigh") {
		t.Fatalf("pi reasoning metadata missing xhigh: %v", pi.SupportedReasoning)
	}

	gemini := routingHarnessEntry(t, inputs.Harnesses, "gemini")
	if !gemini.AutoRoutingEligible || gemini.DefaultModel != "gemini-2.5-flash" {
		t.Fatalf("gemini routing metadata: AutoRoutingEligible=%v DefaultModel=%q", gemini.AutoRoutingEligible, gemini.DefaultModel)
	}
	if !containsRouteString(gemini.SupportedModels, "gemini-2.5-pro") || !containsRouteString(gemini.SupportedModels, "gemini-2.5-flash-lite") {
		t.Fatalf("gemini supported models not populated from registry: %v", gemini.SupportedModels)
	}
	if len(gemini.SupportedReasoning) != 0 {
		t.Fatalf("gemini should not advertise reasoning controls: %v", gemini.SupportedReasoning)
	}
}

func TestResolveRoute_GeminiProfilesUseCatalogModels(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "redacted")
	registry := harnesses.NewRegistry()
	registry.LookPath = func(file string) (string, error) {
		if file == "gemini" {
			return "/usr/bin/gemini", nil
		}
		return "", os.ErrNotExist
	}
	svc := &service{opts: ServiceOptions{}, registry: registry}

	for profile, want := range map[string]string{
		"smart":    "gemini-2.5-pro",
		"standard": "gemini-2.5-flash",
		"cheap":    "gemini-2.5-flash-lite",
	} {
		dec, err := svc.ResolveRoute(context.Background(), RouteRequest{Harness: "gemini", Profile: profile})
		if err != nil {
			t.Fatalf("ResolveRoute profile=%s: %v", profile, err)
		}
		if dec.Harness != "gemini" || dec.Model != want {
			t.Fatalf("profile=%s: got harness=%q model=%q, want gemini/%s", profile, dec.Harness, dec.Model, want)
		}
	}
}

func routeAttemptTestService(cooldown time.Duration) *service {
	sc := &fakeServiceConfig{
		providers: map[string]ServiceProviderEntry{
			"bragi":      {Type: "lmstudio", BaseURL: "http://127.0.0.1:9999/v1", Model: "qwen"},
			"openrouter": {Type: "openrouter", BaseURL: "https://openrouter.invalid/v1", Model: "qwen"},
		},
		names:          []string{"bragi", "openrouter"},
		defaultName:    "bragi",
		healthCooldown: cooldown,
		routeConfigs: map[string]ServiceModelRouteConfig{
			"qwen": {
				Strategy: "ordered-failover",
				Candidates: []ServiceRouteCandidateEntry{
					{Provider: "bragi", Model: "qwen", Priority: 100},
					{Provider: "openrouter", Model: "qwen", Priority: 50},
				},
			},
		},
		routes: map[string][]string{"qwen": {"bragi", "openrouter"}},
	}
	return &service{opts: ServiceOptions{ServiceConfig: sc}, registry: harnesses.NewRegistry()}
}

func routingHarnessEntry(t *testing.T, entries []routing.HarnessEntry, name string) routing.HarnessEntry {
	t.Helper()
	for _, entry := range entries {
		if entry.Name == name {
			return entry
		}
	}
	t.Fatalf("routing entry %q not found", name)
	return routing.HarnessEntry{}
}

func containsRouteString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
