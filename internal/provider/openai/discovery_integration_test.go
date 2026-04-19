//go:build integration

package openai

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func lmStudioURLForDiscovery(t *testing.T) string {
	t.Helper()
	if url := os.Getenv("LMSTUDIO_URL"); url != "" {
		if providerReachable(t, url) {
			return url
		}
		t.Skipf("LM Studio at %q is unreachable", url)
	}

	for _, host := range []string{"vidar:1234", "bragi:1234"} {
		url := fmt.Sprintf("http://%s/v1", host)
		if providerReachable(t, url) {
			return url
		}
	}

	t.Skip("No LM Studio instance found for discovery tests (set LMSTUDIO_URL)")
	return ""
}

func omlxURL(t *testing.T) string {
	t.Helper()
	if url := os.Getenv("OMLX_URL"); url != "" {
		if providerReachable(t, url) {
			return url
		}
		t.Skipf("oMLX at %q is unreachable", url)
	}

	for _, host := range []string{"vidar:1235", "localhost:1235"} {
		url := fmt.Sprintf("http://%s/v1", host)
		if providerReachable(t, url) {
			return url
		}
	}

	t.Skip("No oMLX instance found for discovery tests (set OMLX_URL)")
	return ""
}

func providerReachable(t *testing.T, baseURL string) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := DiscoverModels(ctx, baseURL, "")
	return err == nil
}

func firstDiscoveredModel(t *testing.T, baseURL string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ids, err := DiscoverModels(ctx, baseURL, "")
	require.NoError(t, err)
	require.NotEmpty(t, ids)
	return ids[0]
}

func TestIntegration_LookupModelLimits_LMStudio(t *testing.T) {
	baseURL := lmStudioURLForDiscovery(t)
	model := firstDiscoveredModel(t, baseURL)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	limits := LookupModelLimits(ctx, baseURL, "", "lmstudio", nil, model)
	require.Greater(t, limits.ContextLength, 0)

	root := strings.TrimSuffix(strings.TrimRight(baseURL, "/"), "/v1")
	var info struct {
		LoadedContextLength int `json:"loaded_context_length"`
		MaxContextLength    int `json:"max_context_length"`
	}
	err := getAndDecode(ctx, 5*time.Second, root+"/api/v0/models/"+url.PathEscape(model), "", nil, &info)
	require.NoError(t, err)
	require.Greater(t, info.MaxContextLength, 0)
	require.Greater(t, info.LoadedContextLength, 0)
	assert.Equal(t, info.LoadedContextLength, limits.ContextLength)
}

func TestIntegration_LookupModelLimits_Omlx(t *testing.T) {
	baseURL := omlxURL(t)
	model := firstDiscoveredModel(t, baseURL)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	limits := LookupModelLimits(ctx, baseURL, "", "omlx", nil, model)
	require.Greater(t, limits.ContextLength, 0)
	require.Greater(t, limits.MaxCompletionTokens, 0)

	var status struct {
		Models []struct {
			ID               string `json:"id"`
			MaxContextWindow int    `json:"max_context_window"`
			MaxTokens        int    `json:"max_tokens"`
		} `json:"models"`
	}
	err := getAndDecode(ctx, 5*time.Second, strings.TrimRight(baseURL, "/")+"/models/status", "", nil, &status)
	require.NoError(t, err)

	for _, entry := range status.Models {
		if strings.EqualFold(entry.ID, model) {
			assert.Equal(t, entry.MaxContextWindow, limits.ContextLength)
			assert.Equal(t, entry.MaxTokens, limits.MaxCompletionTokens)
			return
		}
	}
	t.Fatalf("model %q not found in oMLX status payload", model)
}

func TestIntegration_ProbeProviderFlavor_LMStudio(t *testing.T) {
	baseURL := lmStudioURLForDiscovery(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	assert.Equal(t, "lmstudio", probeProviderFlavor(ctx, baseURL))
}

func TestIntegration_ProbeProviderFlavor_Omlx(t *testing.T) {
	baseURL := omlxURL(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	assert.Equal(t, "omlx", probeProviderFlavor(ctx, baseURL))
}

func TestIntegration_DiscoverModels_LMStudio(t *testing.T) {
	baseURL := lmStudioURLForDiscovery(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ids, err := DiscoverModels(ctx, baseURL, "")
	require.NoError(t, err)
	assert.NotEmpty(t, ids)
}

func TestIntegration_DiscoverModels_Omlx(t *testing.T) {
	baseURL := omlxURL(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ids, err := DiscoverModels(ctx, baseURL, "")
	require.NoError(t, err)
	assert.NotEmpty(t, ids)
}
