// Package openai implements a forge.Provider for any OpenAI-compatible API
// endpoint (LM Studio, Ollama, OpenAI, Azure, Groq, Together, OpenRouter).
package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/DocumentDrivenDX/forge"
	oai "github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/packages/param"
)

// Provider implements forge.Provider for OpenAI-compatible APIs.
type Provider struct {
	client *oai.Client
	model  string
}

// Config holds configuration for the OpenAI-compatible provider.
type Config struct {
	BaseURL string            // e.g., "http://localhost:1234/v1" for LM Studio
	APIKey  string            // optional for local providers
	Model   string            // e.g., "qwen3.5-7b", "gpt-4o"
	Headers map[string]string // extra HTTP headers (OpenRouter, Azure, etc.)
}

// New creates a new OpenAI-compatible provider.
func New(cfg Config) *Provider {
	opts := []option.RequestOption{
		option.WithBaseURL(cfg.BaseURL),
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
	return &Provider{
		client: &client,
		model:  cfg.Model,
	}
}

func (p *Provider) Chat(ctx context.Context, messages []forge.Message, tools []forge.ToolDef, opts forge.Options) (forge.Response, error) {
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

	var resp forge.Response
	var lastErr error

	for attempt := range 3 {
		completion, err := p.client.Chat.Completions.New(ctx, params)
		if err != nil {
			lastErr = err
			if attempt < 2 {
				select {
				case <-ctx.Done():
					return resp, fmt.Errorf("openai: %w", ctx.Err())
				case <-time.After(time.Duration(1<<uint(attempt)) * time.Second):
					continue
				}
			}
			return resp, fmt.Errorf("openai: after %d attempts: %w", attempt+1, lastErr)
		}

		resp.Model = completion.Model
		if completion.Usage.TotalTokens != 0 {
			resp.Usage = forge.TokenUsage{
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

	return resp, fmt.Errorf("openai: after 3 attempts: %w", lastErr)
}

func convertMessages(msgs []forge.Message) []oai.ChatCompletionMessageParamUnion {
	var result []oai.ChatCompletionMessageParamUnion
	for _, m := range msgs {
		switch m.Role {
		case forge.RoleSystem:
			result = append(result, oai.SystemMessage(m.Content))
		case forge.RoleUser:
			result = append(result, oai.UserMessage(m.Content))
		case forge.RoleAssistant:
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
		case forge.RoleTool:
			result = append(result, oai.ToolMessage(m.Content, m.ToolCallID))
		}
	}
	return result
}

func convertTools(tools []forge.ToolDef) []oai.ChatCompletionToolParam {
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

func extractToolCalls(calls []oai.ChatCompletionMessageToolCall) []forge.ToolCall {
	if len(calls) == 0 {
		return nil
	}
	var result []forge.ToolCall
	for _, c := range calls {
		result = append(result, forge.ToolCall{
			ID:        c.ID,
			Name:      c.Function.Name,
			Arguments: json.RawMessage(c.Function.Arguments),
		})
	}
	return result
}

// ChatStream implements forge.StreamingProvider for token-level streaming.
func (p *Provider) ChatStream(ctx context.Context, messages []forge.Message, tools []forge.ToolDef, opts forge.Options) (<-chan forge.StreamDelta, error) {
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

	stream := p.client.Chat.Completions.NewStreaming(ctx, params)

	ch := make(chan forge.StreamDelta, 1)
	go func() {
		defer close(ch)
		// OpenAI only sends tool call ID in the first chunk for each index;
		// subsequent argument chunks carry the index but have an empty ID.
		// Track index→ID so we can carry the ID forward.
		indexToID := make(map[int]string)
		for stream.Next() {
			chunk := stream.Current()

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
					ch <- forge.StreamDelta{
						Model:        chunk.Model,
						ToolCallID:   id,
						ToolCallName: tc.Function.Name,
						ToolCallArgs: tc.Function.Arguments,
					}
				}

				// Emit a separate delta for content / finish reason when present.
				if choice.Delta.Content != "" || choice.FinishReason != "" {
					ch <- forge.StreamDelta{
						Model:        chunk.Model,
						Content:      choice.Delta.Content,
						FinishReason: string(choice.FinishReason),
					}
				} else if len(choice.Delta.ToolCalls) == 0 {
					// No content, no tool calls — still forward model/finish metadata.
					ch <- forge.StreamDelta{
						Model:        chunk.Model,
						FinishReason: string(choice.FinishReason),
					}
				}
			} else {
				ch <- forge.StreamDelta{Model: chunk.Model}
			}

			if chunk.Usage.TotalTokens != 0 {
				usage := &forge.TokenUsage{
					Input:  int(chunk.Usage.PromptTokens),
					Output: int(chunk.Usage.CompletionTokens),
					Total:  int(chunk.Usage.TotalTokens),
				}
				// Extract cached tokens if present
				if chunk.Usage.PromptTokensDetails.CachedTokens > 0 {
					usage.CacheRead = int(chunk.Usage.PromptTokensDetails.CachedTokens)
				}
				ch <- forge.StreamDelta{Usage: usage}
			}
		}

		if err := stream.Err(); err != nil {
			ch <- forge.StreamDelta{Err: err}
			return
		}
		ch <- forge.StreamDelta{Done: true}
	}()

	return ch, nil
}

var _ forge.Provider = (*Provider)(nil)
var _ forge.StreamingProvider = (*Provider)(nil)
