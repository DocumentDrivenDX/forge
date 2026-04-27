// Package vllm wraps the OpenAI-compat HTTP surface exposed by `vllm serve`
// (https://docs.vllm.ai/). vLLM is a high-throughput inference server with
// one behavior worth distinguishing in the catalog stack: by default it
// applies the target model's HuggingFace `generation_config.json` when the
// client omits sampler fields. Most other local servers we wrap (omlx,
// lmstudio, luce) cannot do this — MLX / GGUF repackaging typically drops
// generation_config.json from the bundle, and the servers ship their own
// presets instead.
//
// The implication for ADR-007's catalog-stale nudge: when a vLLM-served
// request omits sampler fields, the user is not "decoding greedy" — the
// server is honoring the model creator's recommended bundle. The CLI
// reflects that with a softer message.
//
// Capabilities mirror lmstudio (Tools / Stream / StructuredOutput true) and
// add ImplicitGenerationConfig=true. Reasoning is model-dependent and not
// declared at the provider level; per-model thinking-mode controls live in
// the catalog ModelEntry, matching the lmstudio precedent.
//
// Default port 8000 follows the vLLM docs. Auth is optional: vLLM accepts
// unauthenticated requests by default and gates with --api-key (or
// VLLM_API_KEY) when the operator sets one. The Config.APIKey field flows
// through unchanged.
package vllm

import (
	"github.com/DocumentDrivenDX/agent/internal/provider/openai"
	"github.com/DocumentDrivenDX/agent/internal/reasoning"
)

const DefaultBaseURL = "http://localhost:8000/v1"

// ProtocolCapabilities mirrors lmstudio's openai-compat surface and adds
// ImplicitGenerationConfig=true so the catalog-stale nudge can soften.
var ProtocolCapabilities = openai.ProtocolCapabilities{
	Tools:                    true,
	Stream:                   true,
	StructuredOutput:         true,
	ImplicitGenerationConfig: true,
}

type Config struct {
	BaseURL      string
	APIKey       string
	Model        string
	ModelPattern string
	KnownModels  map[string]string
	Headers      map[string]string
	Reasoning    reasoning.Reasoning
}

func New(cfg Config) *openai.Provider {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return openai.New(openai.Config{
		BaseURL:        baseURL,
		APIKey:         cfg.APIKey,
		Model:          cfg.Model,
		ProviderName:   "vllm",
		ProviderSystem: "vllm",
		ModelPattern:   cfg.ModelPattern,
		KnownModels:    cfg.KnownModels,
		Headers:        cfg.Headers,
		Reasoning:      cfg.Reasoning,
		Capabilities:   &ProtocolCapabilities,
	})
}
