package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agentcore "github.com/DocumentDrivenDX/agent/internal/core"
	"github.com/DocumentDrivenDX/agent/internal/harnesses"
	claudeharness "github.com/DocumentDrivenDX/agent/internal/harnesses/claude"
	codexharness "github.com/DocumentDrivenDX/agent/internal/harnesses/codex"
	sessionlog "github.com/DocumentDrivenDX/agent/internal/session"
)

func TestListHarnesses_QuotaAndAccountStatus(t *testing.T) {
	dir := t.TempDir()
	claudePath := filepath.Join(dir, "claude-quota.json")
	codexPath := filepath.Join(dir, "codex-quota.json")
	t.Setenv("DDX_AGENT_CLAUDE_QUOTA_CACHE", claudePath)
	t.Setenv("DDX_AGENT_CODEX_QUOTA_CACHE", codexPath)

	capturedAt := time.Now().UTC().Add(-time.Minute)
	if err := claudeharness.WriteClaudeQuota(claudePath, claudeharness.ClaudeQuotaSnapshot{
		CapturedAt:        capturedAt,
		FiveHourRemaining: 80,
		FiveHourLimit:     100,
		WeeklyRemaining:   90,
		WeeklyLimit:       100,
		Source:            "pty",
		Account:           &harnesses.AccountInfo{PlanType: "Claude Max"},
	}); err != nil {
		t.Fatalf("WriteClaudeQuota: %v", err)
	}
	if err := codexharness.WriteCodexQuota(codexPath, codexharness.CodexQuotaSnapshot{
		CapturedAt: capturedAt,
		Source:     "pty",
		Windows: []harnesses.QuotaWindow{
			{Name: "5h", LimitID: "codex", WindowMinutes: 300, UsedPercent: 20, State: "ok"},
		},
	}); err != nil {
		t.Fatalf("WriteCodexQuota: %v", err)
	}

	svc := &service{opts: ServiceOptions{}, registry: harnesses.NewRegistry()}
	harnesses, err := svc.ListHarnesses(context.Background())
	if err != nil {
		t.Fatalf("ListHarnesses: %v", err)
	}

	claudeInfo := findHarnessInfo(harnesses, "claude")
	if claudeInfo == nil || claudeInfo.Quota == nil {
		t.Fatalf("expected claude quota, got %#v", claudeInfo)
	}
	if claudeInfo.Quota.Source != "pty" || claudeInfo.Quota.Status != "ok" || !claudeInfo.Quota.Fresh {
		t.Fatalf("claude quota status: %#v", claudeInfo.Quota)
	}
	if claudeInfo.Account == nil || !claudeInfo.Account.Authenticated || claudeInfo.Account.PlanType != "Claude Max" {
		t.Fatalf("claude account: %#v", claudeInfo.Account)
	}

	codexInfo := findHarnessInfo(harnesses, "codex")
	if codexInfo == nil || codexInfo.Quota == nil {
		t.Fatalf("expected codex quota, got %#v", codexInfo)
	}
	if codexInfo.Quota.Source != "pty" || codexInfo.Quota.Status != "ok" || len(codexInfo.Quota.Windows) != 1 {
		t.Fatalf("codex quota status: %#v", codexInfo.Quota)
	}
}

func TestListHarnesses_CodexUsageWindowsFromDDXSessionLogs(t *testing.T) {
	dir := t.TempDir()
	logDir := filepath.Join(dir, ".agent", "sessions")
	t.Setenv("CODEX_HOME", filepath.Join(dir, "private-codex"))
	t.Setenv("DDX_AGENT_CODEX_QUOTA_CACHE", filepath.Join(dir, "missing-codex-quota.json"))
	t.Setenv("DDX_AGENT_CLAUDE_QUOTA_CACHE", filepath.Join(dir, "missing-claude-quota.json"))
	disableCodexSessionQuotaReaderForTest(t)
	if err := os.MkdirAll(filepath.Join(dir, "private-codex", "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "private-codex", "sessions", "private.jsonl"), []byte(`{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":999999}}}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	start := time.Now().UTC().Add(-time.Hour)
	writeServiceUsageSession(t, logDir, "codex-known", start, sessionlog.SessionStartData{
		Provider: "codex",
		Model:    "gpt-5.4",
		Prompt:   "private prompt must not be read by status aggregation",
	}, sessionlog.SessionEndData{
		Status:     agentcore.StatusSuccess,
		Tokens:     agentcore.TokenUsage{Input: 10, Output: 4, Total: 14, CacheRead: 3, CacheWrite: 2},
		CostUSD:    usageCostPtr(0.12),
		DurationMs: 1000,
		Model:      "gpt-5.4",
	})
	writeServiceUsageSession(t, logDir, "codex-unknown", start.Add(time.Minute), sessionlog.SessionStartData{
		Provider: "codex",
		Model:    "gpt-5.4",
		Prompt:   "another prompt",
	}, sessionlog.SessionEndData{
		Status:     agentcore.StatusSuccess,
		Tokens:     agentcore.TokenUsage{Input: 5, Output: 2, Total: 7},
		CostUSD:    usageCostPtr(-1),
		DurationMs: 1000,
		Model:      "gpt-5.4",
	})
	writeServiceUsageSession(t, logDir, "provider-not-codex", start.Add(2*time.Minute), sessionlog.SessionStartData{
		Provider: "openrouter",
		Model:    "gpt-5.4",
		Prompt:   "not codex",
	}, sessionlog.SessionEndData{
		Status:     agentcore.StatusSuccess,
		Tokens:     agentcore.TokenUsage{Input: 100, Output: 100, Total: 200},
		CostUSD:    usageCostPtr(1),
		DurationMs: 1000,
		Model:      "gpt-5.4",
	})

	svc := &service{
		opts:     ServiceOptions{ServiceConfig: &fakeServiceConfig{workDir: dir}},
		registry: harnesses.NewRegistry(),
	}
	harnesses, err := svc.ListHarnesses(context.Background())
	if err != nil {
		t.Fatalf("ListHarnesses: %v", err)
	}
	codexInfo := findHarnessInfo(harnesses, "codex")
	if codexInfo == nil {
		t.Fatal("missing codex harness")
	}
	if len(codexInfo.UsageWindows) != 1 {
		t.Fatalf("UsageWindows: got %#v", codexInfo.UsageWindows)
	}
	window := codexInfo.UsageWindows[0]
	if window.Name != "30d" || window.Source != logDir || !window.Fresh {
		t.Fatalf("usage window metadata: %#v", window)
	}
	if window.InputTokens != 15 || window.OutputTokens != 6 || window.TotalTokens != 21 {
		t.Fatalf("usage totals should come only from DDx codex logs, not private Codex sessions or other providers: %#v", window)
	}
	if window.CacheReadTokens != 3 || window.CacheWriteTokens != 2 {
		t.Fatalf("cache tokens: %#v", window)
	}
	if window.KnownCostUSD != nil || window.CostUSD != 0 || window.UnknownCostSessions != 1 {
		t.Fatalf("known/unknown cost state: %#v", window)
	}
}

func TestListHarnesses_GeminiQuotaAndUsageWindows(t *testing.T) {
	dir := t.TempDir()
	logDir := filepath.Join(dir, ".agent", "sessions")
	t.Setenv("GOOGLE_GENAI_USE_GCA", "")
	t.Setenv("GOOGLE_GENAI_USE_VERTEXAI", "")
	t.Setenv("GOOGLE_API_KEY", "test-key")
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("CLOUD_SHELL", "")
	t.Setenv("GEMINI_CLI_USE_COMPUTE_ADC", "")
	t.Setenv("DDX_AGENT_CODEX_QUOTA_CACHE", filepath.Join(dir, "missing-codex-quota.json"))
	t.Setenv("DDX_AGENT_CLAUDE_QUOTA_CACHE", filepath.Join(dir, "missing-claude-quota.json"))

	start := time.Now().UTC().Add(-time.Hour)
	writeServiceUsageSession(t, logDir, "gemini-known", start, sessionlog.SessionStartData{
		Provider: "gemini",
		Model:    "gemini-2.5-flash",
		Prompt:   "private prompt must not be read by status aggregation",
	}, sessionlog.SessionEndData{
		Status:     agentcore.StatusSuccess,
		Tokens:     agentcore.TokenUsage{Input: 21, Output: 3, Total: 24, CacheRead: 5},
		CostUSD:    usageCostPtr(0.02),
		DurationMs: 1000,
		Model:      "gemini-2.5-flash",
	})
	writeServiceUsageSession(t, logDir, "not-gemini", start.Add(time.Minute), sessionlog.SessionStartData{
		Provider: "codex",
		Model:    "gpt-5.4",
		Prompt:   "not gemini",
	}, sessionlog.SessionEndData{
		Status:     agentcore.StatusSuccess,
		Tokens:     agentcore.TokenUsage{Input: 100, Output: 100, Total: 200},
		CostUSD:    usageCostPtr(1),
		DurationMs: 1000,
		Model:      "gpt-5.4",
	})

	svc := &service{
		opts:     ServiceOptions{ServiceConfig: &fakeServiceConfig{workDir: dir}},
		registry: harnesses.NewRegistry(),
	}
	harnesses, err := svc.ListHarnesses(context.Background())
	if err != nil {
		t.Fatalf("ListHarnesses: %v", err)
	}
	geminiInfo := findHarnessInfo(harnesses, "gemini")
	if geminiInfo == nil {
		t.Fatal("missing gemini harness")
	}
	if geminiInfo.Quota == nil || geminiInfo.Quota.Status != "ok" || !geminiInfo.Quota.Fresh || geminiInfo.Quota.Source != "environment" {
		t.Fatalf("gemini quota status: %#v", geminiInfo.Quota)
	}
	if geminiInfo.Account == nil || !geminiInfo.Account.Authenticated || geminiInfo.Account.PlanType != "Gemini API key" {
		t.Fatalf("gemini account: %#v", geminiInfo.Account)
	}
	if len(geminiInfo.UsageWindows) != 1 {
		t.Fatalf("UsageWindows: got %#v", geminiInfo.UsageWindows)
	}
	window := geminiInfo.UsageWindows[0]
	if window.Name != "30d" || window.Source != logDir || !window.Fresh {
		t.Fatalf("usage window metadata: %#v", window)
	}
	if window.InputTokens != 21 || window.OutputTokens != 3 || window.TotalTokens != 24 || window.CacheReadTokens != 5 {
		t.Fatalf("usage totals should come only from DDx gemini logs, not other providers: %#v", window)
	}
	if window.KnownCostUSD == nil || *window.KnownCostUSD != 0.02 || window.CostUSD != 0.02 || window.UnknownCostSessions != 0 {
		t.Fatalf("known/unknown cost state: %#v", window)
	}
}

func TestBuildRoutingInputs_IgnoresCodexUsageWindows(t *testing.T) {
	dir := t.TempDir()
	logDir := filepath.Join(dir, ".agent", "sessions")
	t.Setenv("DDX_AGENT_CODEX_QUOTA_CACHE", filepath.Join(dir, "missing-codex-quota.json"))
	t.Setenv("DDX_AGENT_CLAUDE_QUOTA_CACHE", filepath.Join(dir, "missing-claude-quota.json"))
	writeServiceUsageSession(t, logDir, "codex-usage", time.Now().UTC().Add(-time.Hour), sessionlog.SessionStartData{
		Provider: "codex",
		Model:    "gpt-5.4",
	}, sessionlog.SessionEndData{
		Status: agentcore.StatusSuccess,
		Tokens: agentcore.TokenUsage{Input: 100, Output: 20, Total: 120},
		Model:  "gpt-5.4",
	})
	svc := &service{
		opts:     ServiceOptions{ServiceConfig: &fakeServiceConfig{workDir: dir}},
		registry: harnesses.NewRegistry(),
	}
	codex := routingHarnessEntry(t, svc.buildRoutingInputs(context.Background()).Harnesses, "codex")
	if codex.SubscriptionOK {
		t.Fatal("usage logs must not make Codex routing-eligible without quota evidence")
	}
}

func TestReferenceConsumerDoctorReportUsesServiceStatus(t *testing.T) {
	sc := &fakeServiceConfig{
		providers: map[string]ServiceProviderEntry{
			"openrouter": {Type: "openrouter", BaseURL: "http://127.0.0.1:1/v1"},
		},
		names:       []string{"openrouter"},
		defaultName: "openrouter",
		routes: map[string][]string{
			"smart": {"openrouter"},
		},
		routeConfigs: map[string]ServiceModelRouteConfig{
			"smart": {Strategy: "ordered-failover", Candidates: []ServiceRouteCandidateEntry{{Provider: "openrouter", Model: "model-a", Priority: 10}}},
		},
	}
	svc := &service{opts: ServiceOptions{ServiceConfig: sc}, registry: harnesses.NewRegistry()}

	providers, err := svc.ListProviders(context.Background())
	if err != nil {
		t.Fatalf("ListProviders: %v", err)
	}
	routes, err := svc.RouteStatus(context.Background())
	if err != nil {
		t.Fatalf("RouteStatus: %v", err)
	}

	var b strings.Builder
	for _, p := range providers {
		b.WriteString(p.Name)
		b.WriteString(":")
		b.WriteString(p.EndpointStatus[0].Status)
		b.WriteString("\n")
	}
	for _, r := range routes.Routes {
		b.WriteString(r.Model)
		b.WriteString(":")
		b.WriteString(r.Strategy)
		b.WriteString("\n")
	}
	report := b.String()
	if !strings.Contains(report, "openrouter:") || !strings.Contains(report, "smart:ordered-failover") {
		t.Fatalf("doctor report missing service data: %q", report)
	}
}

func writeServiceUsageSession(t *testing.T, logDir, sessionID string, startAt time.Time, start sessionlog.SessionStartData, end sessionlog.SessionEndData) {
	t.Helper()
	logger := sessionlog.NewLogger(logDir, sessionID)
	startEvent := sessionlog.NewEvent(sessionID, 0, agentcore.EventSessionStart, start)
	startEvent.Timestamp = startAt
	logger.Write(startEvent)
	endEvent := sessionlog.NewEvent(sessionID, 1, agentcore.EventSessionEnd, end)
	endEvent.Timestamp = startAt.Add(time.Second)
	logger.Write(endEvent)
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
}

func usageCostPtr(v float64) *float64 {
	return &v
}

func findHarnessInfo(list []HarnessInfo, name string) *HarnessInfo {
	for i := range list {
		if list[i].Name == name {
			return &list[i]
		}
	}
	return nil
}
