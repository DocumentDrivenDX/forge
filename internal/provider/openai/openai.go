// Package openai implements a agent.Provider for any OpenAI-compatible API
// endpoint (LM Studio, Ollama, OpenAI, Azure, Groq, Together, OpenRouter).
package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/DocumentDrivenDX/agent"
	oai "github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/packages/param"
)

// Provider implements agent.Provider for OpenAI-compatible APIs.
type Provider struct {
	client         *oai.Client
	model          string
	modelPattern   string            // regex filter for auto-discovery; "" means first model
	knownModels    map[string]string // catalog-recognized model IDs (modelID → catalogRef)
	baseURL        string            // stored for lazy model discovery
	apiKey         string            // stored for lazy model discovery
	providerName   string
	providerSystem string // URL-heuristic tag; eager, zero-cost, used in hot telemetry paths
	configFlavor   string // explicit Config.Flavor when set; empty means auto-detect via probe
	serverAddress  string
	serverPort     int
	thinkingBudget int

	// lazy model discovery — runs at most once per Provider instance
	discoverOnce     sync.Once
	discoverErr      error
	discoveredModels []ScoredModel // full ranked list; populated on first use when model == ""

	// lazy flavor detection — probe runs at most once per Provider instance
	flavorOnce     sync.Once
	detectedFlavor string
}

// Config holds configuration for the OpenAI-compatible provider.
type Config struct {
	BaseURL      string // e.g., "http://localhost:1234/v1" for LM Studio
	APIKey       string // optional for local providers
	Model        string // e.g., "qwen3.5-7b", "gpt-4o". Empty = auto-discover.
	ModelPattern string // case-insensitive regex to prefer among auto-discovered models
	// KnownModels maps concrete model IDs to catalog target IDs for the
	// agent.openai surface. Models present in this map are ranked higher during
	// auto-selection. Populated by the config layer from the model catalog;
	// nil disables catalog-aware ranking.
	KnownModels    map[string]string
	Headers        map[string]string // extra HTTP headers (OpenRouter, Azure, etc.)
	ThinkingBudget int               // max reasoning tokens for thinking models (0 = unset)
	// Flavor is an optional explicit server-type hint ("lmstudio", "omlx",
	// "openrouter", "ollama"). When set, DetectedFlavor() returns this value
	// without probing. When empty, DetectedFlavor() runs a one-time probe.
	Flavor string
}

// New creates a new OpenAI-compatible provider.
func New(cfg Config) *Provider {
	opts := []option.RequestOption{
		option.WithBaseURL(cfg.BaseURL),
		option.WithMaxRetries(0),
	}
	if cfg.APIKey != "" {
		opts = append(opts, option.WithAPIKey(cfg.APIKey))
	} else {
		opts = append(opts, option.WithAPIKey("not-needed"))
	}
	for k, v := range cfg.Headers {
		opts = append(opts, option.WithHeader(k, v))
	}
	// SSE comment-frame filter must sit before the debug sink so the sink
	// observes the same byte stream the decoder will see. Middlewares are
	// applied in registration order, outermost first.
	opts = append(opts, option.WithMiddleware(sseFilterMiddleware()))
	if s := resolveDebugSink(); s != nil {
		opts = append(opts, option.WithMiddleware(debugMiddleware(s)))
	}

	client := oai.NewClient(opts...)
	providerSystem, serverAddress, serverPort := openAIIdentity(cfg.BaseURL)
	return &Provider{
		client:         &client,
		model:          cfg.Model,
		modelPattern:   cfg.ModelPattern,
		knownModels:    cfg.KnownModels,
		baseURL:        cfg.BaseURL,
		apiKey:         cfg.APIKey,
		providerName:   "openai-compat",
		providerSystem: providerSystem,
		configFlavor:   cfg.Flavor,
		serverAddress:  serverAddress,
		serverPort:     serverPort,
		thinkingBudget: cfg.ThinkingBudget,
	}
}

// DetectedFlavor returns the effective server flavor for this provider.
// Resolution order:
//
//  1. Config.Flavor (if set at construction) — returned verbatim, no probe.
//  2. Cached probe result — computed on first call by contacting
//     /v1/models/status (omlx) and /api/v0/models (lmstudio).
//  3. URL-heuristic providerSystem — fallback when probe is inconclusive.
//
// This accessor is intended for pre-dispatch gating (capability introspection,
// routing decisions) where the caller is willing to block once on a short
// network probe. Do not use it in per-response hot paths; use
// ChatStartMetadata() for telemetry, which is eager and non-blocking.
func (p *Provider) DetectedFlavor() string {
	p.flavorOnce.Do(func() {
		if p.configFlavor != "" {
			p.detectedFlavor = strings.ToLower(strings.TrimSpace(p.configFlavor))
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		p.detectedFlavor = resolveProviderFlavor(ctx, p.baseURL, "")
	})
	if p.detectedFlavor != "" {
		return p.detectedFlavor
	}
	return p.providerSystem
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

	params := oai.ChatCompletionNewParams{
		Model:    model,
		Messages: convertMessages(messages),
	}

	if len(tools) > 0 {
		params.Tools = convertTools(tools)
	}
	if opts.MaxTokens > 0 {
		params.MaxTokens = oai.Int(int64(opts.MaxTokens))
	}
	if opts.Temperature != nil {
		params.Temperature = oai.Float(*opts.Temperature)
	}
	if len(opts.Stop) > 0 {
		params.Stop = oai.ChatCompletionNewParamsStopUnion{OfStringArray: opts.Stop}
	}

	var resp agent.Response
	completion, err := p.client.Chat.Completions.New(ctx, params)
	if err != nil {
		return resp, fmt.Errorf("openai: %w", err)
	}

	resp.Model = completion.Model
	resp.Attempt = &agent.AttemptMetadata{
		ProviderName:   p.providerName,
		ProviderSystem: p.providerSystem,
		ServerAddress:  p.serverAddress,
		ServerPort:     p.serverPort,
		RequestedModel: model,
		ResponseModel:  completion.Model,
		ResolvedModel:  completion.Model,
		Cost: &agent.CostAttribution{
			Source: agent.CostSourceUnknown,
		},
	}
	if cost, ok := openRouterCostAttribution(p.providerSystem, completion.Usage.RawJSON()); ok {
		resp.Attempt.Cost = cost
	}
	if completion.Usage.TotalTokens != 0 {
		resp.Usage = agent.TokenUsage{
			Input:  int(completion.Usage.PromptTokens),
			Output: int(completion.Usage.CompletionTokens),
			Total:  int(completion.Usage.TotalTokens),
		}
		// Extract cached tokens if present
		if completion.Usage.PromptTokensDetails.CachedTokens > 0 {
			resp.Usage.CacheRead = int(completion.Usage.PromptTokensDetails.CachedTokens)
		}
	}

	if len(completion.Choices) > 0 {
		choice := completion.Choices[0]
		resp.Content = choice.Message.Content
		resp.FinishReason = string(choice.FinishReason)
		resp.ToolCalls = extractToolCalls(choice.Message.ToolCalls)
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

func convertMessages(msgs []agent.Message) []oai.ChatCompletionMessageParamUnion {
	var result []oai.ChatCompletionMessageParamUnion
	for _, m := range msgs {
		switch m.Role {
		case agent.RoleSystem:
			result = append(result, oai.SystemMessage(m.Content))
		case agent.RoleUser:
			result = append(result, oai.UserMessage(m.Content))
		case agent.RoleAssistant:
			if len(m.ToolCalls) > 0 {
				var toolCalls []oai.ChatCompletionMessageToolCallParam
				for _, tc := range m.ToolCalls {
					toolCalls = append(toolCalls, oai.ChatCompletionMessageToolCallParam{
						ID: tc.ID,
						Function: oai.ChatCompletionMessageToolCallFunctionParam{
							Name:      tc.Name,
							Arguments: string(tc.Arguments),
						},
					})
				}
				assistant := oai.ChatCompletionAssistantMessageParam{
					Content: oai.ChatCompletionAssistantMessageParamContentUnion{
						OfString: param.NewOpt(m.Content),
					},
					ToolCalls: toolCalls,
				}
				result = append(result, oai.ChatCompletionMessageParamUnion{OfAssistant: &assistant})
			} else {
				result = append(result, oai.AssistantMessage(m.Content))
			}
		case agent.RoleTool:
			result = append(result, oai.ToolMessage(m.Content, m.ToolCallID))
		}
	}
	return result
}

func convertTools(tools []agent.ToolDef) []oai.ChatCompletionToolParam {
	var result []oai.ChatCompletionToolParam
	for _, t := range tools {
		var params map[string]interface{}
		_ = json.Unmarshal(t.Parameters, &params)

		result = append(result, oai.ChatCompletionToolParam{
			Function: oai.FunctionDefinitionParam{
				Name:        t.Name,
				Description: oai.String(t.Description),
				Parameters:  oai.FunctionParameters(params),
			},
		})
	}
	return result
}

func extractToolCalls(calls []oai.ChatCompletionMessageToolCall) []agent.ToolCall {
	if len(calls) == 0 {
		return nil
	}
	var result []agent.ToolCall
	for _, c := range calls {
		result = append(result, agent.ToolCall{
			ID:        c.ID,
			Name:      c.Function.Name,
			Arguments: json.RawMessage(c.Function.Arguments),
		})
	}
	return result
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

	params := oai.ChatCompletionNewParams{
		Model:    model,
		Messages: convertMessages(messages),
		StreamOptions: oai.ChatCompletionStreamOptionsParam{
			IncludeUsage: oai.Bool(true),
		},
	}
	if len(tools) > 0 {
		params.Tools = convertTools(tools)
	}
	if opts.MaxTokens > 0 {
		params.MaxTokens = oai.Int(int64(opts.MaxTokens))
	}
	if opts.Temperature != nil {
		params.Temperature = oai.Float(*opts.Temperature)
	}

	// Build per-request options. For thinking models (Qwen3, DeepSeek-R1 etc.)
	// apply a budget cap via the non-standard `thinking` body field. Only
	// include it for flavors that actually tolerate it — sending it to omlx
	// causes silent SSE termination after the first delta (agent-04639431
	// wire evidence from DocumentDrivenDX/ddx ddx-6a5dfe35). Other flavors
	// either ignore it (OpenAI, Ollama) or pass it through to backends that
	// don't know it (OpenRouter). Gate on p.SupportsThinking() which reads
	// the flavor-keyed capability table in protocol_support.go.
	thinkingBudget := p.thinkingBudget
	if opts.ThinkingBudget > 0 {
		thinkingBudget = opts.ThinkingBudget
	}
	if thinkingBudget == 0 && opts.ThinkingLevel != "" {
		thinkingBudget = agent.ResolveThinkingBudget(opts.ThinkingLevel)
	}
	var streamReqOpts []option.RequestOption
	if thinkingBudget > 0 && p.SupportsThinking() {
		streamReqOpts = append(streamReqOpts, option.WithJSONSet("thinking", map[string]interface{}{
			"type":          "enabled",
			"budget_tokens": thinkingBudget,
		}))
	}

	stream := p.client.Chat.Completions.NewStreaming(ctx, params, streamReqOpts...)

	ch := make(chan agent.StreamDelta, 1)
	go func() {
		defer close(ch)
		send := func(delta agent.StreamDelta) {
			delta.ArrivedAt = time.Now()
			ch <- delta
		}
		// OpenAI only sends tool call ID in the first chunk for each index;
		// subsequent argument chunks carry the index but have an empty ID.
		// Track index→ID so we can carry the ID forward.
		indexToID := make(map[int]string)
		responseModel := model
		var streamCost *agent.CostAttribution
		for stream.Next() {
			chunk := stream.Current()
			if chunk.Model != "" {
				responseModel = chunk.Model
			}
			if cost, ok := openRouterCostAttribution(p.providerSystem, chunk.Usage.RawJSON()); ok {
				streamCost = cost
			}

			// Extract reasoning_content from the raw chunk JSON. Models like Qwen3
			// and DeepSeek-R1 emit thinking tokens in choices[0].delta.reasoning_content,
			// which the typed SDK struct does not expose.
			var reasoningContent string
			if rawJSON := chunk.RawJSON(); rawJSON != "" {
				var raw struct {
					Choices []struct {
						Delta struct {
							ReasoningContent string `json:"reasoning_content"`
						} `json:"delta"`
					} `json:"choices"`
				}
				if err := json.Unmarshal([]byte(rawJSON), &raw); err == nil && len(raw.Choices) > 0 {
					reasoningContent = raw.Choices[0].Delta.ReasoningContent
				}
			}

			if len(chunk.Choices) > 0 {
				choice := chunk.Choices[0]

				// Emit one delta per tool call entry so multiple parallel tool
				// calls in the same chunk are not collapsed to the last one.
				for _, tc := range choice.Delta.ToolCalls {
					id := tc.ID
					if id != "" {
						indexToID[int(tc.Index)] = id
					} else {
						id = indexToID[int(tc.Index)]
					}
					send(agent.StreamDelta{
						Model:        chunk.Model,
						ToolCallID:   id,
						ToolCallName: tc.Function.Name,
						ToolCallArgs: tc.Function.Arguments,
					})
				}

				// Emit a separate delta for content / finish reason / reasoning when present.
				if choice.Delta.Content != "" || choice.FinishReason != "" || reasoningContent != "" {
					send(agent.StreamDelta{
						Model:            chunk.Model,
						Content:          choice.Delta.Content,
						ReasoningContent: reasoningContent,
						FinishReason:     string(choice.FinishReason),
					})
				} else if len(choice.Delta.ToolCalls) == 0 {
					// No content, no tool calls — still forward model/finish metadata.
					send(agent.StreamDelta{
						Model:        chunk.Model,
						FinishReason: string(choice.FinishReason),
					})
				}
			} else {
				send(agent.StreamDelta{Model: chunk.Model})
			}

			if chunk.Usage.TotalTokens != 0 {
				usage := &agent.TokenUsage{
					Input:  int(chunk.Usage.PromptTokens),
					Output: int(chunk.Usage.CompletionTokens),
					Total:  int(chunk.Usage.TotalTokens),
				}
				// Extract cached tokens if present
				if chunk.Usage.PromptTokensDetails.CachedTokens > 0 {
					usage.CacheRead = int(chunk.Usage.PromptTokensDetails.CachedTokens)
				}
				send(agent.StreamDelta{Usage: usage})
			}
		}

		if err := stream.Err(); err != nil {
			send(agent.StreamDelta{Err: err})
			return
		}

		send(agent.StreamDelta{
			Model: responseModel,
			Attempt: &agent.AttemptMetadata{
				ProviderName:   p.providerName,
				ProviderSystem: p.providerSystem,
				ServerAddress:  p.serverAddress,
				ServerPort:     p.serverPort,
				RequestedModel: model,
				ResponseModel:  responseModel,
				ResolvedModel:  responseModel,
				Cost:           streamAttemptCost(streamCost),
			},
			Done: true,
		})
	}()

	return ch, nil
}

var _ agent.Provider = (*Provider)(nil)
var _ agent.StreamingProvider = (*Provider)(nil)

func openAIIdentity(baseURL string) (providerSystem, serverAddress string, serverPort int) {
	providerSystem = "openai"
	serverAddress = "api.openai.com"
	serverPort = 443

	if baseURL == "" {
		return providerSystem, serverAddress, serverPort
	}

	parsed, err := url.Parse(baseURL)
	if err != nil {
		return providerSystem, serverAddress, serverPort
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

	switch {
	case strings.Contains(host, "openrouter.ai"):
		providerSystem = "openrouter"
	case host == "localhost" || host == "127.0.0.1":
		switch serverPort {
		case 11434:
			providerSystem = "ollama"
		case 1234:
			providerSystem = "lmstudio"
		case 1235:
			providerSystem = "omlx"
		default:
			providerSystem = "local"
		}
	case strings.Contains(host, "openai.com"):
		providerSystem = "openai"
	case strings.Contains(host, "minimaxi.chat"):
		providerSystem = "minimax"
	case strings.Contains(host, "dashscope.aliyuncs.com"):
		providerSystem = "qwen"
	case strings.Contains(host, "z.ai"):
		providerSystem = "zai"
	default:
		// Non-standard port on a named host → treat as local inference runtime.
		if serverPort != 0 && serverPort != 80 && serverPort != 443 {
			switch serverPort {
			case 11434:
				providerSystem = "ollama"
			case 1234:
				providerSystem = "lmstudio"
			case 1235:
				providerSystem = "omlx"
			default:
				providerSystem = "local"
			}
		}
		// Standard ports (0, 80, 443) on an unknown host fall through to "openai".
	}

	return providerSystem, serverAddress, serverPort
}

func streamAttemptCost(cost *agent.CostAttribution) *agent.CostAttribution {
	if cost != nil {
		return cost
	}
	return &agent.CostAttribution{Source: agent.CostSourceUnknown}
}

func openRouterCostAttribution(providerSystem, rawUsage string) (*agent.CostAttribution, bool) {
	if providerSystem != "openrouter" || strings.TrimSpace(rawUsage) == "" {
		return nil, false
	}

	// OpenRouter extends the OpenAI-compatible usage object with a billed USD
	// cost field at usage.cost. Preserve it when present instead of guessing from
	// a local pricing table.
	var usage struct {
		Cost *float64 `json:"cost"`
	}
	if err := json.Unmarshal([]byte(rawUsage), &usage); err != nil || usage.Cost == nil || *usage.Cost < 0 {
		return nil, false
	}

	return &agent.CostAttribution{
		Source:     agent.CostSourceGatewayReported,
		Currency:   "USD",
		Amount:     usage.Cost,
		PricingRef: "openrouter/usage.cost",
		Raw:        json.RawMessage(rawUsage),
	}, true
}
