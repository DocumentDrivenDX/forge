package omlx

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/DocumentDrivenDX/agent/internal/provider/limits"
	"github.com/DocumentDrivenDX/agent/internal/provider/openai"
	"github.com/DocumentDrivenDX/agent/internal/reasoning"
)

const DefaultBaseURL = "http://localhost:1235/v1"

var ProtocolCapabilities = openai.ProtocolCapabilities{
	Tools:            true,
	Stream:           true,
	StructuredOutput: true,
	Thinking:         true,
	ThinkingFormat:   openai.ThinkingWireFormatQwen,
	// OMLX serves Qwen MLX variants exclusively, so an explicit reasoning
	// request against a non-Qwen model is a configuration bug worth failing
	// the request for. LM Studio hosts mixed families and keeps this false.
	StrictThinkingModelMatch: true,
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
		ProviderName:   "omlx",
		ProviderSystem: "omlx",
		ModelPattern:   cfg.ModelPattern,
		KnownModels:    cfg.KnownModels,
		Headers:        cfg.Headers,
		Reasoning:      cfg.Reasoning,
		Capabilities:   &ProtocolCapabilities,
	})
}

func LookupModelLimits(ctx context.Context, baseURL, model string) limits.ModelLimits {
	base := strings.TrimRight(baseURL, "/")
	endpoint := base + "/models/status"

	var status struct {
		Models []struct {
			ID               string `json:"id"`
			MaxContextWindow int    `json:"max_context_window"`
			MaxTokens        int    `json:"max_tokens"`
		} `json:"models"`
	}
	if err := getAndDecode(ctx, 5*time.Second, endpoint, &status); err != nil {
		return limits.ModelLimits{}
	}

	for _, entry := range status.Models {
		if strings.EqualFold(entry.ID, model) {
			return limits.ModelLimits{
				ContextLength:       entry.MaxContextWindow,
				MaxCompletionTokens: entry.MaxTokens,
			}
		}
	}
	return limits.ModelLimits{}
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
