package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DocumentDrivenDX/agent/internal/harnesses"
	"github.com/DocumentDrivenDX/agent/internal/modelcatalog"
)

// fakeModelsServer returns an httptest.Server that serves the given model IDs from /v1/models.
func fakeModelsServer(models []string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" || r.URL.Path == "/models" {
			w.Header().Set("Content-Type", "application/json")
			data := make([]map[string]any, len(models))
			for i, m := range models {
				data[i] = map[string]any{"id": m}
			}
			json.NewEncoder(w).Encode(map[string]any{"data": data})
			return
		}
		http.NotFound(w, r)
	}))
}

func TestListModels_noServiceConfig(t *testing.T) {
	svc := &service{opts: ServiceOptions{}, registry: harnesses.NewRegistry()}
	_, err := svc.ListModels(context.Background(), ModelFilter{})
	if err == nil {
		t.Fatal("expected error when ServiceConfig is nil")
	}
}

func TestListModels_emptyFilterReturnsAll(t *testing.T) {
	ts1 := fakeModelsServer([]string{"model-a", "model-b"})
	defer ts1.Close()
	ts2 := fakeModelsServer([]string{"model-c"})
	defer ts2.Close()

	sc := &fakeServiceConfig{
		providers: map[string]ServiceProviderEntry{
			"bragi":  {Type: "lmstudio", BaseURL: ts1.URL + "/v1"},
			"remote": {Type: "lmstudio", BaseURL: ts2.URL + "/v1"},
		},
		names:       []string{"bragi", "remote"},
		defaultName: "bragi",
	}
	svc := &service{opts: ServiceOptions{ServiceConfig: sc}, registry: harnesses.NewRegistry()}

	infos, err := svc.ListModels(context.Background(), ModelFilter{})
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(infos) != 3 {
		t.Fatalf("want 3 models total, got %d: %v", len(infos), modelIDs(infos))
	}
}

func TestListModels_filtersProvider(t *testing.T) {
	ts1 := fakeModelsServer([]string{"model-a", "model-b"})
	defer ts1.Close()
	ts2 := fakeModelsServer([]string{"model-c"})
	defer ts2.Close()

	sc := &fakeServiceConfig{
		providers: map[string]ServiceProviderEntry{
			"bragi":  {Type: "lmstudio", BaseURL: ts1.URL + "/v1"},
			"remote": {Type: "lmstudio", BaseURL: ts2.URL + "/v1"},
		},
		names:       []string{"bragi", "remote"},
		defaultName: "bragi",
	}
	svc := &service{opts: ServiceOptions{ServiceConfig: sc}, registry: harnesses.NewRegistry()}

	infos, err := svc.ListModels(context.Background(), ModelFilter{Provider: "bragi"})
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(infos) != 2 {
		t.Fatalf("want 2 bragi models, got %d: %v", len(infos), modelIDs(infos))
	}
	for _, info := range infos {
		if info.Provider != "bragi" {
			t.Errorf("model %q has Provider=%q, want bragi", info.ID, info.Provider)
		}
	}
}

func TestListModels_isDefaultMatchesConfig(t *testing.T) {
	ts := fakeModelsServer([]string{"model-a", "model-b", "default-model"})
	defer ts.Close()

	sc := &fakeServiceConfig{
		providers: map[string]ServiceProviderEntry{
			"bragi": {Type: "lmstudio", BaseURL: ts.URL + "/v1", Model: "default-model"},
		},
		names:       []string{"bragi"},
		defaultName: "bragi",
	}
	svc := &service{opts: ServiceOptions{ServiceConfig: sc}, registry: harnesses.NewRegistry()}

	infos, err := svc.ListModels(context.Background(), ModelFilter{})
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}

	var defaultCount int
	for _, info := range infos {
		if info.IsDefault {
			defaultCount++
			if info.ID != "default-model" {
				t.Errorf("IsDefault=true for wrong model %q", info.ID)
			}
		}
	}
	if defaultCount != 1 {
		t.Errorf("want exactly 1 IsDefault model, got %d", defaultCount)
	}
}

func TestListModels_isConfiguredMatchesRoute(t *testing.T) {
	ts := fakeModelsServer([]string{"model-a", "configured-model", "model-b"})
	defer ts.Close()

	sc := &fakeServiceConfig{
		providers: map[string]ServiceProviderEntry{
			"bragi": {Type: "lmstudio", BaseURL: ts.URL + "/v1"},
		},
		names:       []string{"bragi"},
		defaultName: "bragi",
		routes:      map[string][]string{"configured-model": {"bragi"}},
	}
	svc := &service{opts: ServiceOptions{ServiceConfig: sc}, registry: harnesses.NewRegistry()}

	infos, err := svc.ListModels(context.Background(), ModelFilter{})
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}

	var configuredCount int
	for _, info := range infos {
		if info.IsConfigured {
			configuredCount++
			if info.ID != "configured-model" {
				t.Errorf("IsConfigured=true for unexpected model %q", info.ID)
			}
		}
	}
	if configuredCount != 1 {
		t.Errorf("want exactly 1 IsConfigured model, got %d", configuredCount)
	}
}

func TestListModels_catalogRefSetForKnown(t *testing.T) {
	// Load the embedded catalog to find a known model ID.
	cat, err := modelcatalog.Default()
	if err != nil {
		t.Skip("catalog load failed:", err)
	}
	knownModels := cat.AllConcreteModels(modelcatalog.SurfaceAgentOpenAI)
	if len(knownModels) == 0 {
		t.Skip("no agent.openai models in catalog")
	}
	// Pick the first known model ID deterministically.
	var knownID string
	for id := range knownModels {
		knownID = id
		break
	}

	ts := fakeModelsServer([]string{knownID, "unknown-model-xyz"})
	defer ts.Close()

	sc := &fakeServiceConfig{
		providers: map[string]ServiceProviderEntry{
			"bragi": {Type: "lmstudio", BaseURL: ts.URL + "/v1"},
		},
		names:       []string{"bragi"},
		defaultName: "bragi",
	}
	svc := &service{opts: ServiceOptions{ServiceConfig: sc}, registry: harnesses.NewRegistry()}

	infos, err := svc.ListModels(context.Background(), ModelFilter{})
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}

	var catalogRefFound bool
	for _, info := range infos {
		if info.ID == knownID && info.CatalogRef != "" {
			catalogRefFound = true
		}
	}
	if !catalogRefFound {
		t.Errorf("expected CatalogRef to be set for known catalog model %q; models: %v", knownID, modelInfoDebug(infos))
	}
}

func TestListModels_harnessFilter(t *testing.T) {
	ts := fakeModelsServer([]string{"model-a"})
	defer ts.Close()

	sc := &fakeServiceConfig{
		providers: map[string]ServiceProviderEntry{
			"bragi": {Type: "lmstudio", BaseURL: ts.URL + "/v1"},
		},
		names:       []string{"bragi"},
		defaultName: "bragi",
	}
	svc := &service{opts: ServiceOptions{ServiceConfig: sc}, registry: harnesses.NewRegistry()}

	// Agent harness should return results.
	infos, err := svc.ListModels(context.Background(), ModelFilter{Harness: "agent"})
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(infos) != 1 {
		t.Fatalf("want 1 model for harness=agent, got %d", len(infos))
	}

	// Claude harness should return the documented CLI/TUI model surface.
	infos2, err := svc.ListModels(context.Background(), ModelFilter{Harness: "claude"})
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(infos2) == 0 {
		t.Fatal("want harness-native models for harness=claude")
	}
	for _, info := range infos2 {
		if info.Provider != "claude" || info.Harness != "claude" || !info.Available {
			t.Errorf("unexpected claude model info: %#v", info)
		}
	}

	// Promoted subprocess harnesses expose their documented CLI model surface.
	for _, harness := range []string{"opencode", "pi"} {
		infos, err := svc.ListModels(context.Background(), ModelFilter{Harness: harness})
		if err != nil {
			t.Fatalf("ListModels harness=%s: %v", harness, err)
		}
		if len(infos) == 0 {
			t.Fatalf("want harness-native models for harness=%s", harness)
		}
		for _, info := range infos {
			if info.Provider != harness || info.Harness != harness || !info.Available {
				t.Errorf("unexpected %s model info: %#v", harness, info)
			}
		}
	}

	infos3, err := svc.ListModels(context.Background(), ModelFilter{Harness: "gemini"})
	if err != nil {
		t.Fatalf("ListModels harness=gemini: %v", err)
	}
	if len(infos3) != 0 {
		t.Fatalf("gemini should not expose a harness-native model list until promoted, got %v", modelInfoDebug(infos3))
	}
}

func TestListModels_rankPosition(t *testing.T) {
	ts := fakeModelsServer([]string{"first-model", "second-model", "third-model"})
	defer ts.Close()

	sc := &fakeServiceConfig{
		providers: map[string]ServiceProviderEntry{
			"bragi": {Type: "lmstudio", BaseURL: ts.URL + "/v1"},
		},
		names:       []string{"bragi"},
		defaultName: "bragi",
	}
	svc := &service{opts: ServiceOptions{ServiceConfig: sc}, registry: harnesses.NewRegistry()}

	infos, err := svc.ListModels(context.Background(), ModelFilter{})
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(infos) != 3 {
		t.Fatalf("want 3 models, got %d", len(infos))
	}
	for _, info := range infos {
		if info.RankPosition < 0 {
			t.Errorf("model %q has RankPosition=%d, want >= 0", info.ID, info.RankPosition)
		}
	}
}

// helpers

func modelIDs(infos []ModelInfo) []string {
	out := make([]string, len(infos))
	for i, info := range infos {
		out[i] = info.Provider + ":" + info.ID
	}
	return out
}

func modelInfoDebug(infos []ModelInfo) []string {
	out := make([]string, len(infos))
	for i, info := range infos {
		out[i] = info.Provider + ":" + info.ID + "(ref=" + info.CatalogRef + ")"
	}
	return out
}
