package main

import (
	"bytes"
	"strings"
	"sync"
	"testing"

	agentConfig "github.com/DocumentDrivenDX/agent/internal/config"
	"github.com/DocumentDrivenDX/agent/internal/sampling"
	"github.com/stretchr/testify/assert"
)

// TestSamplingProfileNudge_FiresOnceWhenMissing pins the ADR-007 §7 rule 4
// contract: when the resolver reports MissingProfile, the CLI emits one
// stderr warning per process pointing at the catalog refresh command, and
// stays silent on subsequent calls within the same process.
func TestSamplingProfileNudge_FiresOnceWhenMissing(t *testing.T) {
	// Reset the package-level once and divert the sink for test isolation.
	samplingProfileNudgeOnce = sync.Once{}
	var buf bytes.Buffer
	prevSink := samplingNudgeSink
	samplingNudgeSink = &buf
	t.Cleanup(func() { samplingNudgeSink = prevSink })

	// Simulate the CLI's nudge path. The structure mirrors the block in
	// main.go that calls Resolve and gates the warning on
	// res.MissingProfile.
	emit := func(res sampling.ResolveResult) {
		if res.MissingProfile {
			samplingProfileNudgeOnce.Do(func() {
				_, _ = samplingNudgeSink.Write([]byte(samplingProfileNudgeMessage + "\n"))
			})
		}
	}

	// First call: catalog has no "code" profile → nudge fires.
	emit(sampling.Resolve(nil, "any-model", "code", nil))
	out := buf.String()
	assert.Contains(t, out, "sampling_profiles.code", "warning identifies the missing profile name")
	assert.Contains(t, out, "ddx-agent catalog update", "warning points at the refresh command")
	assert.Contains(t, out, "ADR-007", "warning cites the governing artifact for grep-ability")
	assert.Equal(t, 1, strings.Count(out, samplingProfileNudgeMessage), "exactly one warning on first miss")

	// Second call (same process): nudge is silent even though MissingProfile
	// fires again.
	buf.Reset()
	emit(sampling.Resolve(nil, "any-model", "code", nil))
	assert.Empty(t, buf.String(), "second miss in the same process must not re-warn")
}

// TestSamplingProfileNudge_SoftMessageForImplicitGenerationConfig confirms
// the ADR-007 §7 rule 4 toning rule: providers whose servers auto-load
// model-card defaults (vLLM) get the softer "note" wording, not the
// "warning" — they are not in the loop-bug regime.
func TestSamplingProfileNudge_SoftMessageForImplicitGenerationConfig(t *testing.T) {
	samplingProfileNudgeOnce = sync.Once{}
	var buf bytes.Buffer
	prevSink := samplingNudgeSink
	samplingNudgeSink = &buf
	t.Cleanup(func() { samplingNudgeSink = prevSink })

	emit := func(res sampling.ResolveResult, providerType string) {
		if res.MissingProfile {
			msg := samplingProfileNudgeMessage
			if agentConfig.ProviderImplicitGenerationConfig(providerType) {
				msg = samplingProfileNudgeMessageImplicit
			}
			samplingProfileNudgeOnce.Do(func() {
				_, _ = samplingNudgeSink.Write([]byte(msg + "\n"))
			})
		}
	}

	emit(sampling.Resolve(nil, "any-model", "code", nil), "vllm")
	out := buf.String()
	assert.Contains(t, out, "note:", "vLLM gets the soft 'note:' prefix, not 'warning:'")
	assert.Contains(t, out, "generation_config.json", "soft message names the model-card default mechanism")
	assert.Contains(t, out, "ddx-agent catalog update", "soft message still points at the refresh path")
	assert.NotContains(t, out, "samplers will use server defaults", "loud-warning phrasing must not leak into the soft note")
}

// TestSamplingProfileNudge_HardMessageForExplicitProviders covers the other
// branch: providers without ImplicitGenerationConfig (omlx, lmstudio, luce)
// get the loud warning because their server fallback is decode-greedy.
func TestSamplingProfileNudge_HardMessageForExplicitProviders(t *testing.T) {
	for _, providerType := range []string{"omlx", "lmstudio", "luce"} {
		t.Run(providerType, func(t *testing.T) {
			samplingProfileNudgeOnce = sync.Once{}
			var buf bytes.Buffer
			prevSink := samplingNudgeSink
			samplingNudgeSink = &buf
			t.Cleanup(func() { samplingNudgeSink = prevSink })

			res := sampling.Resolve(nil, "any-model", "code", nil)
			if res.MissingProfile {
				msg := samplingProfileNudgeMessage
				if agentConfig.ProviderImplicitGenerationConfig(providerType) {
					msg = samplingProfileNudgeMessageImplicit
				}
				samplingProfileNudgeOnce.Do(func() {
					_, _ = samplingNudgeSink.Write([]byte(msg + "\n"))
				})
			}
			out := buf.String()
			assert.Contains(t, out, "warning:", "non-vLLM providers get the loud 'warning:' prefix")
			assert.NotContains(t, out, "generation_config.json", "loud warning must not mention the implicit-load mechanism")
		})
	}
}

// TestSamplingProfileNudge_SilentWhenProfilePresent confirms the nudge stays
// silent for the success path: catalog declares the requested profile, so
// MissingProfile is false and the once-block never trips.
func TestSamplingProfileNudge_SilentWhenProfilePresent(t *testing.T) {
	samplingProfileNudgeOnce = sync.Once{}
	var buf bytes.Buffer
	prevSink := samplingNudgeSink
	samplingNudgeSink = &buf
	t.Cleanup(func() { samplingNudgeSink = prevSink })

	temp := 0.6
	cat := stubSamplingCatalog{
		profiles: map[string]sampling.Profile{
			"code": {Temperature: &temp},
		},
	}
	res := sampling.Resolve(cat, "any-model", "code", nil)
	if res.MissingProfile {
		samplingProfileNudgeOnce.Do(func() {
			_, _ = samplingNudgeSink.Write([]byte(samplingProfileNudgeMessage + "\n"))
		})
	}
	assert.Empty(t, buf.String(), "profile present → no nudge")
}

// stubSamplingCatalog implements sampling.CatalogLookup for tests in this
// package. Mirrors the test stub in internal/sampling/resolve_test.go but
// avoids importing test code across packages.
type stubSamplingCatalog struct {
	profiles map[string]sampling.Profile
	control  map[string]string
}

func (s stubSamplingCatalog) SamplingProfile(name string) (sampling.Profile, bool) {
	p, ok := s.profiles[name]
	return p, ok
}

func (s stubSamplingCatalog) ModelSamplingControl(modelID string) string {
	return s.control[modelID]
}
