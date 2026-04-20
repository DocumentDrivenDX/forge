package agent

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DocumentDrivenDX/agent/internal/harnesses"
	claudeharness "github.com/DocumentDrivenDX/agent/internal/harnesses/claude"
	codexharness "github.com/DocumentDrivenDX/agent/internal/harnesses/codex"
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

func findHarnessInfo(list []HarnessInfo, name string) *HarnessInfo {
	for i := range list {
		if list[i].Name == name {
			return &list[i]
		}
	}
	return nil
}
