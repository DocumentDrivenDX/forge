package routing

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// newTestRoutingEngine returns a baseline Inputs with two harnesses and a
// trivial catalog resolver. Mirrors the DDx newTestRunnerForRouting helper.
func newTestRoutingEngine() Inputs {
	return Inputs{
		Harnesses: []HarnessEntry{
			{
				Name:                "agent",
				Surface:             "embedded-openai",
				CostClass:           "local",
				IsLocal:             true,
				AutoRoutingEligible: true,
				ExactPinSupport:     true,
				Available:           true,
				QuotaOK:             true,
				SubscriptionOK:      true,
				SupportedReasoning:  []string{"low", "medium", "high"},
				SupportedPerms:      []string{"safe", "supervised", "unrestricted"},
				SupportsTools:       true,
				Providers: []ProviderEntry{
					{
						Name:          "vidar-omlx",
						BaseURL:       "http://vidar:11434",
						DiscoveredIDs: []string{"Qwen3.6-35B-A3B-4bit", "MiniMax-M2.5-MLX-4bit"},
						SupportsTools: true,
						ContextWindows: map[string]int{
							"Qwen3.6-35B-A3B-4bit": 256000,
						},
					},
					{
						Name:          "openrouter",
						BaseURL:       "https://openrouter.ai/api/v1",
						DiscoveredIDs: []string{"qwen/qwen3.6", "anthropic/claude-sonnet-4-6"},
						SupportsTools: true,
					},
				},
			},
			{
				Name:                "codex",
				Surface:             "codex",
				CostClass:           "medium",
				IsSubscription:      true,
				AutoRoutingEligible: true,
				ExactPinSupport:     true,
				Available:           true,
				QuotaOK:             true,
				SubscriptionOK:      true,
				SupportedReasoning:  []string{"low", "medium", "high"},
				SupportedPerms:      []string{"safe", "supervised", "unrestricted"},
				SupportsTools:       true,
				DefaultModel:        "gpt-5.4",
			},
			{
				Name:                "claude",
				Surface:             "claude",
				CostClass:           "medium",
				IsSubscription:      true,
				AutoRoutingEligible: true,
				ExactPinSupport:     true,
				Available:           true,
				QuotaOK:             true,
				SubscriptionOK:      true,
				SupportedReasoning:  []string{"low", "medium", "high"},
				SupportedPerms:      []string{"safe", "supervised", "unrestricted"},
				SupportsTools:       true,
				DefaultModel:        "claude-sonnet-4-6",
			},
		},
		CatalogResolver: func(ref, surface string) (string, bool) {
			// Trivial test catalog.
			switch ref {
			case "cheap":
				if surface == "embedded-openai" {
					return "qwen/qwen3.6", true
				}
				if surface == "codex" {
					return "gpt-5.4-mini", true
				}
				if surface == "claude" {
					return "claude-haiku-4-6", true
				}
			case "smart":
				if surface == "claude" {
					return "claude-opus-4-7", true
				}
				if surface == "codex" {
					return "gpt-5.4", true
				}
			case "qwen/qwen3.6":
				if surface == "embedded-openai" {
					return "qwen/qwen3.6", true
				}
			}
			return "", false
		},
		Now: time.Date(2026, 4, 18, 0, 0, 0, 0, time.UTC),
	}
}

// === Smell 1: ddx-8610020e — Provider field present from day one ===
//
// RouteRequest carries Provider as a soft preference; the engine ranks
// candidates that match req.Provider higher, and applies a hard pin
// when both Harness and Provider are set.
func TestSmellProviderFieldDayOne(t *testing.T) {
	in := newTestRoutingEngine()

	// Soft preference: req.Provider boosts matching candidates.
	req := Request{Profile: "cheap", Provider: "vidar-omlx"}
	dec, err := Resolve(req, in)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if dec.Provider != "vidar-omlx" {
		t.Errorf("provider=vidar-omlx soft pref: got %q, want vidar-omlx", dec.Provider)
	}

	// Hard pin: Harness=agent + Provider=openrouter constrains routing.
	hardReq := Request{Harness: "agent", Provider: "openrouter", Model: "qwen/qwen3.6"}
	dec2, err := Resolve(hardReq, in)
	if err != nil {
		t.Fatalf("hard pin Resolve: %v", err)
	}
	if dec2.Provider != "openrouter" {
		t.Errorf("hard pin: got provider=%q, want openrouter", dec2.Provider)
	}
}

// === Smell 2: ddx-0486e601 — canonical-form fuzzy matcher ===
//
// "qwen/qwen3.6" must match "Qwen3.6-35B-A3B-4bit" (case + vendor
// prefix normalization).
func TestSmellCanonicalFormFuzzyMatcher(t *testing.T) {
	in := newTestRoutingEngine()

	// Direct fuzzy-match function.
	pool := []string{"Qwen3.6-35B-A3B-4bit", "MiniMax-M2.5-MLX-4bit"}
	matched := FuzzyMatch("qwen/qwen3.6", pool)
	if matched != "Qwen3.6-35B-A3B-4bit" {
		t.Errorf("FuzzyMatch(qwen/qwen3.6): got %q, want Qwen3.6-35B-A3B-4bit", matched)
	}

	// End-to-end: Model="qwen/qwen3.6" + Provider=vidar-omlx resolves to
	// the provider-native ID via fuzzy match.
	req := Request{Provider: "vidar-omlx", Harness: "agent", Model: "qwen/qwen3.6"}
	dec, err := Resolve(req, in)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if dec.Model != "Qwen3.6-35B-A3B-4bit" {
		t.Errorf("end-to-end fuzzy resolution: got model=%q, want Qwen3.6-35B-A3B-4bit", dec.Model)
	}
}

// === Smell 3: ddx-4817edfd — capability gating ===
//
// Per-(harness, provider, model) capability gating: context window,
// tool support, effort, permissions.
func TestSmellCapabilityGating(t *testing.T) {
	t.Run("context window", func(t *testing.T) {
		in := newTestRoutingEngine()
		// MiniMax has no ContextWindow entry; qwen has 256k.
		// Request a 80k-token prompt — qwen should pass, MiniMax should be neutral
		// (unknown ctx → not rejected).
		req := Request{
			Provider:              "vidar-omlx",
			Harness:               "agent",
			Model:                 "Qwen3.6-35B-A3B-4bit",
			EstimatedPromptTokens: 80000,
		}
		dec, err := Resolve(req, in)
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if dec.Model != "Qwen3.6-35B-A3B-4bit" {
			t.Errorf("got model=%q, want Qwen3.6", dec.Model)
		}

		// Now request a 300k-token prompt — qwen (256k) should be rejected.
		req.EstimatedPromptTokens = 300000
		dec2, err := Resolve(req, in)
		if err == nil && dec2.Eligible() {
			// Find qwen candidate, should be ineligible.
			for _, c := range dec2.Candidates {
				if c.Model == "Qwen3.6-35B-A3B-4bit" && c.Eligible {
					t.Errorf("300k prompt: qwen (256k) should be ineligible")
				}
			}
		}
	})

	t.Run("tool support", func(t *testing.T) {
		in := newTestRoutingEngine()
		// Mark vidar-omlx provider as tool-incapable.
		for i, h := range in.Harnesses {
			if h.Name == "agent" {
				for j, p := range h.Providers {
					if p.Name == "vidar-omlx" {
						in.Harnesses[i].Providers[j].SupportsTools = false
					}
				}
				// Disable harness-level tool support too so the OR doesn't rescue.
				in.Harnesses[i].SupportsTools = false
			}
		}
		req := Request{Profile: "cheap", Provider: "vidar-omlx", RequiresTools: true}
		dec, err := Resolve(req, in)
		// vidar-omlx must not be eligible.
		if err == nil {
			for _, c := range dec.Candidates {
				if c.Provider == "vidar-omlx" && c.Eligible {
					t.Errorf("vidar-omlx without tools must be ineligible when RequiresTools=true")
				}
			}
		}
	})

	t.Run("reasoning", func(t *testing.T) {
		// A harness with no SupportedReasoning must reject reasoning=high.
		in := newTestRoutingEngine()
		in.Harnesses = append(in.Harnesses, HarnessEntry{
			Name:                "no-reasoning-harness",
			Surface:             "test",
			CostClass:           "medium",
			AutoRoutingEligible: true,
			Available:           true,
			QuotaOK:             true,
			SubscriptionOK:      true,
			ExactPinSupport:     true,
			DefaultModel:        "x",
		})
		req := Request{Profile: "standard", Reasoning: "high"}
		dec, err := Resolve(req, in)
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		for _, c := range dec.Candidates {
			if c.Harness == "no-reasoning-harness" && c.Eligible {
				t.Errorf("no-reasoning-harness must be ineligible when Reasoning=high")
			}
		}
	})

	t.Run("reasoning off imposes no requirement", func(t *testing.T) {
		cap := Capabilities{}
		for _, value := range []string{"off", "0", "none", "false"} {
			if got := CheckGating(cap, Request{Reasoning: value}); got != "" {
				t.Fatalf("Reasoning=%q should not gate candidate, got %q", value, got)
			}
		}
	})

	t.Run("extended reasoning requires advertised support", func(t *testing.T) {
		cap := Capabilities{SupportedReasoning: []string{"low", "medium", "high", "xhigh", "max"}}
		if got := CheckGating(cap, Request{Reasoning: "x-high"}); got != "" {
			t.Fatalf("x-high should normalize to advertised xhigh, got %q", got)
		}
		if got := CheckGating(Capabilities{SupportedReasoning: []string{"low"}}, Request{Reasoning: "max"}); got == "" {
			t.Fatal("max should reject candidates that do not advertise it")
		}
	})

	t.Run("numeric reasoning gates against max", func(t *testing.T) {
		cap := Capabilities{MaxReasoningTokens: 4096}
		if got := CheckGating(cap, Request{Reasoning: "2048"}); got != "" {
			t.Fatalf("numeric value under max should pass, got %q", got)
		}
		if got := CheckGating(cap, Request{Reasoning: "8192"}); got == "" {
			t.Fatal("numeric value over max should fail")
		}
	})
}

// === Smell 4: ddx-3c5ba7cc — profile-aware tier escalation ===
//
// EscalateProfileAware must respect provider affinity: when the
// pinned provider can't serve the next tier's model, that tier is skipped.
func TestSmellProfileAwareEscalation(t *testing.T) {
	in := newTestRoutingEngine()
	// Restrict vidar-omlx to qwen3.6 (cheap), nothing for smart.
	for i, h := range in.Harnesses {
		if h.Name == "agent" {
			for j, p := range h.Providers {
				if p.Name == "vidar-omlx" {
					// Only the cheap-tier model is discovered.
					in.Harnesses[i].Providers[j].DiscoveredIDs = []string{"Qwen3.6-35B-A3B-4bit"}
				}
			}
		}
	}
	// With Harness=agent+Provider=vidar-omlx pin, escalating to "smart"
	// should fail (the catalog smart→claude-opus surface mismatch + provider
	// pin means no candidate is viable on the agent harness).
	ladder := []string{"cheap", "smart"}
	req := Request{Harness: "agent", Provider: "vidar-omlx", Profile: "cheap"}
	next := EscalateProfileAware("cheap", ladder, req, in)
	// smart catalog → claude-opus (surface=claude), but Harness=agent pinned,
	// so smart isn't viable. EscalateProfileAware should return "" or skip.
	if next == "smart" {
		t.Errorf("escalation to smart under Harness=agent+Provider=vidar-omlx should be skipped")
	}
}

// === Smell 5: single observation store + cooldown abstraction ===
//
// Cooldown demotion is applied uniformly via Inputs.ProviderCooldowns.
// A provider in cooldown loses score; without demotion it would have won.
func TestSmellSingleCooldownAbstraction(t *testing.T) {
	in := newTestRoutingEngine()
	// Without cooldown: with provider affinity to vidar-omlx, vidar wins.
	baseReq := Request{Profile: "cheap", Harness: "agent", Provider: "vidar-omlx", Model: "qwen/qwen3.6"}
	dec0, err := Resolve(baseReq, in)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if dec0.Provider != "vidar-omlx" {
		t.Fatalf("baseline: vidar should win with affinity; got %q", dec0.Provider)
	}

	// Now put vidar-omlx in cooldown. Other providers are still eligible
	// (provider pin is soft when paired only with Harness — not a hard reject)
	// so the cooldown demotion lets a non-cooldowned candidate take over.
	in.ProviderCooldowns = map[string]time.Time{
		"vidar-omlx": in.Now.Add(-5 * time.Second),
	}
	in.CooldownDuration = 30 * time.Second

	// Use a cheap-tier request without the hard provider pin so cooldown
	// demotion is observable.
	cooldownReq := Request{Profile: "cheap", Harness: "agent", Model: "qwen/qwen3.6"}
	dec, err := Resolve(cooldownReq, in)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	// Vidar should still resolve but with a -50 cooldown demotion — openrouter
	// (no cooldown) overtakes via score, even though both share local cost class.
	if dec.Provider == "vidar-omlx" {
		t.Errorf("under cooldown vidar-omlx should NOT be top pick; got %q", dec.Provider)
	}

	// After cooldown expires (Now > failedAt + cooldown), vidar is no longer demoted.
	in.Now = in.Now.Add(60 * time.Second)
	dec2, err := Resolve(cooldownReq, in)
	if err != nil {
		t.Fatalf("Resolve after cooldown: %v", err)
	}
	// Find both candidates' eligibility/scores.
	var vidarScore, openrouterScore float64
	for _, c := range dec2.Candidates {
		switch c.Provider {
		case "vidar-omlx":
			vidarScore = c.Score
		case "openrouter":
			openrouterScore = c.Score
		}
	}
	// Confirm cooldown demotion is gone (scores within 1.0 of each other).
	if vidarScore < openrouterScore-1 {
		t.Errorf("after cooldown expiry, vidar should not be demoted; vidar=%.1f openrouter=%.1f", vidarScore, openrouterScore)
	}
}

// === Smell 6: TestOnly harnesses excluded from tier routing ===
//
// Regression for ddx-869848ec (carried forward from DDx routing.go):
// TestOnly harnesses (script, virtual) must never leak into profile-based
// routing — only explicit Harness override reaches them.
func TestSmellTestOnlyHarnessExcluded(t *testing.T) {
	in := newTestRoutingEngine()
	for _, name := range []string{"script", "virtual"} {
		in.Harnesses = append(in.Harnesses, HarnessEntry{
			Name:            name,
			Surface:         name,
			CostClass:       "local",
			IsLocal:         true,
			TestOnly:        true,
			Available:       true,
			QuotaOK:         true,
			SubscriptionOK:  true,
			ExactPinSupport: true,
			DefaultModel:    "recorded",
		})
	}

	for _, profile := range []string{"cheap", "standard", "smart"} {
		req := Request{Profile: profile}
		dec, err := Resolve(req, in)
		if err != nil {
			continue
		}
		for _, c := range dec.Candidates {
			if c.Harness == "script" || c.Harness == "virtual" {
				t.Errorf("profile=%s: TestOnly harness %q leaked into candidates", profile, c.Harness)
			}
		}
	}

	for _, name := range []string{"script", "virtual"} {
		req := Request{Harness: name}
		dec, err := Resolve(req, in)
		if err != nil {
			t.Fatalf("explicit Harness=%s must succeed: %v", name, err)
		}
		if dec.Harness != name {
			t.Errorf("explicit Harness=%s: got %q", name, dec.Harness)
		}
	}
}

func TestAutoRoutingEligibilityGate(t *testing.T) {
	in := newTestRoutingEngine()
	for _, h := range []HarnessEntry{
		{
			Name:            "gemini",
			Surface:         "gemini",
			CostClass:       "experimental",
			Available:       true,
			QuotaOK:         true,
			SubscriptionOK:  true,
			ExactPinSupport: true,
			SupportsTools:   true,
			DefaultModel:    "gemini-test",
		},
		{
			Name:            "opencode",
			Surface:         "embedded-openai",
			CostClass:       "medium",
			Available:       true,
			QuotaOK:         true,
			SubscriptionOK:  true,
			ExactPinSupport: true,
			SupportsTools:   true,
			DefaultModel:    "opencode-test",
		},
		{
			Name:            "pi",
			Surface:         "pi",
			CostClass:       "medium",
			Available:       true,
			QuotaOK:         true,
			SubscriptionOK:  true,
			ExactPinSupport: true,
			SupportsTools:   true,
			DefaultModel:    "pi-test",
		},
	} {
		in.Harnesses = append(in.Harnesses, h)
	}

	dec, err := Resolve(Request{Profile: "smart"}, in)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	for _, c := range dec.Candidates {
		switch c.Harness {
		case "agent", "codex", "claude":
			// Harnesses marked auto-routing eligible may appear.
		case "gemini", "opencode", "pi":
			t.Fatalf("non-full-coverage harness %q leaked into automatic routing candidates", c.Harness)
		default:
			t.Fatalf("unexpected harness %q in automatic routing candidates", c.Harness)
		}
	}

	for _, name := range []string{"gemini", "opencode", "pi"} {
		req := Request{Harness: name, Model: name + "-test"}
		dec, err := Resolve(req, in)
		if err != nil {
			t.Fatalf("explicit Harness=%s must remain routable: %v", name, err)
		}
		if dec.Harness != name {
			t.Fatalf("explicit Harness=%s: got %q", name, dec.Harness)
		}
	}
}

func TestSecondaryHarnessesRequireOperationalEvidenceForAutoRouting(t *testing.T) {
	in := Inputs{
		Harnesses: []HarnessEntry{
			{
				Name:                "codex",
				Surface:             "codex",
				CostClass:           "medium",
				IsSubscription:      true,
				AutoRoutingEligible: true,
				Available:           true,
				QuotaOK:             true,
				SubscriptionOK:      true,
				ExactPinSupport:     true,
				SupportsTools:       true,
				DefaultModel:        "gpt-5.4",
			},
			{
				Name:            "opencode",
				Surface:         "embedded-openai",
				CostClass:       "medium",
				Available:       true,
				QuotaOK:         true,
				SubscriptionOK:  true,
				ExactPinSupport: true,
				SupportsTools:   true,
				DefaultModel:    "opencode/gpt-5.4",
			},
			{
				Name:            "pi",
				Surface:         "pi",
				CostClass:       "medium",
				Available:       true,
				QuotaOK:         true,
				SubscriptionOK:  true,
				ExactPinSupport: true,
				SupportsTools:   true,
				DefaultModel:    "gemini-2.5-flash",
			},
			{
				Name:            "gemini",
				Surface:         "gemini",
				CostClass:       "experimental",
				Available:       true,
				QuotaOK:         true,
				SubscriptionOK:  true,
				ExactPinSupport: true,
				SupportsTools:   true,
				DefaultModel:    "gemini-2.5-flash",
			},
		},
		Now: time.Date(2026, 4, 18, 0, 0, 0, 0, time.UTC),
	}

	dec, err := Resolve(Request{Profile: "standard"}, in)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	seen := map[string]Candidate{}
	for _, c := range dec.Candidates {
		seen[c.Harness] = c
	}
	for _, name := range []string{"opencode", "pi"} {
		if _, ok := seen[name]; ok {
			t.Fatalf("secondary harness %q should remain outside auto-routing candidates without cost/quota evidence", name)
		}
	}
	if _, ok := seen["gemini"]; ok {
		t.Fatalf("gemini should remain outside auto-routing candidates when AutoRoutingEligible is false")
	}
}

// === Profile policy semantics ported from DDx routing_test.go ===

func TestCheapPrefersLocal(t *testing.T) {
	in := newTestRoutingEngine()
	req := Request{Profile: "cheap"}
	dec, err := Resolve(req, in)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if dec.Harness != "agent" {
		t.Errorf("cheap profile: got harness=%q, want agent (local)", dec.Harness)
	}
}

func TestSmartPrefersCloud(t *testing.T) {
	in := newTestRoutingEngine()
	req := Request{Profile: "smart"}
	dec, err := Resolve(req, in)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if dec.Harness == "agent" {
		t.Errorf("smart profile: got harness=agent (local); should prefer cloud")
	}
}

func TestProfileRejectsUnsupportedSurfaceWithoutModel(t *testing.T) {
	in := Inputs{
		Harnesses: []HarnessEntry{
			{
				Name:                "gemini",
				Surface:             "gemini",
				CostClass:           "experimental",
				AutoRoutingEligible: true,
				Available:           true,
				QuotaOK:             true,
				SubscriptionOK:      true,
				ExactPinSupport:     true,
				SupportsTools:       true,
			},
		},
		CatalogResolver: func(ref, surface string) (string, bool) {
			return "", false
		},
	}

	dec, err := Resolve(Request{Profile: "smart"}, in)
	if err == nil {
		t.Fatal("expected unsupported profile surface to be rejected")
	}
	if dec == nil || len(dec.Candidates) != 1 {
		t.Fatalf("expected one rejected candidate, got %#v", dec)
	}
	c := dec.Candidates[0]
	if c.Eligible {
		t.Fatalf("gemini candidate should be ineligible for profile routing: %#v", c)
	}
	if c.Model != "" {
		t.Fatalf("unsupported profile should not produce an empty-model eligible route, got model %q", c.Model)
	}
	if !strings.Contains(c.Reason, `profile "smart" not available on surface "gemini"`) {
		t.Fatalf("unexpected rejection reason: %q", c.Reason)
	}
}

func TestSmartDoesNotSelectUnmodeledGeminiOverModeledAgent(t *testing.T) {
	in := Inputs{
		Harnesses: []HarnessEntry{
			{
				Name:                "agent",
				Surface:             "embedded-openai",
				CostClass:           "local",
				IsLocal:             true,
				AutoRoutingEligible: true,
				Available:           true,
				QuotaOK:             true,
				SubscriptionOK:      true,
				ExactPinSupport:     true,
				SupportsTools:       true,
				DefaultModel:        "qwen3.5-27b",
			},
			{
				Name:            "gemini",
				Surface:         "gemini",
				CostClass:       "experimental",
				Available:       true,
				QuotaOK:         true,
				SubscriptionOK:  true,
				ExactPinSupport: true,
				SupportsTools:   true,
			},
		},
		CatalogResolver: func(ref, surface string) (string, bool) {
			if ref == "smart" && surface == "embedded-openai" {
				return "qwen3.5-27b", true
			}
			return "", false
		},
	}

	dec, err := Resolve(Request{Profile: "smart"}, in)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if dec.Harness != "agent" {
		t.Fatalf("smart should use modeled agent route, got harness=%q model=%q", dec.Harness, dec.Model)
	}
	for _, c := range dec.Candidates {
		if c.Harness == "gemini" && c.Eligible {
			t.Fatalf("gemini should not be eligible without a smart profile model: %#v", c)
		}
	}
}

func TestGeminiProfileRoutingResolvesConcreteModels(t *testing.T) {
	in := Inputs{
		Harnesses: []HarnessEntry{
			{
				Name:                "gemini",
				Surface:             "gemini",
				CostClass:           "medium",
				IsSubscription:      true,
				AutoRoutingEligible: true,
				Available:           true,
				QuotaOK:             true,
				SubscriptionOK:      true,
				ExactPinSupport:     true,
				SupportsTools:       true,
				SupportedPerms:      []string{"safe", "supervised", "unrestricted"},
			},
		},
		CatalogResolver: func(ref, surface string) (string, bool) {
			if surface != "gemini" {
				return "", false
			}
			switch ref {
			case "smart":
				return "gemini-2.5-pro", true
			case "standard":
				return "gemini-2.5-flash", true
			case "cheap":
				return "gemini-2.5-flash-lite", true
			default:
				return "", false
			}
		},
	}

	for profile, wantModel := range map[string]string{
		"smart":    "gemini-2.5-pro",
		"standard": "gemini-2.5-flash",
		"cheap":    "gemini-2.5-flash-lite",
	} {
		dec, err := Resolve(Request{Profile: profile}, in)
		if err != nil {
			t.Fatalf("Resolve profile=%s: %v", profile, err)
		}
		if dec.Harness != "gemini" || dec.Model != wantModel {
			t.Fatalf("profile=%s: got harness=%q model=%q, want gemini/%s", profile, dec.Harness, dec.Model, wantModel)
		}
	}
}

func TestReasoningRequestsDoNotSelectGemini(t *testing.T) {
	in := Inputs{
		Harnesses: []HarnessEntry{
			{
				Name:                "gemini",
				Surface:             "gemini",
				CostClass:           "medium",
				IsSubscription:      true,
				AutoRoutingEligible: true,
				Available:           true,
				QuotaOK:             true,
				SubscriptionOK:      true,
				ExactPinSupport:     true,
				SupportsTools:       true,
				DefaultModel:        "gemini-2.5-pro",
			},
			{
				Name:                "codex",
				Surface:             "codex",
				CostClass:           "medium",
				IsSubscription:      true,
				AutoRoutingEligible: true,
				Available:           true,
				QuotaOK:             true,
				SubscriptionOK:      true,
				ExactPinSupport:     true,
				SupportsTools:       true,
				SupportedReasoning:  []string{"low", "medium", "high"},
				DefaultModel:        "gpt-5.4",
			},
		},
		CatalogResolver: func(ref, surface string) (string, bool) {
			if ref != "smart" {
				return "", false
			}
			if surface == "gemini" {
				return "gemini-2.5-pro", true
			}
			if surface == "codex" {
				return "gpt-5.4", true
			}
			return "", false
		},
	}

	dec, err := Resolve(Request{Profile: "smart", Reasoning: "high"}, in)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if dec.Harness == "gemini" {
		t.Fatalf("reasoning request should not select gemini: %#v", dec)
	}
	foundGemini := false
	for _, c := range dec.Candidates {
		if c.Harness == "gemini" {
			foundGemini = true
			if c.Eligible || !strings.Contains(c.Reason, `reasoning "high" not supported`) {
				t.Fatalf("gemini reasoning candidate: %#v", c)
			}
		}
	}
	if !foundGemini {
		t.Fatal("expected gemini candidate to prove reasoning gate")
	}
}

func TestSmartSelectsGeminiOnlyWhenEligibleBestCandidate(t *testing.T) {
	in := Inputs{
		Harnesses: []HarnessEntry{
			{
				Name:                "agent",
				Surface:             "embedded-openai",
				CostClass:           "local",
				IsLocal:             true,
				AutoRoutingEligible: true,
				Available:           true,
				QuotaOK:             true,
				SubscriptionOK:      true,
				ExactPinSupport:     true,
				SupportsTools:       true,
				DefaultModel:        "qwen3.5-27b",
			},
			{
				Name:                "gemini",
				Surface:             "gemini",
				CostClass:           "medium",
				IsSubscription:      true,
				AutoRoutingEligible: true,
				Available:           true,
				QuotaOK:             true,
				SubscriptionOK:      true,
				ExactPinSupport:     true,
				SupportsTools:       true,
				DefaultModel:        "gemini-2.5-pro",
			},
		},
		CatalogResolver: func(ref, surface string) (string, bool) {
			if ref != "smart" {
				return "", false
			}
			if surface == "gemini" {
				return "gemini-2.5-pro", true
			}
			if surface == "embedded-openai" {
				return "qwen3.5-27b", true
			}
			return "", false
		},
	}

	dec, err := Resolve(Request{Profile: "smart"}, in)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if dec.Harness != "gemini" || dec.Model != "gemini-2.5-pro" {
		t.Fatalf("smart should select eligible gemini over lower-scored local route, got harness=%q model=%q", dec.Harness, dec.Model)
	}

	in.Harnesses[1].SubscriptionOK = false
	in.Harnesses[1].QuotaOK = false
	dec, err = Resolve(Request{Profile: "smart"}, in)
	if err != nil {
		t.Fatalf("Resolve after auth gate: %v", err)
	}
	if dec.Harness == "gemini" {
		t.Fatalf("gemini should not win when auth/quota evidence is missing: %#v", dec)
	}
}

func TestStableTieBreakerAlphabetical(t *testing.T) {
	// Two equal-score candidates → alphabetical winner.
	in := Inputs{
		Harnesses: []HarnessEntry{
			{Name: "zharness", Surface: "x", CostClass: "medium", AutoRoutingEligible: true, Available: true, QuotaOK: true, SubscriptionOK: true, DefaultModel: "z", ExactPinSupport: true, SupportsTools: true},
			{Name: "aharness", Surface: "x", CostClass: "medium", AutoRoutingEligible: true, Available: true, QuotaOK: true, SubscriptionOK: true, DefaultModel: "a", ExactPinSupport: true, SupportsTools: true},
		},
	}
	req := Request{Profile: "standard"}
	dec, err := Resolve(req, in)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if dec.Harness != "aharness" {
		t.Errorf("alphabetical tiebreak: got %q, want aharness", dec.Harness)
	}
}

func TestNoViableCandidate(t *testing.T) {
	in := Inputs{
		Harnesses: []HarnessEntry{
			{Name: "down", AutoRoutingEligible: true, Available: false},
		},
	}
	req := Request{Profile: "cheap"}
	_, err := Resolve(req, in)
	if err == nil {
		t.Fatal("expected error when no harness available")
	}
	if !strings.Contains(err.Error(), "no viable") {
		t.Errorf("error should mention 'no viable': %v", err)
	}
}

func TestResolveRoute_GeminiRejectsNonGeminiModel(t *testing.T) {
	gemini := HarnessEntry{
		Name:                "gemini",
		Surface:             "gemini",
		CostClass:           "medium",
		IsSubscription:      true,
		AutoRoutingEligible: true,
		ExactPinSupport:     true,
		Available:           true,
		QuotaOK:             true,
		SubscriptionOK:      true,
		DefaultModel:        "gemini-2.5-flash",
		SupportedModels:     []string{"gemini-2.5-pro", "gemini-2.5-flash", "gemini-2.5-flash-lite"},
		SupportsTools:       true,
	}
	agent := HarnessEntry{
		Name:                "agent",
		Surface:             "embedded-openai",
		CostClass:           "local",
		IsLocal:             true,
		AutoRoutingEligible: true,
		ExactPinSupport:     true,
		Available:           true,
		QuotaOK:             true,
		SubscriptionOK:      true,
		SupportedModels:     []string{"qwen/qwen3.6"},
		SupportsTools:       true,
		Providers: []ProviderEntry{{
			Name:               "minimax",
			EndpointName:       "local",
			DiscoveredIDs:      []string{"minimax/minimax-m2.7"},
			DiscoveryAttempted: true,
			SupportsTools:      true,
		}},
	}

	in := Inputs{Harnesses: []HarnessEntry{gemini, agent}}
	dec, err := Resolve(Request{Profile: "default", Model: "minimax/minimax-m2.7"}, in)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if dec.Harness != "agent" || dec.Model != "minimax/minimax-m2.7" {
		t.Fatalf("got harness=%q model=%q, want agent/minimax", dec.Harness, dec.Model)
	}
	for _, c := range dec.Candidates {
		if c.Harness == "gemini" && c.Eligible {
			t.Fatalf("gemini must reject non-gemini exact pin: %#v", c)
		}
		if c.Harness == "gemini" && c.Reason != "model not in harness allow-list" {
			t.Fatalf("gemini rejection reason=%q, want allow-list reason", c.Reason)
		}
	}

	dec, err = Resolve(Request{Profile: "default", Model: "minimax/minimax-m2.7"}, Inputs{Harnesses: []HarnessEntry{gemini}})
	if err == nil {
		t.Fatal("expected no viable candidate without agent live endpoint")
	}
	var noViable *NoViableCandidateError
	if !errors.As(err, &noViable) {
		t.Fatalf("error type=%T, want *NoViableCandidateError: %v", err, err)
	}
	if dec != nil && dec.Harness == "gemini" {
		t.Fatalf("must not pick gemini for non-gemini model: %#v", dec)
	}
	for _, c := range dec.Candidates {
		if c.Harness == "gemini" {
			if c.Eligible {
				t.Fatalf("gemini candidate must be ineligible: %#v", c)
			}
			if c.Reason != "model not in harness allow-list" {
				t.Fatalf("gemini rejection reason=%q, want allow-list reason", c.Reason)
			}
		}
	}
}

func TestResolveRoute_GeminiAllowListExactPinSucceeds(t *testing.T) {
	gemini := HarnessEntry{
		Name:                "gemini",
		Surface:             "gemini",
		CostClass:           "medium",
		IsSubscription:      true,
		AutoRoutingEligible: true,
		ExactPinSupport:     true,
		Available:           true,
		QuotaOK:             true,
		SubscriptionOK:      true,
		DefaultModel:        "gemini-2.5-flash",
		SupportedModels:     []string{"gemini-2.5-pro", "gemini-2.5-flash", "gemini-2.5-flash-lite"},
		SupportsTools:       true,
	}

	dec, err := Resolve(Request{Profile: "default", Model: "gemini-2.5-flash"}, Inputs{Harnesses: []HarnessEntry{gemini}})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if dec.Harness != "gemini" || dec.Model != "gemini-2.5-flash" {
		t.Fatalf("got harness=%q model=%q, want gemini/gemini-2.5-flash", dec.Harness, dec.Model)
	}
}

func TestHarnessOverrideRejectsOthers(t *testing.T) {
	in := newTestRoutingEngine()
	req := Request{Harness: "codex"}
	dec, err := Resolve(req, in)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if dec.Harness != "codex" {
		t.Errorf("Harness=codex pin: got %q, want codex", dec.Harness)
	}
	// Only codex candidates should appear.
	for _, c := range dec.Candidates {
		if c.Harness != "codex" {
			t.Errorf("Harness=codex pin: candidate %q leaked", c.Harness)
		}
	}
}

func TestLocalAliasResolvesToAgent(t *testing.T) {
	in := newTestRoutingEngine()
	req := Request{Harness: "local"}
	dec, err := Resolve(req, in)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if dec.Harness != "agent" {
		t.Errorf("Harness=local must resolve to agent; got %q", dec.Harness)
	}
}

// Eligible reports whether the Decision picked a viable candidate.
func (d *Decision) Eligible() bool {
	return d != nil && d.Harness != ""
}
