package agent

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/DocumentDrivenDX/agent/internal/harnesses"
)

func TestAxesOverridden_EmptyForUnpinnedRequest(t *testing.T) {
	got := axesOverridden(ServiceExecuteRequest{Profile: "smart", ModelRef: "code-medium"})
	if len(got) != 0 {
		t.Fatalf("axesOverridden(profile-only) = %v, want empty", got)
	}
}

func TestAxesOverridden_TracksEachAxisIndependently(t *testing.T) {
	cases := []struct {
		name string
		req  ServiceExecuteRequest
		want []string
	}{
		{"harness only", ServiceExecuteRequest{Harness: "claude"}, []string{overrideAxisHarness}},
		{"provider only", ServiceExecuteRequest{Provider: "openrouter"}, []string{overrideAxisProvider}},
		{"model only", ServiceExecuteRequest{Model: "opus-4.7"}, []string{overrideAxisModel}},
		{"all three", ServiceExecuteRequest{Harness: "claude", Provider: "openrouter", Model: "opus-4.7"},
			[]string{overrideAxisHarness, overrideAxisProvider, overrideAxisModel}},
		{"harness+model", ServiceExecuteRequest{Harness: "claude", Model: "opus-4.7"},
			[]string{overrideAxisHarness, overrideAxisModel}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := axesOverridden(tc.req); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("axesOverridden(%+v) = %v, want %v", tc.req, got, tc.want)
			}
		})
	}
}

func TestBuildPromptFeatures_NullableEstimatedTokens(t *testing.T) {
	pf := buildPromptFeatures(ServiceExecuteRequest{})
	if pf.EstimatedTokens != nil {
		t.Fatalf("EstimatedTokens for empty request: want nil, got %d", *pf.EstimatedTokens)
	}
	pf = buildPromptFeatures(ServiceExecuteRequest{EstimatedPromptTokens: 12500, RequiresTools: true, Reasoning: "high"})
	if pf.EstimatedTokens == nil || *pf.EstimatedTokens != 12500 {
		t.Fatalf("EstimatedTokens: want 12500, got %v", pf.EstimatedTokens)
	}
	if !pf.RequiresTools {
		t.Fatalf("RequiresTools: want true, got false")
	}
	if pf.Reasoning != "high" {
		t.Fatalf("Reasoning: want high, got %q", pf.Reasoning)
	}
}

func TestOverrideReasonHint_FromMetadata(t *testing.T) {
	if got := overrideReasonHint(ServiceExecuteRequest{}); got != "" {
		t.Fatalf("empty request reason_hint: want empty, got %q", got)
	}
	req := ServiceExecuteRequest{Metadata: map[string]string{"override.reason": "model needs to match training"}}
	if got := overrideReasonHint(req); got != "model needs to match training" {
		t.Fatalf("reason_hint: got %q", got)
	}
}

// coincidenceFakeService returns a *service backed by a fakeServiceConfig
// that exposes a single provider so ResolveRoute deterministically picks
// that one provider regardless of pin. Used by coincidence and per-axis
// override tests to anchor stripped-auto resolution.
func coincidenceFakeService() *service {
	sc := &fakeServiceConfig{
		providers: map[string]ServiceProviderEntry{
			"local": {Type: "test", BaseURL: "http://127.0.0.1:9999/v1", Model: "model-a"},
		},
		names:       []string{"local"},
		defaultName: "local",
	}
	return publicRouteTraceService(sc)
}

// TestOverrideEventCoincidentalAgreement covers AC #3 in full: when the
// pin matches what auto-routing would have picked anyway, the override
// event still fires AND match_per_axis is true on every overridden axis.
// Synthesis is real — a fakeServiceConfig with a single provider means
// the stripped auto resolution lands on the same Harness/Provider/Model
// the user pinned.
func TestOverrideEventCoincidentalAgreement(t *testing.T) {
	svc := coincidenceFakeService()

	// Pin Provider only. Stripped auto resolution still picks "local"
	// because it is the sole configured provider — coincidental agreement.
	req := ServiceExecuteRequest{
		Harness:  "agent",
		Provider: "local",
		Model:    "model-a",
	}
	octx := svc.buildOverrideContext(context.Background(), req)
	if octx == nil {
		t.Fatal("buildOverrideContext returned nil for pinned request")
	}
	if !equalAxisSets(octx.payload.AxesOverridden, []string{overrideAxisHarness, overrideAxisProvider, overrideAxisModel}) {
		t.Fatalf("axes_overridden: got %v, want all three", octx.payload.AxesOverridden)
	}
	// Auto decision must be the very same thing the user pinned.
	if octx.payload.AutoDecision.Provider != "local" {
		t.Fatalf("auto provider: got %q, want %q", octx.payload.AutoDecision.Provider, "local")
	}
	if octx.payload.AutoDecision.Harness != "agent" {
		t.Fatalf("auto harness: got %q, want %q", octx.payload.AutoDecision.Harness, "agent")
	}
	if octx.payload.AutoDecision.Model != "model-a" {
		t.Fatalf("auto model: got %q, want %q", octx.payload.AutoDecision.Model, "model-a")
	}
	// match_per_axis must be true everywhere — coincidental agreement.
	for _, axis := range octx.payload.AxesOverridden {
		if !octx.payload.MatchPerAxis[axis] {
			t.Fatalf("match_per_axis[%s] = false; want true (auto=%+v pin=%+v)",
				axis, octx.payload.AutoDecision, octx.payload.UserPin)
		}
	}
	// Event still fires: makeOverrideEvent must succeed. We construct a
	// minimal final event with non-zero Sequence so the override event
	// gets a real preceding sequence number.
	finalRaw, _ := json.Marshal(ServiceFinalData{Status: "success", DurationMS: 1})
	finalEv := ServiceEvent{
		Type:     harnesses.EventTypeFinal,
		Sequence: 5,
		Time:     time.Now().UTC(),
		Data:     finalRaw,
	}
	ev, _, ok := makeOverrideEvent(octx, "test-session", finalEv, nil)
	if !ok {
		t.Fatal("makeOverrideEvent returned ok=false on coincidental agreement")
	}
	if string(ev.Type) != ServiceEventTypeOverride {
		t.Fatalf("override event type: got %q, want %q", ev.Type, ServiceEventTypeOverride)
	}
	if ev.Sequence >= finalEv.Sequence {
		t.Fatalf("override Sequence=%d must precede final Sequence=%d", ev.Sequence, finalEv.Sequence)
	}
}

// TestBuildOverrideContext_AxesIndividuallyAndInCombination covers AC #2's
// per-axis bookkeeping that is awkward to exercise from the external
// integration test (provider-only and model-only need a configured
// service to dispatch successfully). Each subtest pins exactly one or two
// axes and asserts the override-context payload reflects the pin.
func TestBuildOverrideContext_AxesIndividuallyAndInCombination(t *testing.T) {
	svc := coincidenceFakeService()
	cases := []struct {
		name     string
		req      ServiceExecuteRequest
		wantAxes []string
		wantPin  ServiceOverridePin
	}{
		{
			name:     "harness_only",
			req:      ServiceExecuteRequest{Harness: "agent"},
			wantAxes: []string{overrideAxisHarness},
			wantPin:  ServiceOverridePin{Harness: "agent"},
		},
		{
			name:     "provider_only",
			req:      ServiceExecuteRequest{Provider: "local"},
			wantAxes: []string{overrideAxisProvider},
			wantPin:  ServiceOverridePin{Provider: "local"},
		},
		{
			name:     "model_only",
			req:      ServiceExecuteRequest{Model: "model-a"},
			wantAxes: []string{overrideAxisModel},
			wantPin:  ServiceOverridePin{Model: "model-a"},
		},
		{
			name:     "harness_and_provider",
			req:      ServiceExecuteRequest{Harness: "agent", Provider: "local"},
			wantAxes: []string{overrideAxisHarness, overrideAxisProvider},
			wantPin:  ServiceOverridePin{Harness: "agent", Provider: "local"},
		},
		{
			name:     "provider_and_model",
			req:      ServiceExecuteRequest{Provider: "local", Model: "model-a"},
			wantAxes: []string{overrideAxisProvider, overrideAxisModel},
			wantPin:  ServiceOverridePin{Provider: "local", Model: "model-a"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			octx := svc.buildOverrideContext(context.Background(), tc.req)
			if octx == nil {
				t.Fatalf("buildOverrideContext returned nil for %+v", tc.req)
			}
			if !reflect.DeepEqual(octx.payload.AxesOverridden, tc.wantAxes) {
				t.Fatalf("axes_overridden: got %v, want %v", octx.payload.AxesOverridden, tc.wantAxes)
			}
			if octx.payload.UserPin != tc.wantPin {
				t.Fatalf("user_pin: got %+v, want %+v", octx.payload.UserPin, tc.wantPin)
			}
			// MatchPerAxis must contain a key per axis, even when false.
			if len(octx.payload.MatchPerAxis) != len(tc.wantAxes) {
				t.Fatalf("match_per_axis size: got %d (%v), want %d",
					len(octx.payload.MatchPerAxis), octx.payload.MatchPerAxis, len(tc.wantAxes))
			}
			for _, axis := range tc.wantAxes {
				if _, ok := octx.payload.MatchPerAxis[axis]; !ok {
					t.Fatalf("match_per_axis missing key %q: %v", axis, octx.payload.MatchPerAxis)
				}
			}
		})
	}
}

// TestRejectedOverrideOnUnknownProvider covers AC #6's "unknown provider"
// branch. With ServiceConfig present but the pinned provider name absent,
// resolveExecuteRoute must surface ErrUnknownProvider, which in turn must
// be classified as an explicit-pin error and produce a rejected_override
// event (no override event, no channel).
func TestRejectedOverrideOnUnknownProvider(t *testing.T) {
	sc := &fakeServiceConfig{
		providers: map[string]ServiceProviderEntry{
			"local": {Type: "test", BaseURL: "http://127.0.0.1:9999/v1", Model: "model-a"},
		},
		names:       []string{"local"},
		defaultName: "local",
	}
	svc := publicRouteTraceService(sc)

	ch, err := svc.Execute(context.Background(), ServiceExecuteRequest{
		Prompt:   "hi",
		Harness:  "agent",
		Provider: "definitely-not-configured",
	})
	if err == nil {
		t.Fatal("expected typed pin error for unknown provider, got nil")
	}
	if ch != nil {
		t.Fatalf("expected nil channel for typed pin error, got %#v", ch)
	}
	var unknown *ErrUnknownProvider
	if !errors.As(err, &unknown) {
		t.Fatalf("errors.As ErrUnknownProvider: got %T %v", err, err)
	}
	if unknown.Provider != "definitely-not-configured" {
		t.Fatalf("ErrUnknownProvider.Provider: got %q", unknown.Provider)
	}
	rejected, ok := AsRejectedOverride(err)
	if !ok {
		t.Fatalf("AsRejectedOverride: expected wrapper carrying rejected_override payload, got %T %v", err, err)
	}
	if !equalAxisSets(rejected.AxesOverridden, []string{overrideAxisHarness, overrideAxisProvider}) {
		t.Fatalf("rejected.axes_overridden: got %v", rejected.AxesOverridden)
	}
	if rejected.UserPin.Provider != "definitely-not-configured" {
		t.Fatalf("rejected.user_pin.provider: got %q", rejected.UserPin.Provider)
	}
	if rejected.Outcome != nil {
		t.Fatalf("rejected_override must not carry outcome: got %+v", rejected.Outcome)
	}
}

// TestIsExplicitPinError_ClassifiesUnknownProvider locks in the contract
// that the unknown-provider typed error participates in the pin-error
// classification used by Execute to decide rejected_override emission.
func TestIsExplicitPinError_ClassifiesUnknownProvider(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"unknown provider direct", &ErrUnknownProvider{Provider: "x"}, true},
		{"unknown provider wrapped", errors.Join(errors.New("ctx"), &ErrUnknownProvider{Provider: "x"}), true},
		{"orphan model", &ErrHarnessModelIncompatible{Harness: "h", Model: "m"}, true},
		{"profile conflict", &ErrProfilePinConflict{Profile: "smart"}, true},
		{"plain error", errors.New("nope"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isExplicitPinError(tc.err); got != tc.want {
				t.Fatalf("isExplicitPinError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func equalAxisSets(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	gs := make(map[string]bool, len(got))
	for _, g := range got {
		gs[g] = true
	}
	for _, w := range want {
		if !gs[w] {
			return false
		}
	}
	return true
}
