// Package anthropic implements a forge.Provider for the Anthropic Claude API.
package anthropic

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/DocumentDrivenDX/forge"
	ant "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// Provider implements forge.Provider for the Anthropic Messages API.
type Provider struct {
	client *ant.Client
	model  string
}

// Config holds configuration for the Anthropic provider.
type Config struct {
	APIKey string
	Model  string // e.g., "claude-sonnet-4-20250514"
}

// New creates a new Anthropic provider.
func New(cfg Config) *Provider {
	opts := []option.RequestOption{}
	if cfg.APIKey != "" {
		opts = append(opts, option.WithAPIKey(cfg.APIKey))
	}
	client := ant.NewClient(opts...)
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

	// Separate system message from conversation
	var system []ant.TextBlockParam
	var convMsgs []forge.Message
	for _, m := range messages {
		if m.Role == forge.RoleSystem {
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

	var resp forge.Response
	var lastErr error

	for attempt := range 3 {
		msg, err := p.client.Messages.New(ctx, params)
		if err != nil {
			lastErr = err
			if attempt < 2 {
				select {
				case <-ctx.Done():
					return resp, fmt.Errorf("anthropic: %w", ctx.Err())
				case <-time.After(time.Duration(1<<uint(attempt)) * time.Second):
					continue
				}
			}
			return resp, fmt.Errorf("anthropic: after %d attempts: %w", attempt+1, lastErr)
		}

		resp.Model = string(msg.Model)
		resp.Usage = forge.TokenUsage{
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
				resp.ToolCalls = append(resp.ToolCalls, forge.ToolCall{
					ID:        block.ID,
					Name:      block.Name,
					Arguments: json.RawMessage(block.Input),
				})
			}
		}

		return resp, nil
	}

	return resp, fmt.Errorf("anthropic: after 3 attempts: %w", lastErr)
}

func convertMessages(msgs []forge.Message) []ant.MessageParam {
	var result []ant.MessageParam
	for _, m := range msgs {
		switch m.Role {
		case forge.RoleUser:
			result = append(result, ant.NewUserMessage(ant.NewTextBlock(m.Content)))
		case forge.RoleAssistant:
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
		case forge.RoleTool:
			result = append(result, ant.NewUserMessage(
				ant.NewToolResultBlock(m.ToolCallID, m.Content, false),
			))
		}
	}
	return result
}

func convertTools(tools []forge.ToolDef) []ant.ToolUnionParam {
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

// ChatStream implements forge.StreamingProvider for token-level streaming.
func (p *Provider) ChatStream(ctx context.Context, messages []forge.Message, tools []forge.ToolDef, opts forge.Options) (<-chan forge.StreamDelta, error) {
	model := p.model
	if opts.Model != "" {
		model = opts.Model
	}

	var system []ant.TextBlockParam
	var convMsgs []forge.Message
	for _, m := range messages {
		if m.Role == forge.RoleSystem {
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

	ch := make(chan forge.StreamDelta, 1)
	go func() {
		defer close(ch)

		// Track current tool use block being streamed
		var currentToolID string
		var currentToolName string

		for stream.Next() {
			event := stream.Current()

			switch event.Type {
			case "message_start":
				// Capture input tokens from message_start
				ch <- forge.StreamDelta{
					Model: string(event.Message.Model),
					Usage: &forge.TokenUsage{
						Input: int(event.Usage.InputTokens),
					},
				}

			case "content_block_start":
				if event.ContentBlock.Type == "tool_use" {
					currentToolID = event.ContentBlock.ID
					currentToolName = event.ContentBlock.Name
					ch <- forge.StreamDelta{
						ToolCallID:   currentToolID,
						ToolCallName: currentToolName,
					}
				}

			case "content_block_delta":
				// Text delta
				if event.Delta.Text != "" {
					ch <- forge.StreamDelta{Content: event.Delta.Text}
				}
				// Tool input JSON delta
				if event.Delta.PartialJSON != "" {
					ch <- forge.StreamDelta{
						ToolCallID:   currentToolID,
						ToolCallArgs: event.Delta.PartialJSON,
					}
				}

			case "content_block_stop":
				currentToolID = ""
				currentToolName = ""

			case "message_delta":
				delta := forge.StreamDelta{
					FinishReason: string(event.Delta.StopReason),
				}
				// Build usage with output and cache tokens
				usage := &forge.TokenUsage{}
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
				ch <- delta

			case "message_stop":
				ch <- forge.StreamDelta{Done: true}
				return
			}
		}

		// Stream ended without message_stop — check for error.
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
