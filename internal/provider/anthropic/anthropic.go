// Package anthropic implements a agent.Provider for the Anthropic Claude API.
package anthropic

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/DocumentDrivenDX/agent"
	ant "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// Provider implements agent.Provider for the Anthropic Messages API.
type Provider struct {
	client         *ant.Client
	model          string
	providerName   string
	providerSystem string
	serverAddress  string
	serverPort     int
}

// Config holds configuration for the Anthropic provider.
type Config struct {
	APIKey  string
	Model   string // e.g., "claude-sonnet-4-20250514"
	BaseURL string
}

// New creates a new Anthropic provider.
func New(cfg Config) *Provider {
	opts := []option.RequestOption{option.WithMaxRetries(0)}
	if cfg.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(cfg.BaseURL))
	}
	if cfg.APIKey != "" {
		opts = append(opts, option.WithAPIKey(cfg.APIKey))
	}
	client := ant.NewClient(opts...)
	serverAddress, serverPort := anthropicIdentity(cfg.BaseURL)
	return &Provider{
		client:         &client,
		model:          cfg.Model,
		providerName:   "anthropic",
		providerSystem: "anthropic",
		serverAddress:  serverAddress,
		serverPort:     serverPort,
	}
}

func (p *Provider) Chat(ctx context.Context, messages []agent.Message, tools []agent.ToolDef, opts agent.Options) (agent.Response, error) {
	model := p.model
	if opts.Model != "" {
		model = opts.Model
	}

	// Separate system message from conversation
	var system []ant.TextBlockParam
	var convMsgs []agent.Message
	for _, m := range messages {
		if m.Role == agent.RoleSystem {
			system = append(system, ant.TextBlockParam{Text: m.Content})
		} else {
			convMsgs = append(convMsgs, m)
		}
	}

	params := ant.MessageNewParams{
		Model:    ant.Model(model),
		Messages: convertMessages(convMsgs),
	}

	if len(system) > 0 {
		params.System = system
	}

	maxTokens := 4096
	if opts.MaxTokens > 0 {
		maxTokens = opts.MaxTokens
	}
	params.MaxTokens = int64(maxTokens)

	if opts.Temperature != nil {
		params.Temperature = ant.Float(*opts.Temperature)
	}

	if len(tools) > 0 {
		params.Tools = convertTools(tools)
	}

	var resp agent.Response
	msg, err := p.client.Messages.New(ctx, params)
	if err != nil {
		return resp, fmt.Errorf("anthropic: %w", err)
	}

	resp.Model = string(msg.Model)
	resp.Attempt = &agent.AttemptMetadata{
		ProviderName:   p.providerName,
		ProviderSystem: p.providerSystem,
		ServerAddress:  p.serverAddress,
		ServerPort:     p.serverPort,
		RequestedModel: model,
		ResponseModel:  string(msg.Model),
		ResolvedModel:  string(msg.Model),
		Cost: &agent.CostAttribution{
			Source: agent.CostSourceUnknown,
		},
	}
	resp.Usage = agent.TokenUsage{
		Input:  int(msg.Usage.InputTokens),
		Output: int(msg.Usage.OutputTokens),
		Total:  int(msg.Usage.InputTokens + msg.Usage.OutputTokens),
	}

	// Extract cache tokens if present
	if msg.Usage.CacheCreationInputTokens > 0 {
		resp.Usage.CacheWrite = int(msg.Usage.CacheCreationInputTokens)
	}
	if msg.Usage.CacheReadInputTokens > 0 {
		resp.Usage.CacheRead = int(msg.Usage.CacheReadInputTokens)
	}

	resp.FinishReason = string(msg.StopReason)

	// Extract content and tool calls from content blocks
	for _, block := range msg.Content {
		switch block.Type {
		case "text":
			resp.Content += block.Text
		case "tool_use":
			resp.ToolCalls = append(resp.ToolCalls, agent.ToolCall{
				ID:        block.ID,
				Name:      block.Name,
				Arguments: json.RawMessage(block.Input),
			})
		}
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

func convertMessages(msgs []agent.Message) []ant.MessageParam {
	var result []ant.MessageParam
	for _, m := range msgs {
		switch m.Role {
		case agent.RoleUser:
			result = append(result, ant.NewUserMessage(ant.NewTextBlock(m.Content)))
		case agent.RoleAssistant:
			if len(m.ToolCalls) > 0 {
				var blocks []ant.ContentBlockParamUnion
				if m.Content != "" {
					blocks = append(blocks, ant.NewTextBlock(m.Content))
				}
				for _, tc := range m.ToolCalls {
					var input interface{}
					_ = json.Unmarshal(tc.Arguments, &input)
					blocks = append(blocks, ant.NewToolUseBlock(tc.ID, input, tc.Name))
				}
				result = append(result, ant.NewAssistantMessage(blocks...))
			} else {
				result = append(result, ant.NewAssistantMessage(ant.NewTextBlock(m.Content)))
			}
		case agent.RoleTool:
			result = append(result, ant.NewUserMessage(
				ant.NewToolResultBlock(m.ToolCallID, m.Content, false),
			))
		}
	}
	return result
}

func convertTools(tools []agent.ToolDef) []ant.ToolUnionParam {
	var result []ant.ToolUnionParam
	for _, t := range tools {
		var schema ant.ToolInputSchemaParam
		_ = json.Unmarshal(t.Parameters, &schema)

		result = append(result, ant.ToolUnionParam{
			OfTool: &ant.ToolParam{
				Name:        t.Name,
				Description: ant.String(t.Description),
				InputSchema: schema,
			},
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

	var system []ant.TextBlockParam
	var convMsgs []agent.Message
	for _, m := range messages {
		if m.Role == agent.RoleSystem {
			system = append(system, ant.TextBlockParam{Text: m.Content})
		} else {
			convMsgs = append(convMsgs, m)
		}
	}

	params := ant.MessageNewParams{
		Model:    ant.Model(model),
		Messages: convertMessages(convMsgs),
	}
	if len(system) > 0 {
		params.System = system
	}
	maxTokens := 4096
	if opts.MaxTokens > 0 {
		maxTokens = opts.MaxTokens
	}
	params.MaxTokens = int64(maxTokens)
	if opts.Temperature != nil {
		params.Temperature = ant.Float(*opts.Temperature)
	}
	if len(tools) > 0 {
		params.Tools = convertTools(tools)
	}

	stream := p.client.Messages.NewStreaming(ctx, params)

	ch := make(chan agent.StreamDelta, 1)
	go func() {
		defer close(ch)
		send := func(delta agent.StreamDelta) {
			delta.ArrivedAt = time.Now()
			ch <- delta
		}

		// Track current tool use block being streamed
		var currentToolID string
		var currentToolName string
		responseModel := model

		for stream.Next() {
			event := stream.Current()

			switch event.Type {
			case "message_start":
				// Capture input tokens from message_start
				if event.Message.Model != "" {
					responseModel = string(event.Message.Model)
				}
				send(agent.StreamDelta{
					Model: responseModel,
					Usage: &agent.TokenUsage{
						Input: int(event.Usage.InputTokens),
					},
				})

			case "content_block_start":
				if event.ContentBlock.Type == "tool_use" {
					currentToolID = event.ContentBlock.ID
					currentToolName = event.ContentBlock.Name
					send(agent.StreamDelta{
						ToolCallID:   currentToolID,
						ToolCallName: currentToolName,
					})
				}

			case "content_block_delta":
				// Text delta
				if event.Delta.Text != "" {
					send(agent.StreamDelta{Content: event.Delta.Text})
				}
				// Tool input JSON delta
				if event.Delta.PartialJSON != "" {
					send(agent.StreamDelta{
						ToolCallID:   currentToolID,
						ToolCallArgs: event.Delta.PartialJSON,
					})
				}

			case "content_block_stop":
				currentToolID = ""
				currentToolName = ""

			case "message_delta":
				delta := agent.StreamDelta{
					FinishReason: string(event.Delta.StopReason),
				}
				// Build usage with output and cache tokens
				usage := &agent.TokenUsage{}
				if event.Usage.OutputTokens > 0 {
					usage.Output = int(event.Usage.OutputTokens)
				}
				if event.Usage.CacheCreationInputTokens > 0 {
					usage.CacheWrite = int(event.Usage.CacheCreationInputTokens)
				}
				if event.Usage.CacheReadInputTokens > 0 {
					usage.CacheRead = int(event.Usage.CacheReadInputTokens)
				}
				if usage.Input > 0 || usage.Output > 0 || usage.CacheRead > 0 || usage.CacheWrite > 0 {
					delta.Usage = usage
				}
				send(delta)

			case "message_stop":
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
						Cost: &agent.CostAttribution{
							Source: agent.CostSourceUnknown,
						},
					},
					Done: true,
				})
				return
			}
		}

		// Stream ended without message_stop — check for error.
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
				Cost: &agent.CostAttribution{
					Source: agent.CostSourceUnknown,
				},
			},
			Done: true,
		})
	}()

	return ch, nil
}

var _ agent.Provider = (*Provider)(nil)
var _ agent.StreamingProvider = (*Provider)(nil)

func anthropicIdentity(baseURL string) (serverAddress string, serverPort int) {
	serverAddress = "api.anthropic.com"
	serverPort = 443

	if baseURL == "" {
		return serverAddress, serverPort
	}

	parsed, err := url.Parse(baseURL)
	if err != nil {
		return serverAddress, serverPort
	}

	if host := parsed.Hostname(); host != "" {
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
