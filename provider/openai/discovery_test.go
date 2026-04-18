package openai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDiscoverModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/models", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"object": "list",
			"data": []map[string]interface{}{
				{"id": "qwen3.5-27b", "object": "model"},
				{"id": "llama3.1-8b", "object": "model"},
			},
		})
	}))
	defer srv.Close()

	models, err := DiscoverModels(context.Background(), srv.URL+"/v1", "")
	require.NoError(t, err)
	assert.Equal(t, []string{"qwen3.5-27b", "llama3.1-8b"}, models)
}

func TestDiscoverModels_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer srv.Close()

	_, err := DiscoverModels(context.Background(), srv.URL+"/v1", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "401")
}

func TestRankModels(t *testing.T) {
	candidates := []string{"qwen3.5-27b", "llama3.1-8b", "deepseek-r1-distill-qwen-32b"}

	t.Run("no pattern no catalog returns original order", func(t *testing.T) {
		ranked, err := RankModels(candidates, nil, "")
		require.NoError(t, err)
		require.Len(t, ranked, 3)
		assert.Equal(t, "qwen3.5-27b", ranked[0].ID)
		assert.Equal(t, 1, ranked[0].Score)
	})

	t.Run("pattern match raises score", func(t *testing.T) {
		ranked, err := RankModels(candidates, nil, "llama")
		require.NoError(t, err)
		assert.Equal(t, "llama3.1-8b", ranked[0].ID)
		assert.Equal(t, 2, ranked[0].Score)
		assert.True(t, ranked[0].PatternMatch)
	})

	t.Run("case insensitive pattern", func(t *testing.T) {
		ranked, err := RankModels(candidates, nil, "DEEPSEEK")
		require.NoError(t, err)
		assert.Equal(t, "deepseek-r1-distill-qwen-32b", ranked[0].ID)
	})

	t.Run("catalog recognized is highest score", func(t *testing.T) {
		known := map[string]string{"deepseek-r1-distill-qwen-32b": "code-high"}
		ranked, err := RankModels(candidates, known, "")
		require.NoError(t, err)
		assert.Equal(t, "deepseek-r1-distill-qwen-32b", ranked[0].ID)
		assert.Equal(t, 3, ranked[0].Score)
		assert.Equal(t, "code-high", ranked[0].CatalogRef)
	})

	t.Run("catalog beats pattern", func(t *testing.T) {
		known := map[string]string{"llama3.1-8b": "code-economy"}
		ranked, err := RankModels(candidates, known, "qwen")
		require.NoError(t, err)
		assert.Equal(t, "llama3.1-8b", ranked[0].ID)
		assert.Equal(t, 3, ranked[0].Score)
	})

	t.Run("no match falls back to first uncategorized", func(t *testing.T) {
		ranked, err := RankModels(candidates, nil, "gpt-4o")
		require.NoError(t, err)
		assert.Equal(t, "qwen3.5-27b", ranked[0].ID)
	})

	t.Run("empty candidates", func(t *testing.T) {
		ranked, err := RankModels(nil, nil, "qwen")
		require.NoError(t, err)
		assert.Empty(t, ranked)
		assert.Equal(t, "", SelectModel(ranked))
	})

	t.Run("invalid pattern returns error", func(t *testing.T) {
		_, err := RankModels(candidates, nil, "[invalid")
		require.Error(t, err)
	})
}

func TestSelectModel(t *testing.T) {
	t.Run("returns first model ID", func(t *testing.T) {
		ranked := []ScoredModel{{ID: "qwen3.5-27b", Score: 3}, {ID: "llama3.1-8b", Score: 1}}
		assert.Equal(t, "qwen3.5-27b", SelectModel(ranked))
	})

	t.Run("empty list returns empty string", func(t *testing.T) {
		assert.Equal(t, "", SelectModel(nil))
	})
}

func TestProvider_LazyModelDiscovery(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			callCount++
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"object": "list",
				"data": []map[string]interface{}{
					{"id": "discovered-model-1", "object": "model"},
				},
			})
			return
		}
		// Reject actual chat requests in this unit test
		http.Error(w, "not a chat test", http.StatusBadRequest)
	}))
	defer srv.Close()

	p := New(Config{BaseURL: srv.URL + "/v1", APIKey: "test"})

	// First resolveModel call triggers discovery.
	m, err := p.resolveModel(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "discovered-model-1", m)
	assert.Equal(t, 1, callCount)

	// Second call should hit the cache — no additional HTTP request.
	m2, err := p.resolveModel(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "discovered-model-1", m2)
	assert.Equal(t, 1, callCount, "discovery endpoint should only be called once")
}

func TestProvider_ModelPatternFilter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"object": "list",
			"data": []map[string]interface{}{
				{"id": "llama3.1-8b"},
				{"id": "qwen3.5-27b"},
			},
		})
	}))
	defer srv.Close()

	p := New(Config{BaseURL: srv.URL + "/v1", ModelPattern: "qwen"})
	m, err := p.resolveModel(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "qwen3.5-27b", m)

	// Full list should be available after resolution.
	discovered := p.DiscoveredModels()
	require.Len(t, discovered, 2)
	assert.Equal(t, "qwen3.5-27b", discovered[0].ID) // pattern-matched, ranked first
	assert.True(t, discovered[0].PatternMatch)
}

func TestNormalizeModelID(t *testing.T) {
	t.Run("exact match", func(t *testing.T) {
		result, err := NormalizeModelID("qwen3-coder-next", []string{"qwen3-coder-next", "llama3.1-8b"})
		require.NoError(t, err)
		assert.Equal(t, "qwen3-coder-next", result)
	})

	t.Run("exact match case insensitive", func(t *testing.T) {
		result, err := NormalizeModelID("Qwen3-Coder-Next", []string{"qwen3-coder-next", "llama3.1-8b"})
		require.NoError(t, err)
		assert.Equal(t, "qwen3-coder-next", result)
	})

	t.Run("suffix match normalizes bare name to prefixed", func(t *testing.T) {
		result, err := NormalizeModelID("qwen3-coder-next", []string{"qwen/qwen3-coder-next", "llama3.1-8b"})
		require.NoError(t, err)
		assert.Equal(t, "qwen/qwen3-coder-next", result)
	})

	t.Run("suffix match case insensitive", func(t *testing.T) {
		result, err := NormalizeModelID("QWEN3-CODER-NEXT", []string{"qwen/qwen3-coder-next"})
		require.NoError(t, err)
		assert.Equal(t, "qwen/qwen3-coder-next", result)
	})

	t.Run("ambiguous suffix match returns error", func(t *testing.T) {
		_, err := NormalizeModelID("foo", []string{"org1/foo", "org2/foo"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "ambiguous")
		assert.Contains(t, err.Error(), "org1/foo")
		assert.Contains(t, err.Error(), "org2/foo")
	})

	t.Run("no match returns original", func(t *testing.T) {
		result, err := NormalizeModelID("nonexistent", []string{"qwen/qwen3-coder-next", "llama3.1-8b"})
		require.NoError(t, err)
		assert.Equal(t, "nonexistent", result)
	})

	t.Run("empty requested returns empty", func(t *testing.T) {
		result, err := NormalizeModelID("", []string{"qwen/qwen3-coder-next"})
		require.NoError(t, err)
		assert.Equal(t, "", result)
	})

	t.Run("empty catalog returns original", func(t *testing.T) {
		result, err := NormalizeModelID("foo", nil)
		require.NoError(t, err)
		assert.Equal(t, "foo", result)
	})
}

func TestProvider_KnownModelsCatalogRank(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"object": "list",
			"data": []map[string]interface{}{
				{"id": "llama3.1-8b"},
				{"id": "gpt-4o"},
				{"id": "qwen3.5-27b"},
			},
		})
	}))
	defer srv.Close()

	// Simulate catalog recognizing gpt-4o.
	known := map[string]string{"gpt-4o": "code-high"}
	p := New(Config{BaseURL: srv.URL + "/v1", KnownModels: known})
	m, err := p.resolveModel(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "gpt-4o", m) // catalog-recognized should be selected

	discovered := p.DiscoveredModels()
	require.Len(t, discovered, 3)
	assert.Equal(t, "gpt-4o", discovered[0].ID)
	assert.Equal(t, "code-high", discovered[0].CatalogRef)
	assert.Equal(t, 3, discovered[0].Score)
}

// lmStudioServer returns an httptest server that serves /api/v0/models/{model}
// with the given loaded and max context lengths.
func lmStudioServer(loaded, max int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/v0/models/") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"id":                    strings.TrimPrefix(r.URL.Path, "/api/v0/models/"),
			"loaded_context_length": loaded,
			"max_context_length":    max,
		})
	}))
}

// omlxServer returns an httptest server that serves /v1/models/status with
// a single model entry.
func omlxServer(modelID string, maxContext, maxTokens int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models/status" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"models": []map[string]interface{}{
				{
					"id":                 modelID,
					"max_context_window": maxContext,
					"max_tokens":         maxTokens,
				},
			},
		})
	}))
}

func TestLookupModelLimits_ExplicitFlavorLMStudio(t *testing.T) {
	srv := lmStudioServer(100_000, 131_072)
	defer srv.Close()

	limits := LookupModelLimits(context.Background(), srv.URL+"/v1", "", "lmstudio", nil, "qwen3.5-27b")
	// Prefer loaded_context_length over max_context_length.
	assert.Equal(t, 100_000, limits.ContextLength)
	assert.Equal(t, 0, limits.MaxCompletionTokens)
}

func TestLookupModelLimits_LMStudioFallsBackToMaxWhenLoadedZero(t *testing.T) {
	srv := lmStudioServer(0, 131_072)
	defer srv.Close()

	limits := LookupModelLimits(context.Background(), srv.URL+"/v1", "", "lmstudio", nil, "qwen3.5-27b")
	assert.Equal(t, 131_072, limits.ContextLength)
}

func TestLookupModelLimits_ExplicitFlavorOmlx(t *testing.T) {
	srv := omlxServer("Qwen3.5-27B-4bit", 262_144, 32_768)
	defer srv.Close()

	limits := LookupModelLimits(context.Background(), srv.URL+"/v1", "", "omlx", nil, "Qwen3.5-27B-4bit")
	assert.Equal(t, 262_144, limits.ContextLength)
	assert.Equal(t, 32_768, limits.MaxCompletionTokens)
}

func TestLookupModelLimits_OmlxModelMatchIsCaseInsensitive(t *testing.T) {
	srv := omlxServer("Qwen3.5-27B-4bit", 262_144, 32_768)
	defer srv.Close()

	// Requested model differs only in case.
	limits := LookupModelLimits(context.Background(), srv.URL+"/v1", "", "omlx", nil, "qwen3.5-27b-4bit")
	assert.Equal(t, 262_144, limits.ContextLength)
}

func TestLookupModelLimits_OmlxUnknownModelReturnsZero(t *testing.T) {
	srv := omlxServer("foo-model", 262_144, 32_768)
	defer srv.Close()

	limits := LookupModelLimits(context.Background(), srv.URL+"/v1", "", "omlx", nil, "bar-model")
	assert.Zero(t, limits.ContextLength)
	assert.Zero(t, limits.MaxCompletionTokens)
}

func TestLookupModelLimits_UnreachableReturnsZeroWithoutError(t *testing.T) {
	// Port 1 is reserved and will refuse connections immediately.
	limits := LookupModelLimits(context.Background(), "http://127.0.0.1:1/v1", "", "lmstudio", nil, "qwen3.5-27b")
	assert.Zero(t, limits.ContextLength)
	assert.Zero(t, limits.MaxCompletionTokens)
}

func TestLookupModelLimits_UnknownFlavorReturnsZero(t *testing.T) {
	limits := LookupModelLimits(context.Background(), "http://127.0.0.1:1/v1", "", "ollama", nil, "llama3")
	assert.Zero(t, limits.ContextLength)
}

func TestLookupModelLimits_ProbeDetectsOmlx(t *testing.T) {
	// omlx server — probe should detect it by /v1/models/status responding.
	srv := omlxServer("Qwen3.5-27B-4bit", 262_144, 32_768)
	defer srv.Close()

	// No flavor hint and URL looks "local" (httptest.Server URL is 127.0.0.1).
	limits := LookupModelLimits(context.Background(), srv.URL+"/v1", "", "", nil, "Qwen3.5-27B-4bit")
	assert.Equal(t, 262_144, limits.ContextLength)
	assert.Equal(t, 32_768, limits.MaxCompletionTokens)
}

func TestLookupModelLimits_ProbeDetectsLMStudio(t *testing.T) {
	// LM Studio server — probe should fall through to lmstudio detection because
	// /v1/models/status returns 404 but /api/v0/models does not.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/v0/models/qwen3.5-27b"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"id":                    "qwen3.5-27b",
				"loaded_context_length": 100_000,
				"max_context_length":    131_072,
			})
		case r.URL.Path == "/api/v0/models":
			// LM Studio probe endpoint — must return a `data` array for the
			// probe to identify the server as LM Studio.
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"data": []map[string]interface{}{{"id": "qwen3.5-27b"}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	limits := LookupModelLimits(context.Background(), srv.URL+"/v1", "", "", nil, "qwen3.5-27b")
	assert.Equal(t, 100_000, limits.ContextLength)
}

func TestProbeProviderFlavor_DetectsOmlx(t *testing.T) {
	srv := omlxServer("any-model", 0, 0)
	defer srv.Close()

	flavor := probeProviderFlavor(context.Background(), srv.URL+"/v1")
	assert.Equal(t, "omlx", flavor)
}

func TestProbeProviderFlavor_DetectsLMStudio(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v0/models" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"data": []map[string]interface{}{{"id": "qwen3.5-27b"}},
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	flavor := probeProviderFlavor(context.Background(), srv.URL+"/v1")
	assert.Equal(t, "lmstudio", flavor)
}

func TestProbeProviderFlavor_ReturnsEmptyWhenNoServerResponds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	flavor := probeProviderFlavor(context.Background(), srv.URL+"/v1")
	assert.Equal(t, "", flavor)
}

func TestResolveProviderFlavor_ExplicitFlavorBypassesProbe(t *testing.T) {
	// Server returns 404 for everything — if the probe ran it would return "".
	// Explicit flavor must bypass the probe and return the flavor verbatim.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	assert.Equal(t, "omlx", resolveProviderFlavor(context.Background(), srv.URL+"/v1", "omlx"))
	assert.Equal(t, "lmstudio", resolveProviderFlavor(context.Background(), srv.URL+"/v1", "LMStudio"))
}

func TestDetectedFlavor_ExplicitConfigFlavorWins(t *testing.T) {
	// Server advertises omlx (probe would return "omlx") but Config.Flavor is
	// set to "lmstudio" — caller-set wins per agent-92f0f324 AC item 3.
	srv := omlxServer("any", 0, 0)
	defer srv.Close()

	p := New(Config{BaseURL: srv.URL + "/v1", Flavor: "lmstudio"})
	assert.Equal(t, "lmstudio", p.DetectedFlavor())
}

func TestDetectedFlavor_ProbeDetectsOmlx(t *testing.T) {
	// No Config.Flavor — DetectedFlavor() must probe and return "omlx".
	srv := omlxServer("any", 0, 0)
	defer srv.Close()

	p := New(Config{BaseURL: srv.URL + "/v1"})
	assert.Equal(t, "omlx", p.DetectedFlavor())
}

func TestDetectedFlavor_FallsBackToProviderSystemWhenProbeInconclusive(t *testing.T) {
	// Server returns 404 to both probes. Without Config.Flavor set,
	// DetectedFlavor() must fall back to the URL-heuristic providerSystem.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	p := New(Config{BaseURL: srv.URL + "/v1"})
	// httptest.Server gives a 127.0.0.1 host, so openAIIdentity will return
	// "local" (no well-known port) for this URL. DetectedFlavor must not
	// return empty; the providerSystem fallback guarantees a non-empty string.
	assert.NotEmpty(t, p.DetectedFlavor())
}

func TestDetectedFlavor_CachesResult(t *testing.T) {
	// Count probe hits. DetectedFlavor() must probe only once per Provider.
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models/status" {
			hits++
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"models":[]}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	p := New(Config{BaseURL: srv.URL + "/v1"})
	_ = p.DetectedFlavor()
	_ = p.DetectedFlavor()
	_ = p.DetectedFlavor()
	assert.Equal(t, 1, hits, "DetectedFlavor probe must fire only once per Provider")
}
