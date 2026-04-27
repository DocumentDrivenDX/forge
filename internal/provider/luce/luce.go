// Package luce wraps the OpenAI-compat shape exposed by the lucebox-hub
// dflash server (https://github.com/Luce-Org/lucebox-hub, dflash/scripts/server.py).
// Despite the OpenAI-shaped wire, the server is intentionally narrow:
//
//   - One model per server. The server reports a single model id
//     ("luce-dflash") and ignores the request's `model` field.
//   - No tool calling. The request schema has no `tools` field. We declare
//     Tools=false on the protocol capabilities so the router does not send
//     tool-bearing prompts here.
//   - Greedy decoding only. `temperature` and `top_p` are accepted on the
//     wire but the server source comments them as "noted + ignored
//     (greedy-only)". `top_k` / `min_p` / `repetition_penalty` / `seed` are
//     dropped silently. Catalog entries for luce-served models should set
//     sampling_control: harness_pinned so internal/sampling.Resolve
//     short-circuits — emitting fields here would only mislead telemetry.
//   - No reasoning toggle. No `enable_thinking` / `reasoning_effort`. The
//     greedy-only constraint plus no thinking-mode exit means the Qwen3.x
//     "DO NOT use greedy decoding" warning does not apply (the server
//     doesn't run thinking mode), but the model is also not steerable.
//
// Default port 1236 mirrors the upstream server.py default of :8000 only
// loosely — luce instances on this LAN are deployed on :1236 alongside the
// existing :1234 (lmstudio) and :1235 (omlx) endpoints.
package luce

import (
	"github.com/DocumentDrivenDX/agent/internal/provider/openai"
	"github.com/DocumentDrivenDX/agent/internal/reasoning"
)

const DefaultBaseURL = "http://localhost:1236/v1"

// ProtocolCapabilities reflects the dflash server's narrow surface.
// Tools=false is the load-bearing flag — the router uses it to filter
// luce out of candidate sets for tool-bearing requests.
var ProtocolCapabilities = openai.ProtocolCapabilities{
	Tools:            false,
	Stream:           true,
	StructuredOutput: false,
	Thinking:         false,
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
		ProviderName:   "luce",
		ProviderSystem: "luce",
		ModelPattern:   cfg.ModelPattern,
		KnownModels:    cfg.KnownModels,
		Headers:        cfg.Headers,
		Reasoning:      cfg.Reasoning,
		Capabilities:   &ProtocolCapabilities,
	})
}
