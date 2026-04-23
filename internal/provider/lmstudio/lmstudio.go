package lmstudio

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/DocumentDrivenDX/agent/internal/provider/limits"
	"github.com/DocumentDrivenDX/agent/internal/provider/openai"
	"github.com/DocumentDrivenDX/agent/internal/reasoning"
)

const DefaultBaseURL = "http://localhost:1234/v1"

// ProtocolCapabilities reflects what an LM Studio server exposes on the
// OpenAI-compatible surface. LM Studio most commonly serves Qwen-family
// thinking models, so reasoning controls use the Qwen wire shape
// (`enable_thinking`/`thinking_budget`). Non-Qwen models are gated in the
// openai layer so those fields are stripped before serialization.
//
// Evidence (2026-04-23) against Bragi LM Studio serving
// `qwen/qwen3.6-35b-a3b` (arch=qwen35moe, Q4_K_M) shows LM Studio accepts
// every tested wire shape (`thinking` map, Qwen controls,
// `chat_template_kwargs.enable_thinking=false`, `reasoning_effort`, and the
// `/no_think` prompt token) but does not forward them into the model's
// chat template: `reasoning_content` is emitted and `reasoning_tokens` is
// reported for all shapes. Qwen remains the correct per-family wire choice
// so requests match the model family; the inability to actually bound
// reasoning on this GGUF is a server/template blocker documented in
// scripts/beadbench/README.md and is not fixable in-provider.
var ProtocolCapabilities = openai.ProtocolCapabilities{
	Tools:            true,
	Stream:           true,
	StructuredOutput: true,
	Thinking:         true,
	ThinkingFormat:   openai.ThinkingWireFormatQwen,
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
		ProviderName:   "lmstudio",
		ProviderSystem: "lmstudio",
		ModelPattern:   cfg.ModelPattern,
		KnownModels:    cfg.KnownModels,
		Headers:        cfg.Headers,
		Reasoning:      cfg.Reasoning,
		Capabilities:   &ProtocolCapabilities,
	})
}

func LookupModelLimits(ctx context.Context, baseURL, model string) limits.ModelLimits {
	root := strings.TrimSuffix(strings.TrimRight(baseURL, "/"), "/v1")
	endpoint := root + "/api/v0/models/" + url.PathEscape(model)

	var info struct {
		LoadedContextLength int `json:"loaded_context_length"`
		MaxContextLength    int `json:"max_context_length"`
	}
	if err := getAndDecode(ctx, 5*time.Second, endpoint, &info); err != nil {
		return limits.ModelLimits{}
	}

	contextLen := info.LoadedContextLength
	if contextLen == 0 {
		contextLen = info.MaxContextLength
	}
	return limits.ModelLimits{ContextLength: contextLen}
}

func getAndDecode(ctx context.Context, timeout time.Duration, endpoint string, out any) error {
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
