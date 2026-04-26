package agent

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DocumentDrivenDX/agent/internal/harnesses"
	"github.com/DocumentDrivenDX/agent/internal/modelcatalog"
	"github.com/DocumentDrivenDX/agent/internal/routing"
)

func TestRouteCandidateFromInternalMapsFields(t *testing.T) {
	candidate := routing.Candidate{
		Harness:            "agent",
		Provider:           "local",
		Endpoint:           "primary",
		Model:              "model-a",
		Score:              42.5,
		CostUSDPer1kTokens: 0.012,
		CostSource:         routing.CostSourceCatalog,
		Eligible:           true,
		Reason:             "profile=cheap; score=42.5",
	}

	got := routeCandidateFromInternal(candidate)
	if got.Harness != candidate.Harness ||
		got.Provider != candidate.Provider ||
		got.Endpoint != candidate.Endpoint ||
		got.Model != candidate.Model ||
		got.Score != candidate.Score ||
		got.CostUSDPer1kTokens != candidate.CostUSDPer1kTokens ||
		got.CostSource != candidate.CostSource ||
		got.Eligible != candidate.Eligible {
		t.Fatalf("routeCandidateFromInternal()=%#v, want fields from %#v", got, candidate)
	}
	if got.Reason != candidate.Reason {
		t.Fatalf("eligible Reason=%q, want %q", got.Reason, candidate.Reason)
	}

	rejected := candidate
	rejected.Eligible = false
	rejected.Reason = "model not in harness allow-list"
	got = routeCandidateFromInternal(rejected)
	if got.Reason != rejected.Reason {
		t.Fatalf("rejected Reason=%q, want %q", got.Reason, rejected.Reason)
	}
}

func TestResolveRouteSuccessIncludesCandidates(t *testing.T) {
	svc := publicRouteTraceService(&fakeServiceConfig{
		providers: map[string]ServiceProviderEntry{
			"local": {Type: "test", BaseURL: "http://127.0.0.1:9999/v1", Model: "model-a"},
		},
		names:       []string{"local"},
		defaultName: "local",
	})

	dec, err := svc.ResolveRoute(context.Background(), RouteRequest{
		Harness: "agent",
		Model:   "model-a",
	})
	if err != nil {
		t.Fatalf("ResolveRoute: %v", err)
	}
	if dec == nil {
		t.Fatal("ResolveRoute returned nil decision")
	}
	if dec.Harness != "agent" || dec.Provider != "local" || dec.Model != "model-a" {
		t.Fatalf("decision=%#v, want agent/local/model-a", dec)
	}
	if len(dec.Candidates) != 1 {
		t.Fatalf("Candidates length=%d, want 1: %#v", len(dec.Candidates), dec.Candidates)
	}
	candidate := dec.Candidates[0]
	if !candidate.Eligible || candidate.Harness != "agent" || candidate.Provider != "local" || candidate.Model != "model-a" {
		t.Fatalf("candidate=%#v, want eligible agent/local/model-a", candidate)
	}
	if !strings.Contains(candidate.Reason, "score=") {
		t.Fatalf("eligible candidate Reason=%q, want scoring reason", candidate.Reason)
	}
}

func TestResolveRouteErrorIncludesCandidatesAndTraceError(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "redacted")
	t.Setenv("GOOGLE_API_KEY", "")
	t.Setenv("GOOGLE_GENAI_USE_VERTEXAI", "")
	t.Setenv("GOOGLE_GENAI_USE_GCA", "")
	t.Setenv("GEMINI_CLI_USE_COMPUTE_ADC", "")
	t.Setenv("CLOUD_SHELL", "")

	registry := harnesses.NewRegistry()
	registry.LookPath = func(file string) (string, error) {
		if file == "gemini" {
			return "/usr/bin/gemini", nil
		}
		return "", os.ErrNotExist
	}
	svc := &service{
		opts:     ServiceOptions{},
		registry: registry,
		hub:      newSessionHub(),
	}

	dec, err := svc.ResolveRoute(context.Background(), RouteRequest{
		Model: "minimax/minimax-m2.7",
	})
	if err == nil {
		t.Fatal("ResolveRoute expected no viable candidate error")
	}
	if dec == nil {
		t.Fatal("ResolveRoute error path returned nil decision")
	}
	if dec.Harness != "" || dec.Provider != "" || dec.Model != "" {
		t.Fatalf("error decision selected a candidate: %#v", dec)
	}
	if len(dec.Candidates) == 0 {
		t.Fatal("error decision Candidates is empty")
	}

	var noViable *routing.NoViableCandidateError
	if !errors.As(err, &noViable) {
		t.Fatalf("errors.As no viable: %T %v", err, err)
	}
	var traced DecisionWithCandidates
	if !errors.As(err, &traced) {
		t.Fatalf("errors.As DecisionWithCandidates: %T %v", err, err)
	}
	tracedCandidates := traced.RouteCandidates()
	if len(tracedCandidates) != len(dec.Candidates) {
		t.Fatalf("traced candidates length=%d, decision candidates length=%d", len(tracedCandidates), len(dec.Candidates))
	}
	tracedCandidates[0].Reason = "mutated"
	if dec.Candidates[0].Reason == "mutated" {
		t.Fatal("RouteCandidates returned an alias of the decision candidates")
	}

	if !strings.Contains(err.Error(), "no viable routing candidate") {
		t.Fatalf("error=%q, want no viable routing candidate detail", err.Error())
	}
}

func TestBuildRoutingInputsPopulatesEndpointProviderCostsFromCatalog(t *testing.T) {
	catalog := loadRoutingFixtureCatalog(t, `
version: 4
generated_at: 2026-04-22T00:00:00Z
models:
  alpha-provider-model:
    family: same
    status: active
    cost_input_per_m: 1
    cost_output_per_m: 3
    surfaces: {agent.openai: alpha-provider-model}
  beta-provider-model:
    family: same
    status: active
    cost_input_per_m: 2
    cost_output_per_m: 4
    surfaces: {agent.openai: beta-provider-model}
  gamma-provider-model:
    family: same
    status: active
    cost_input_per_m: 4
    cost_output_per_m: 8
    surfaces: {agent.openai: gamma-provider-model}
targets:
  alpha:
    family: same
    candidates: [alpha-provider-model]
  beta:
    family: same
    candidates: [beta-provider-model]
  gamma:
    family: same
    candidates: [gamma-provider-model]
`)
	restore := replaceRoutingCatalogForTest(t, catalog)
	defer restore()

	sc := &fakeServiceConfig{
		providers: map[string]ServiceProviderEntry{
			"alpha": {Type: "openai", BaseURL: "http://alpha.invalid/v1", Model: "alpha-provider-model"},
			"beta":  {Type: "openai", BaseURL: "http://beta.invalid/v1", Model: "beta-provider-model"},
			"gamma": {Type: "openai", BaseURL: "http://gamma.invalid/v1", Model: "gamma-provider-model"},
		},
		names:       []string{"alpha", "beta", "gamma"},
		defaultName: "alpha",
	}
	svc := &service{opts: ServiceOptions{ServiceConfig: sc}, registry: harnesses.NewRegistry()}
	in := svc.buildRoutingInputs(context.Background())

	got := providerCostsByName(in, "agent")
	assertProviderCost(t, got, "alpha", 0.002, routing.CostSourceCatalog)
	assertProviderCost(t, got, "beta", 0.003, routing.CostSourceCatalog)
	assertProviderCost(t, got, "gamma", 0.006, routing.CostSourceCatalog)
}

func TestSubscriptionEffectiveCostCurveByHarnessAndBand(t *testing.T) {
	catalog := loadRoutingFixtureCatalog(t, `
version: 4
generated_at: 2026-04-22T00:00:00Z
models:
  priced-model:
    family: priced
    status: active
    cost_input_per_m: 10
    cost_output_per_m: 10
    surfaces:
      agent.openai: priced-model
      codex: priced-model
      claude-code: priced-model
      gemini: priced-model
targets:
  priced:
    family: priced
    candidates: [priced-model]
`)
	svc := &service{}
	tests := []struct {
		name        string
		usedPercent int
		want        float64
	}{
		{name: "free", usedPercent: 70, want: 0},
		{name: "low", usedPercent: 75, want: 0.001},
		{name: "medium", usedPercent: 85, want: 0.003},
		{name: "high", usedPercent: 92, want: 0.012},
	}
	for _, harness := range []string{"claude", "codex", "gemini"} {
		for _, tt := range tests {
			t.Run(harness+"/"+tt.name, func(t *testing.T) {
				entry := routing.HarnessEntry{
					Name:             harness,
					IsSubscription:   true,
					DefaultModel:     "priced-model",
					QuotaPercentUsed: tt.usedPercent,
				}
				svc.applySubscriptionRoutingCost(&entry, catalog)
				if len(entry.Providers) != 1 {
					t.Fatalf("providers=%#v, want one subscription provider", entry.Providers)
				}
				got := entry.Providers[0]
				if got.CostUSDPer1kTokens != tt.want || got.CostSource != routing.CostSourceSubscription {
					t.Fatalf("effective cost=(%v,%q), want (%v,%q)", got.CostUSDPer1kTokens, got.CostSource, tt.want, routing.CostSourceSubscription)
				}
			})
		}
	}
}

func TestBuildRoutingInputsHonorsLocalCostOption(t *testing.T) {
	sc := &fakeServiceConfig{
		providers: map[string]ServiceProviderEntry{
			"local": {Type: "lmstudio", BaseURL: "http://127.0.0.1:1234/v1", Model: "local-model"},
		},
		names:       []string{"local"},
		defaultName: "local",
	}

	unsetSvc := &service{opts: ServiceOptions{ServiceConfig: sc}, registry: harnesses.NewRegistry()}
	unset := providerCostsByName(unsetSvc.buildRoutingInputs(context.Background()), "agent")
	assertProviderCost(t, unset, "local", 0, routing.CostSourceUnknown)

	setSvc := &service{
		opts:     ServiceOptions{ServiceConfig: sc, LocalCostUSDPer1kTokens: 0.0042},
		registry: harnesses.NewRegistry(),
	}
	set := providerCostsByName(setSvc.buildRoutingInputs(context.Background()), "agent")
	assertProviderCost(t, set, "local", 0.0042, routing.CostSourceUserConfig)
}

func TestResolveRouteNearQuotaClaudeDemotesBelowOpenRouter(t *testing.T) {
	catalog := loadRoutingFixtureCatalog(t, `
version: 4
generated_at: 2026-04-22T00:00:00Z
models:
  sonnet-4.6:
    family: claude-sonnet
    status: active
    cost_input_per_m: 3
    cost_output_per_m: 15
    surfaces:
      agent.openai: sonnet-4.6
      claude-code: sonnet-4.6
targets:
  standard:
    family: claude-sonnet
    candidates: [sonnet-4.6]
`)
	svc := &service{}

	claude := routing.HarnessEntry{
		Name:                "claude",
		Surface:             "claude",
		CostClass:           "medium",
		IsSubscription:      true,
		AutoRoutingEligible: true,
		Available:           true,
		QuotaOK:             true,
		QuotaPercentUsed:    92,
		QuotaTrend:          routing.QuotaTrendExhausting,
		SubscriptionOK:      true,
		DefaultModel:        "sonnet-4.6",
		ExactPinSupport:     true,
		SupportsTools:       true,
	}
	svc.applySubscriptionRoutingCost(&claude, catalog)

	openrouterProvider := routing.ProviderEntry{
		Name:          "openrouter",
		BaseURL:       "https://openrouter.ai/api/v1",
		DefaultModel:  "sonnet-4.6",
		SupportsTools: true,
	}
	svc.applyEndpointRoutingCost(&openrouterProvider, ServiceProviderEntry{
		Type:    "openrouter",
		BaseURL: "https://openrouter.ai/api/v1",
		Model:   "sonnet-4.6",
	}, catalog)

	in := routing.Inputs{
		Harnesses: []routing.HarnessEntry{
			claude,
			{
				Name:                "agent",
				Surface:             "embedded-openai",
				CostClass:           "medium",
				AutoRoutingEligible: true,
				Available:           true,
				QuotaOK:             true,
				SubscriptionOK:      true,
				ExactPinSupport:     true,
				SupportsTools:       true,
				Providers:           []routing.ProviderEntry{openrouterProvider},
			},
		},
		ObservedSpeedTPS: map[string]float64{
			// Neutralize Claude's near-quota score penalty so the final base
			// scores tie and the cost tiebreak is the deciding dimension.
			routing.ProviderModelKey("", "", "sonnet-4.6"): 1900,
		},
	}
	dec, err := routing.Resolve(routing.Request{
		Profile:            "standard",
		Model:              "sonnet-4.6",
		ProviderPreference: routing.ProviderPreferenceSubscriptionFirst,
	}, in)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if dec.Harness != "agent" || dec.Provider != "openrouter" {
		t.Fatalf("near-quota route selected harness=%q provider=%q, want agent/openrouter", dec.Harness, dec.Provider)
	}
	var claudeCandidate, openrouterCandidate routing.Candidate
	for _, candidate := range dec.Candidates {
		switch {
		case candidate.Harness == "claude":
			claudeCandidate = candidate
		case candidate.Harness == "agent" && candidate.Provider == "openrouter":
			openrouterCandidate = candidate
		}
	}
	if claudeCandidate.Harness == "" || openrouterCandidate.Harness == "" {
		t.Fatalf("expected claude and openrouter candidates in trace: %#v", dec.Candidates)
	}
	if claudeCandidate.Score != openrouterCandidate.Score {
		t.Fatalf("candidate scores should tie before cost tiebreak: claude=%.1f openrouter=%.1f", claudeCandidate.Score, openrouterCandidate.Score)
	}
	if claudeCandidate.CostSource != routing.CostSourceSubscription || !floatNear(claudeCandidate.CostUSDPer1kTokens, 0.0108) {
		t.Fatalf("claude cost metadata=%#v, want 92%% subscription cost 0.0108", claudeCandidate)
	}
	if openrouterCandidate.CostSource != routing.CostSourceCatalog || !floatNear(openrouterCandidate.CostUSDPer1kTokens, 0.009) {
		t.Fatalf("openrouter cost metadata=%#v, want catalog cost 0.009", openrouterCandidate)
	}
	if !(openrouterCandidate.CostUSDPer1kTokens < claudeCandidate.CostUSDPer1kTokens) {
		t.Fatalf("openrouter cost %.4f should be below claude %.4f", openrouterCandidate.CostUSDPer1kTokens, claudeCandidate.CostUSDPer1kTokens)
	}
}

func publicRouteTraceService(sc ServiceConfig) *service {
	return &service{
		opts:     ServiceOptions{ServiceConfig: sc},
		registry: harnesses.NewRegistry(),
		hub:      newSessionHub(),
	}
}

func loadRoutingFixtureCatalog(t *testing.T, contents string) *modelcatalog.Catalog {
	t.Helper()
	path := filepath.Join(t.TempDir(), "models.yaml")
	requireNoError(t, os.WriteFile(path, []byte(contents), 0o600))
	catalog, err := modelcatalog.Load(modelcatalog.LoadOptions{ManifestPath: path, RequireExternal: true})
	if err != nil {
		t.Fatalf("Load fixture catalog: %v", err)
	}
	return catalog
}

func replaceRoutingCatalogForTest(t *testing.T, catalog *modelcatalog.Catalog) func() {
	t.Helper()
	old := loadRoutingCatalog
	loadRoutingCatalog = func() (*modelcatalog.Catalog, error) {
		return catalog, nil
	}
	return func() { loadRoutingCatalog = old }
}

func providerCostsByName(in routing.Inputs, harness string) map[string]routing.ProviderEntry {
	out := make(map[string]routing.ProviderEntry)
	for _, h := range in.Harnesses {
		if h.Name != harness {
			continue
		}
		for _, p := range h.Providers {
			out[p.Name] = p
		}
	}
	return out
}

func assertProviderCost(t *testing.T, providers map[string]routing.ProviderEntry, name string, wantCost float64, wantSource string) {
	t.Helper()
	provider, ok := providers[name]
	if !ok {
		t.Fatalf("provider %q not found in %#v", name, providers)
	}
	if provider.CostUSDPer1kTokens != wantCost || provider.CostSource != wantSource {
		t.Fatalf("provider %q cost=(%v,%q), want (%v,%q)", name, provider.CostUSDPer1kTokens, provider.CostSource, wantCost, wantSource)
	}
}

func requireNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func floatNear(got, want float64) bool {
	return math.Abs(got-want) < 1e-12
}

// gateFixtureCatalog returns a catalog used by the ContextWindow / RequiresTools
// gate-firing tests below. It declares two concrete models on the agent.openai
// surface: small-ctx-model has a 4096-token context window (and supports
// tools), while no-tools-model has a generous context window but is marked
// no_tools=true so RequiresTools=true requests are rejected against it.
const gateFixtureCatalogYAML = `
version: 4
generated_at: 2026-04-25T00:00:00Z
models:
  small-ctx-model:
    family: gate
    status: active
    context_window: 4096
    surfaces: {agent.openai: small-ctx-model}
  no-tools-model:
    family: gate
    status: active
    context_window: 200000
    no_tools: true
    surfaces: {agent.openai: no-tools-model}
targets:
  small-ctx:
    family: gate
    candidates: [small-ctx-model]
  no-tools:
    family: gate
    candidates: [no-tools-model]
`

func newGateFixtureService(t *testing.T, providerModel string) *service {
	t.Helper()
	catalog := loadRoutingFixtureCatalog(t, gateFixtureCatalogYAML)
	t.Cleanup(replaceRoutingCatalogForTest(t, catalog))

	sc := &fakeServiceConfig{
		providers: map[string]ServiceProviderEntry{
			"local": {Type: "test", BaseURL: "http://127.0.0.1:9999/v1", Model: providerModel},
		},
		names:       []string{"local"},
		defaultName: "local",
	}
	return publicRouteTraceService(sc)
}

func findCandidate(t *testing.T, dec *RouteDecision, harness, provider string) RouteCandidate {
	t.Helper()
	if dec == nil {
		t.Fatal("nil decision")
	}
	for _, c := range dec.Candidates {
		if c.Harness == harness && c.Provider == provider {
			return c
		}
	}
	t.Fatalf("candidate harness=%q provider=%q not found in %#v", harness, provider, dec.Candidates)
	return RouteCandidate{}
}

// TestResolveRoute_FiltersByEstimatedPromptTokens proves the engine's
// context-window gate fires end-to-end: with ContextWindows wired from the
// catalog, a 1M-token prompt against a 4096-token model is marked ineligible
// with FilterReasonContextTooSmall.
func TestResolveRoute_FiltersByEstimatedPromptTokens(t *testing.T) {
	svc := newGateFixtureService(t, "small-ctx-model")

	dec, _ := svc.ResolveRoute(context.Background(), RouteRequest{
		Harness:               "agent",
		Model:                 "small-ctx-model",
		EstimatedPromptTokens: 1_000_000,
	})
	if dec == nil {
		t.Fatal("ResolveRoute returned nil decision")
	}
	candidate := findCandidate(t, dec, "agent", "local")
	if candidate.Eligible {
		t.Fatalf("candidate eligible with 1M tokens against 4096 context: %#v", candidate)
	}
	if candidate.FilterReason != FilterReasonContextTooSmall {
		t.Fatalf("FilterReason=%q, want %q (Reason=%q)", candidate.FilterReason, FilterReasonContextTooSmall, candidate.Reason)
	}
}

// TestResolveRoute_FiltersByRequiresTools proves the RequiresTools gate fires
// when the catalog marks the resolved model with no_tools=true.
func TestResolveRoute_FiltersByRequiresTools(t *testing.T) {
	svc := newGateFixtureService(t, "no-tools-model")

	dec, _ := svc.ResolveRoute(context.Background(), RouteRequest{
		Harness:       "agent",
		Model:         "no-tools-model",
		RequiresTools: true,
	})
	if dec == nil {
		t.Fatal("ResolveRoute returned nil decision")
	}
	candidate := findCandidate(t, dec, "agent", "local")
	if candidate.Eligible {
		t.Fatalf("candidate eligible despite no_tools=true: %#v", candidate)
	}
	if candidate.FilterReason != FilterReasonNoToolSupport {
		t.Fatalf("FilterReason=%q, want %q (Reason=%q)", candidate.FilterReason, FilterReasonNoToolSupport, candidate.Reason)
	}
}

// TestResolveRoute_NoOpWhenZero proves that an unset EstimatedPromptTokens /
// RequiresTools request does not change which candidates are eligible compared
// to a baseline request — no spurious filtering on the same model that the
// previous two tests reject under stress.
func TestResolveRoute_NoOpWhenZero(t *testing.T) {
	smallCtxSvc := newGateFixtureService(t, "small-ctx-model")
	noToolsSvc := newGateFixtureService(t, "no-tools-model")

	smallDec, err := smallCtxSvc.ResolveRoute(context.Background(), RouteRequest{
		Harness: "agent",
		Model:   "small-ctx-model",
	})
	if err != nil {
		t.Fatalf("ResolveRoute small-ctx-model: %v", err)
	}
	smallCandidate := findCandidate(t, smallDec, "agent", "local")
	if !smallCandidate.Eligible {
		t.Fatalf("small-ctx-model candidate ineligible without EstimatedPromptTokens: %#v", smallCandidate)
	}
	if smallCandidate.FilterReason != "" {
		t.Fatalf("small-ctx-model FilterReason=%q, want empty (no-op gate)", smallCandidate.FilterReason)
	}

	noToolsDec, err := noToolsSvc.ResolveRoute(context.Background(), RouteRequest{
		Harness: "agent",
		Model:   "no-tools-model",
	})
	if err != nil {
		t.Fatalf("ResolveRoute no-tools-model: %v", err)
	}
	noToolsCandidate := findCandidate(t, noToolsDec, "agent", "local")
	if !noToolsCandidate.Eligible {
		t.Fatalf("no-tools-model candidate ineligible without RequiresTools=true: %#v", noToolsCandidate)
	}
	if noToolsCandidate.FilterReason != "" {
		t.Fatalf("no-tools-model FilterReason=%q, want empty (no-op gate)", noToolsCandidate.FilterReason)
	}
}

// TestBuildRoutingInputsWiresContextWindowsFromCatalog asserts the structural
// wiring requested by the bead: ProviderEntry.ContextWindows is populated for
// the configured DefaultModel from the catalog so the engine's context-window
// gate has data to act on.
func TestBuildRoutingInputsWiresContextWindowsFromCatalog(t *testing.T) {
	catalog := loadRoutingFixtureCatalog(t, gateFixtureCatalogYAML)
	t.Cleanup(replaceRoutingCatalogForTest(t, catalog))

	sc := &fakeServiceConfig{
		providers: map[string]ServiceProviderEntry{
			"local": {Type: "test", BaseURL: "http://127.0.0.1:9999/v1", Model: "small-ctx-model"},
		},
		names:       []string{"local"},
		defaultName: "local",
	}
	svc := &service{opts: ServiceOptions{ServiceConfig: sc}, registry: harnesses.NewRegistry()}

	in := svc.buildRoutingInputs(context.Background())
	providers := providerCostsByName(in, "agent")
	provider, ok := providers["local"]
	if !ok {
		t.Fatalf("agent/local provider not in inputs: %#v", providers)
	}
	if got := provider.ContextWindows["small-ctx-model"]; got != 4096 {
		t.Fatalf("ContextWindows[small-ctx-model]=%d, want 4096 (full map=%#v)", got, provider.ContextWindows)
	}
}

// TestResolveRoute_LivenessEscalation exercises the profile→tier ladder
// (cheap → standard → smart) wired into ResolveRoute. When every candidate
// at the requested tier is filtered out (here: per-provider context-window
// rejection driven by the catalog), ResolveRoute walks the ladder and
// returns the first higher-tier candidate that still satisfies the request.
// When the entire remaining ladder is also empty, ResolveRoute surfaces
// the precise *ErrNoLiveProvider error rather than the engine's
// "no viable routing candidate" jargon.
func TestResolveRoute_LivenessEscalation(t *testing.T) {
	const fixtureCatalog = `
version: 4
generated_at: 2026-04-25T00:00:00Z
models:
  medium-model:
    family: tier
    status: active
    context_window: 4096
    surfaces: {agent.openai: medium-model}
  high-model:
    family: tier
    status: active
    context_window: 200000
    surfaces: {agent.openai: high-model}
targets:
  code-medium:
    family: tier
    candidates: [medium-model]
  code-high:
    family: tier
    candidates: [high-model]
profiles:
  cheap:
    target: code-medium
    provider_preference: local-first
  standard:
    target: code-medium
    provider_preference: local-first
  smart:
    target: code-high
    provider_preference: local-first
`

	newSvc := func(t *testing.T) (*service, func()) {
		t.Helper()
		// Block claude/codex/gemini subprocess harnesses from the
		// candidate set so the test isolates the agent harness's
		// per-provider tier escalation behavior.
		t.Setenv("GEMINI_API_KEY", "")
		t.Setenv("GOOGLE_API_KEY", "")
		t.Setenv("GOOGLE_GENAI_USE_VERTEXAI", "")
		t.Setenv("GOOGLE_GENAI_USE_GCA", "")
		t.Setenv("GEMINI_CLI_USE_COMPUTE_ADC", "")
		t.Setenv("CLOUD_SHELL", "")

		mediumSrv := openAIModelChatServer(t, []string{"medium-model"}, "medium-model", "ok")
		highSrv := openAIModelChatServer(t, []string{"high-model"}, "high-model", "ok")
		catalog := loadRoutingFixtureCatalog(t, fixtureCatalog)
		restore := replaceRoutingCatalogForTest(t, catalog)
		sc := &fakeServiceConfig{
			providers: map[string]ServiceProviderEntry{
				"alpha-medium": {Type: "openai", BaseURL: mediumSrv.URL + "/v1", APIKey: "k", Model: "medium-model"},
				"beta-high":    {Type: "openai", BaseURL: highSrv.URL + "/v1", APIKey: "k", Model: "high-model"},
			},
			names:       []string{"alpha-medium", "beta-high"},
			defaultName: "alpha-medium",
		}
		registry := harnesses.NewRegistry()
		registry.LookPath = func(string) (string, error) { return "", os.ErrNotExist }
		svc := &service{
			opts:     ServiceOptions{ServiceConfig: sc},
			registry: registry,
			hub:      newSessionHub(),
			catalog:  newCatalogCache(catalogCacheOptions{}),
		}
		cleanup := func() {
			mediumSrv.Close()
			highSrv.Close()
			restore()
		}
		return svc, cleanup
	}

	t.Run("escalates_when_lower_tier_filtered_out", func(t *testing.T) {
		svc, cleanup := newSvc(t)
		defer cleanup()

		// Record a route attempt failure on the lower-tier provider so the
		// real cooldown bookkeeping path (applyRouteAttemptCooldowns) runs
		// against this fixture, exercising the AC's "real ServiceConfig +
		// cooldown fixture" requirement.
		if err := svc.RecordRouteAttempt(context.Background(), RouteAttempt{
			Harness:  "agent",
			Provider: "alpha-medium",
			Model:    "medium-model",
			Status:   "failed",
			Reason:   "synthetic unhealthy fixture",
		}); err != nil {
			t.Fatalf("RecordRouteAttempt: %v", err)
		}

		dec, err := svc.ResolveRoute(context.Background(), RouteRequest{
			Profile:               "standard",
			EstimatedPromptTokens: 50_000,
		})
		if err != nil {
			t.Fatalf("ResolveRoute: %v", err)
		}
		if dec == nil || dec.Harness != "agent" {
			t.Fatalf("decision=%#v, want agent harness", dec)
		}
		if dec.Provider != "beta-high" {
			t.Fatalf("decision provider=%q, want beta-high (escalated to smart tier)", dec.Provider)
		}
		if dec.Model != "high-model" {
			t.Fatalf("decision model=%q, want high-model", dec.Model)
		}
	})

	t.Run("returns_no_live_provider_when_ladder_exhausted", func(t *testing.T) {
		svc, cleanup := newSvc(t)
		defer cleanup()

		dec, err := svc.ResolveRoute(context.Background(), RouteRequest{
			Profile:               "standard",
			EstimatedPromptTokens: 1_000_000, // exceeds both 4096 and 200000 contexts
		})
		if err == nil {
			t.Fatalf("ResolveRoute returned no error, decision=%#v", dec)
		}
		if !strings.Contains(err.Error(), "no live provider") {
			t.Fatalf("error=%q, want 'no live provider' message", err.Error())
		}
		if strings.Contains(err.Error(), "no viable routing candidate") {
			t.Fatalf("error=%q must NOT contain engine 'no viable routing candidate' jargon", err.Error())
		}
		var noLive *ErrNoLiveProvider
		if !errors.As(err, &noLive) {
			t.Fatalf("errors.As ErrNoLiveProvider: %T %v", err, err)
		}
		if noLive.StartingTier != "standard" {
			t.Fatalf("StartingTier=%q, want standard", noLive.StartingTier)
		}
		if noLive.PromptTokens != 1_000_000 {
			t.Fatalf("PromptTokens=%d, want 1000000", noLive.PromptTokens)
		}
	})
}

// TestResolveRouteAutoResolvesToTierDefaultBeforeGate proves that when a
// request specifies Reasoning=auto, the routing engine resolves it to the
// catalog's surface_policy reasoning_default for the requested profile/surface
// BEFORE invoking the capability gate. Without this resolution an off-only
// candidate could win under a profile whose surface default is "high",
// silently dropping reasoning the operator implicitly asked for.
func TestResolveRouteAutoResolvesToTierDefaultBeforeGate(t *testing.T) {
	// off-only harness: SupportedReasoning is empty so the gate accepts
	// "off" (which imposes no requirement) but rejects any named level.
	offOnly := func() routing.HarnessEntry {
		return routing.HarnessEntry{
			Name:                "off-only",
			Surface:             "test-surface",
			CostClass:           "medium",
			AutoRoutingEligible: true,
			Available:           true,
			QuotaOK:             true,
			SubscriptionOK:      true,
			ExactPinSupport:     true,
			DefaultModel:        "off-model",
			SupportsTools:       true,
		}
	}

	resolver := func(profile, surface string) (string, bool) {
		switch profile {
		case "cheap":
			return "off", true
		case "smart":
			return "high", true
		}
		return "", false
	}

	t.Run("cheap_resolves_to_off_gate_passes", func(t *testing.T) {
		dec, err := routing.Resolve(routing.Request{
			Profile:   "cheap",
			Reasoning: "auto",
		}, routing.Inputs{
			Harnesses:         []routing.HarnessEntry{offOnly()},
			ReasoningResolver: resolver,
		})
		if err != nil {
			t.Fatalf("Resolve cheap+auto: %v", err)
		}
		if dec.Harness != "off-only" || dec.Model != "off-model" {
			t.Fatalf("decision=%#v, want off-only/off-model (auto must resolve to off and pass)", dec)
		}
	})

	t.Run("smart_resolves_to_high_gate_rejects", func(t *testing.T) {
		dec, err := routing.Resolve(routing.Request{
			Profile:   "smart",
			Reasoning: "auto",
		}, routing.Inputs{
			Harnesses:         []routing.HarnessEntry{offOnly()},
			ReasoningResolver: resolver,
		})
		if err == nil {
			t.Fatalf("Resolve smart+auto: expected NoViableCandidateError, got decision=%#v", dec)
		}
		var noViable *routing.NoViableCandidateError
		if !errors.As(err, &noViable) {
			t.Fatalf("error=%T %v, want *routing.NoViableCandidateError", err, err)
		}
		if dec == nil || len(dec.Candidates) != 1 {
			t.Fatalf("Candidates=%#v, want exactly one off-only candidate", dec)
		}
		c := dec.Candidates[0]
		if c.Harness != "off-only" || c.Eligible {
			t.Fatalf("candidate=%#v, want ineligible off-only", c)
		}
		if c.FilterReason != routing.FilterReasonReasoningUnsupported {
			t.Fatalf("FilterReason=%q, want %q (Reason=%q)", c.FilterReason, routing.FilterReasonReasoningUnsupported, c.Reason)
		}
	})

	t.Run("unset_reasoning_does_not_resolve", func(t *testing.T) {
		// Regression guard: the Reasoning=unset path keeps its existing
		// "no requirement" behavior — only Reasoning=auto triggers
		// surface_policy resolution.
		dec, err := routing.Resolve(routing.Request{
			Profile: "smart",
		}, routing.Inputs{
			Harnesses:         []routing.HarnessEntry{offOnly()},
			ReasoningResolver: resolver,
		})
		if err != nil {
			t.Fatalf("Resolve smart+unset: %v", err)
		}
		if dec.Harness != "off-only" {
			t.Fatalf("unset+smart decision=%#v, want off-only (unset must not trigger auto resolution)", dec)
		}
	})
}

func TestDecisionWithCandidatesCopiesInput(t *testing.T) {
	candidates := []RouteCandidate{{Harness: "agent", Reason: "original"}}
	err := withRouteCandidates(errors.New("no viable routing candidate"), candidates)

	candidates[0].Reason = "changed"
	var traced DecisionWithCandidates
	if !errors.As(err, &traced) {
		t.Fatalf("errors.As DecisionWithCandidates: %T %v", err, err)
	}
	got := traced.RouteCandidates()
	if len(got) != 1 || got[0].Reason != "original" {
		t.Fatalf("RouteCandidates=%#v, want copied original candidate", got)
	}
}
