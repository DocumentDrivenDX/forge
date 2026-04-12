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
	providerName   string
	providerSystem string
	serverAddress  string
	serverPort     int
	thinkingBudget int
}

// Config holds configuration for the OpenAI-compatible provider.
type Config struct {
	BaseURL        string            // e.g., "http://localhost:1234/v1" for LM Studio
	APIKey         string            // optional for local providers
	Model          string            // e.g., "qwen3.5-7b", "gpt-4o"
	Headers        map[string]string // extra HTTP headers (OpenRouter, Azure, etc.)
	ThinkingBudget int               // max reasoning tokens for thinking models (0 = unset)
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

	client := oai.NewClient(opts...)
	providerSystem, serverAddress, serverPort := openAIIdentity(cfg.BaseURL)
	return &Provider{
		client:         &client,
		model:          cfg.Model,
		providerName:   "openai-compat",
		providerSystem: providerSystem,
		serverAddress:  serverAddress,
		serverPort:     serverPort,
		thinkingBudget: cfg.ThinkingBudget,
	}
}

func (p *Provider) Chat(ctx context.Context, messages []agent.Message, tools []agent.ToolDef, opts agent.Options) (agent.Response, error) {
	model := p.model
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
	model := p.model
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
	// apply a budget cap via the non-standard `thinking` body field that
	// LM Studio and compatible servers recognise.
	thinkingBudget := p.thinkingBudget
	if opts.ThinkingBudget > 0 {
		thinkingBudget = opts.ThinkingBudget
	}
	if thinkingBudget == 0 && opts.ThinkingLevel != "" {
		thinkingBudget = agent.ResolveThinkingBudget(opts.ThinkingLevel)
	}
	var streamReqOpts []option.RequestOption
	if thinkingBudget > 0 {
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
		default:
			providerSystem = "local"
		}
	case strings.Contains(host, "openai.com"):
		providerSystem = "openai"
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
