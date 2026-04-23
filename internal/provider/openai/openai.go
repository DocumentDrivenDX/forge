// Package openai implements a agent.Provider for any OpenAI-compatible API
// endpoint (LM Studio, Ollama, OpenAI, Azure, Groq, Together, OpenRouter).
package openai

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"

	agent "github.com/DocumentDrivenDX/agent/internal/core"
	reasoningpolicy "github.com/DocumentDrivenDX/agent/internal/reasoning"
	"github.com/DocumentDrivenDX/agent/internal/sdk/openaicompat"
	"github.com/openai/openai-go/option"
)

// Provider implements agent.Provider for OpenAI-compatible APIs.
type Provider struct {
	client           *openaicompat.Client
	model            string
	modelPattern     string            // regex filter for auto-discovery; "" means first model
	knownModels      map[string]string // catalog-recognized model IDs (modelID → catalogRef)
	baseURL          string            // stored for lazy model discovery
	apiKey           string            // stored for lazy model discovery
	providerName     string
	providerSystem   string
	capabilities     *ProtocolCapabilities
	usageCost        func(rawUsage string) (*agent.CostAttribution, bool)
	serverAddress    string
	serverPort       int
	reasoningDefault reasoningpolicy.Reasoning

	// lazy model discovery — runs at most once per Provider instance
	discoverOnce     sync.Once
	discoverErr      error
	discoveredModels []ScoredModel // full ranked list; populated on first use when model == ""
}

// Config holds configuration for the OpenAI-compatible provider.
type Config struct {
	BaseURL      string // e.g., "http://localhost:1234/v1" for LM Studio
	APIKey       string // optional for local providers
	Model        string // e.g., "qwen3.5-7b", "gpt-4o". Empty = auto-discover.
	ProviderName string // logical provider identity; default "openai"
	// ProviderSystem is the telemetry/cost system identity. When empty, it
	// defaults to "openai". Concrete provider wrappers set their own type.
	ProviderSystem string
	ModelPattern   string // case-insensitive regex to prefer among auto-discovered models
	// KnownModels maps concrete model IDs to catalog target IDs for the
	// agent.openai surface. Models present in this map are ranked higher during
	// auto-selection. Populated by the config layer from the model catalog;
	// nil disables catalog-aware ranking.
	KnownModels map[string]string
	Headers     map[string]string // extra HTTP headers (OpenRouter, Azure, etc.)
	Reasoning   reasoningpolicy.Reasoning
	// Capabilities supplies provider-owned protocol capability claims. When nil,
	// direct openai.Provider callers use OpenAI protocol defaults.
	Capabilities *ProtocolCapabilities
	// UsageCostAttribution extracts provider-owned gateway cost metadata from
	// the raw usage object, when that provider reports one.
	UsageCostAttribution func(rawUsage string) (*agent.CostAttribution, bool)
}

// New creates a new OpenAI-compatible provider.
func New(cfg Config) *Provider {
	serverAddress, serverPort := endpointMetadata(cfg.BaseURL)
	providerSystem := "openai"
	if cfg.ProviderSystem != "" {
		providerSystem = cfg.ProviderSystem
	}
	providerName := cfg.ProviderName
	if providerName == "" {
		providerName = providerSystem
	}
	return &Provider{
		client: openaicompat.NewClient(openaicompat.Config{
			BaseURL: cfg.BaseURL,
			APIKey:  cfg.APIKey,
			Headers: cfg.Headers,
		}),
		model:            cfg.Model,
		modelPattern:     cfg.ModelPattern,
		knownModels:      cfg.KnownModels,
		baseURL:          cfg.BaseURL,
		apiKey:           cfg.APIKey,
		providerName:     providerName,
		providerSystem:   providerSystem,
		capabilities:     cfg.Capabilities,
		usageCost:        cfg.UsageCostAttribution,
		serverAddress:    serverAddress,
		serverPort:       serverPort,
		reasoningDefault: cfg.Reasoning,
	}
}

// DiscoveredModels returns the full ranked list of models discovered from the
// server's /v1/models endpoint. Returns nil if the provider has a statically
// configured model or if discovery has not yet run (i.e. no request has been
// made yet). Call EnsureDiscovered to force discovery without making a chat
// request.
func (p *Provider) DiscoveredModels() []ScoredModel {
	return p.discoveredModels
}

// EnsureDiscovered probes the server's /v1/models endpoint and caches the
// full ranked model list. It is a no-op when the provider has a statically
// configured model or when discovery has already run.
func (p *Provider) EnsureDiscovered(ctx context.Context) error {
	if p.model != "" {
		return nil
	}
	_, err := p.resolveModel(ctx)
	return err
}

// resolveModel returns the model to use for a request. If the provider was
// configured without a model it queries /v1/models once, ranks results, and
// caches both the full list and the selected model.
// Subsequent calls return the cached value without hitting the network.
func (p *Provider) resolveModel(ctx context.Context) (string, error) {
	if p.model != "" {
		return p.model, nil
	}
	p.discoverOnce.Do(func() {
		candidates, err := DiscoverModels(ctx, p.baseURL, p.apiKey)
		if err != nil {
			p.discoverErr = err
			return
		}
		ranked, err := RankModels(candidates, p.knownModels, p.modelPattern)
		if err != nil {
			p.discoverErr = err
			return
		}
		p.discoveredModels = ranked
		selected := SelectModel(ranked)
		if selected == "" {
			p.discoverErr = fmt.Errorf("openai: no models returned by %s/models", p.baseURL)
			return
		}
		p.model = selected
	})
	return p.model, p.discoverErr
}

func (p *Provider) Chat(ctx context.Context, messages []agent.Message, tools []agent.ToolDef, opts agent.Options) (agent.Response, error) {
	model, err := p.resolveModel(ctx)
	if err != nil {
		return agent.Response{}, err
	}
	if opts.Model != "" {
		model = opts.Model
	}

	reqOpts, err := p.compatRequestOptions(model, opts)
	if err != nil {
		return agent.Response{}, err
	}

	result, err := p.client.Chat(ctx, model, messages, tools, reqOpts)
	if err != nil {
		return agent.Response{}, fmt.Errorf("openai: %w", err)
	}

	resp := agent.Response{
		Model:        result.Model,
		Content:      result.Content,
		FinishReason: result.FinishReason,
		ToolCalls:    result.ToolCalls,
		Usage:        result.Usage,
	}
	resp.Attempt = p.attemptMetadata(model, result.Model, &agent.CostAttribution{
		Source: agent.CostSourceUnknown,
	})
	if cost := p.costAttribution(result.RawUsage); cost != nil {
		resp.Attempt.Cost = cost
	}
	return resp, nil
}

// SessionStartMetadata reports the broad provider identity and configured model
// that should be recorded on session.start events.
func (p *Provider) SessionStartMetadata() (string, string) {
	return p.providerName, p.model
}

// ChatStartMetadata reports the resolved provider system and upstream server
// identity known when the provider is constructed.
func (p *Provider) ChatStartMetadata() (string, string, int) {
	return p.providerSystem, p.serverAddress, p.serverPort
}

// ChatStream implements agent.StreamingProvider for token-level streaming.
func (p *Provider) ChatStream(ctx context.Context, messages []agent.Message, tools []agent.ToolDef, opts agent.Options) (<-chan agent.StreamDelta, error) {
	model, err := p.resolveModel(ctx)
	if err != nil {
		return nil, err
	}
	if opts.Model != "" {
		model = opts.Model
	}

	reqOpts, err := p.compatRequestOptions(model, opts)
	if err != nil {
		return nil, err
	}

	return p.client.ChatStream(ctx, model, messages, tools, reqOpts, openaicompat.StreamHooks{
		Cost: p.costAttribution,
		Attempt: func(responseModel string, cost *agent.CostAttribution) *agent.AttemptMetadata {
			return p.attemptMetadata(model, responseModel, streamAttemptCost(cost))
		},
	})
}

func (p *Provider) compatRequestOptions(model string, opts agent.Options) (openaicompat.RequestOptions, error) {
	extra, err := p.reasoningRequestOptions(model, opts)
	if err != nil {
		return openaicompat.RequestOptions{}, err
	}
	return openaicompat.RequestOptions{
		MaxTokens:    opts.MaxTokens,
		Temperature:  opts.Temperature,
		Seed:         opts.Seed,
		Stop:         opts.Stop,
		ExtraOptions: extra,
	}, nil
}

func (p *Provider) attemptMetadata(requestedModel, responseModel string, cost *agent.CostAttribution) *agent.AttemptMetadata {
	if cost == nil {
		cost = &agent.CostAttribution{Source: agent.CostSourceUnknown}
	}
	return &agent.AttemptMetadata{
		ProviderName:   p.providerName,
		ProviderSystem: p.providerSystem,
		ServerAddress:  p.serverAddress,
		ServerPort:     p.serverPort,
		RequestedModel: requestedModel,
		ResponseModel:  responseModel,
		ResolvedModel:  responseModel,
		Cost:           cost,
	}
}

func (p *Provider) costAttribution(rawUsage string) *agent.CostAttribution {
	if p.usageCost == nil {
		return nil
	}
	cost, _ := p.usageCost(rawUsage)
	return cost
}

// reasoningRequestOptions builds per-request options. For thinking models
// (Qwen3, DeepSeek-R1 etc.) apply provider-specific non-standard body fields
// only when the concrete provider declares the matching wire support.
func (p *Provider) reasoningRequestOptions(model string, opts agent.Options) ([]option.RequestOption, error) {
	policy, err := reasoningpolicy.Parse(opts.Reasoning)
	if err != nil {
		return nil, err
	}
	explicitRequest := policy.IsSet()
	if !explicitRequest {
		policy, err = reasoningpolicy.Parse(p.reasoningDefault)
		if err != nil {
			return nil, err
		}
	}

	if !policy.IsSet() || policy.Kind == reasoningpolicy.KindAuto {
		return nil, nil
	}
	if !p.SupportsThinking() {
		if policy.IsExplicitOff() {
			return nil, nil
		}
		if explicitRequest {
			return nil, fmt.Errorf("openai: reasoning=%q is not supported by provider type %q", policy.Value, p.providerSystem)
		}
		return nil, nil
	}

	if policy.IsExplicitOff() {
		switch p.thinkingWireFormat() {
		case ThinkingWireFormatQwen:
			if !isQwenModel(model) {
				if explicitRequest && p.strictThinkingModelMatch() {
					return nil, fmt.Errorf("openai: qwen reasoning control is not supported for model %q on provider type %q", model, p.providerSystem)
				}
				return nil, nil
			}
			return []option.RequestOption{
				option.WithJSONSet("enable_thinking", false),
				option.WithJSONSet("thinking_budget", 0),
			}, nil
		case ThinkingWireFormatOpenRouter:
			return []option.RequestOption{option.WithJSONSet("reasoning", map[string]interface{}{
				"effort": "none",
			})}, nil
		}
		return nil, nil
	}

	if p.thinkingWireFormat() == ThinkingWireFormatOpenRouter {
		return openRouterReasoningOptions(policy)
	}
	if p.thinkingWireFormat() == ThinkingWireFormatQwen && !isQwenModel(model) {
		if explicitRequest && p.strictThinkingModelMatch() {
			return nil, fmt.Errorf("openai: qwen reasoning control is not supported for model %q on provider type %q", model, p.providerSystem)
		}
		return nil, nil
	}

	thinkingBudget, err := reasoningpolicy.BudgetFor(policy, nil, 0)
	if err != nil {
		return nil, fmt.Errorf("openai: %w", err)
	}
	if thinkingBudget <= 0 {
		return nil, nil
	}

	switch p.thinkingWireFormat() {
	case ThinkingWireFormatQwen:
		return []option.RequestOption{
			option.WithJSONSet("enable_thinking", true),
			option.WithJSONSet("thinking_budget", thinkingBudget),
		}, nil
	case "", ThinkingWireFormatThinkingMap:
		return []option.RequestOption{option.WithJSONSet("thinking", map[string]interface{}{
			"type":          "enabled",
			"budget_tokens": thinkingBudget,
		})}, nil
	default:
		return nil, fmt.Errorf("openai: unsupported thinking wire format %q for provider type %q", p.thinkingWireFormat(), p.providerSystem)
	}
}

func isQwenModel(model string) bool {
	return strings.Contains(strings.ToLower(model), "qwen")
}

func openRouterReasoningOptions(policy reasoningpolicy.Policy) ([]option.RequestOption, error) {
	reasoning := map[string]interface{}{}
	switch policy.Kind {
	case reasoningpolicy.KindTokens:
		reasoning["max_tokens"] = policy.Tokens
	case reasoningpolicy.KindNamed:
		effort := string(policy.Value)
		if policy.Value == reasoningpolicy.ReasoningMax {
			effort = string(reasoningpolicy.ReasoningXHigh)
		}
		switch effort {
		case "minimal", "low", "medium", "high", "xhigh":
			reasoning["effort"] = effort
		default:
			return nil, fmt.Errorf("openai: unsupported OpenRouter reasoning effort %q", policy.Value)
		}
	default:
		return nil, fmt.Errorf("openai: unsupported OpenRouter reasoning policy %q", policy.Kind)
	}
	return []option.RequestOption{option.WithJSONSet("reasoning", reasoning)}, nil
}

var _ agent.Provider = (*Provider)(nil)
var _ agent.StreamingProvider = (*Provider)(nil)

func endpointMetadata(baseURL string) (serverAddress string, serverPort int) {
	serverAddress = "api.openai.com"
	serverPort = 443

	if baseURL == "" {
		return serverAddress, serverPort
	}

	parsed, err := url.Parse(baseURL)
	if err != nil {
		return serverAddress, serverPort
	}

	host := parsed.Hostname()
	if host != "" {
		serverAddress = host
	}

	if port := parsed.Port(); port != "" {
		if parsedPort, err := strconv.Atoi(port); err == nil {
			serverPort = parsedPort
		}
	} else if strings.EqualFold(parsed.Scheme, "http") {
		serverPort = 80
	}

	return serverAddress, serverPort
}

func streamAttemptCost(cost *agent.CostAttribution) *agent.CostAttribution {
	if cost != nil {
		return cost
	}
	return &agent.CostAttribution{Source: agent.CostSourceUnknown}
}
