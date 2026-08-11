package modelprovider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
)

const catalogTestResponse = `{"data":[
	{"id":"openai/gpt-test","context_length":128000,
	 "top_provider":{"context_length":128000,"max_completion_tokens":16384}},
	{"id":"openai/gpt-test:free","context_length":8192},
	{"id":"openai/o-test","context_length":200000,
	 "top_provider":{"context_length":200000,"max_completion_tokens":200000}},
	{"id":"openai/codex-test:batch","context_length":400000,
	 "top_provider":{"context_length":400000,"max_completion_tokens":128000}},
	{"id":"maker-a/shared-slug","context_length":32768},
	{"id":"maker-b/shared-slug","context_length":65536},
	{"id":"maker/limitless"}
]}`

func testLimitsCatalog(t *testing.T, handler http.HandlerFunc) (*LimitsCatalog, *atomic.Int64) {
	t.Helper()
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	return &LimitsCatalog{baseURL: server.URL + "/api/v1", client: server.Client()}, &requests
}

func TestFillMissingLimitsFromCatalog(t *testing.T) {
	t.Parallel()
	catalog, _ := testLimitsCatalog(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(catalogTestResponse))
	})

	models := catalog.FillMissingLimits(context.Background(), []DiscoveredModel{
		{Slug: "gpt-test"},
		{Slug: "gpt-test-2024-08-06"},
		{Slug: "o-test"},
		{Slug: "openai/gpt-test"},
		{Slug: "shared-slug"},
		{Slug: "unknown-model"},
		{Slug: "gpt-test", ContextWindowTokens: intPtr(4096), MaxOutputTokens: intPtr(1024)},
		{Slug: "codex-test"},
		{Slug: "gpt-test", MaxOutputTokens: intPtr(128000)},
		{Slug: "gpt-test", MaxOutputTokens: intPtr(1024)},
	})

	for _, index := range []int{0, 1, 3} {
		model := models[index]
		if model.ContextWindowTokens == nil || *model.ContextWindowTokens != 128000 ||
			model.MaxOutputTokens == nil || *model.MaxOutputTokens != 16384 {
			t.Fatalf("model %q was not enriched: %+v", model.Slug, model)
		}
	}
	if models[2].ContextWindowTokens == nil || *models[2].ContextWindowTokens != 200000 ||
		models[2].MaxOutputTokens != nil {
		t.Fatalf("equal max output should be dropped: %+v", models[2])
	}
	if models[4].ContextWindowTokens != nil {
		t.Fatalf("ambiguous shared slug should stay unenriched: %+v", models[4])
	}
	if models[5].ContextWindowTokens != nil {
		t.Fatalf("unknown model should stay unenriched: %+v", models[5])
	}
	if *models[6].ContextWindowTokens != 4096 || *models[6].MaxOutputTokens != 1024 {
		t.Fatalf("provider-reported limits must win over the catalog: %+v", models[6])
	}
	// codex-test only exists in the catalog as the :batch serving variant.
	if models[7].ContextWindowTokens == nil || *models[7].ContextWindowTokens != 400000 ||
		models[7].MaxOutputTokens == nil || *models[7].MaxOutputTokens != 128000 {
		t.Fatalf("variant-only model was not enriched: %+v", models[7])
	}
	// gpt-test also has a :free variant with smaller limits; the plain
	// entry must win (checked via models[0] above keeping 128000).
	if *models[0].ContextWindowTokens != 128000 {
		t.Fatalf("plain catalog entry must beat serving variants: %+v", models[0])
	}
	if *models[8].ContextWindowTokens != 128000 || models[8].MaxOutputTokens != nil {
		t.Fatalf("max output at the filled context window should be dropped: %+v", models[8])
	}
	if *models[9].ContextWindowTokens != 128000 ||
		models[9].MaxOutputTokens == nil || *models[9].MaxOutputTokens != 1024 {
		t.Fatalf("sane provider max output should be kept: %+v", models[9])
	}
}

func TestFillMissingLimitsCachesCatalogFetch(t *testing.T) {
	t.Parallel()
	catalog, requests := testLimitsCatalog(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(catalogTestResponse))
	})

	for range 3 {
		catalog.FillMissingLimits(context.Background(), []DiscoveredModel{{Slug: "gpt-test"}})
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("catalog fetches = %d, want 1", got)
	}

	fresh, freshRequests := testLimitsCatalog(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(catalogTestResponse))
	})
	fresh.FillMissingLimits(context.Background(), []DiscoveredModel{
		{Slug: "gpt-test", ContextWindowTokens: intPtr(4096)},
	})
	if got := freshRequests.Load(); got != 0 {
		t.Fatalf("catalog fetches for fully limited models = %d, want 0", got)
	}
}

func TestFillMissingLimitsIsBestEffortAndBacksOff(t *testing.T) {
	t.Parallel()
	catalog, requests := testLimitsCatalog(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	input := []DiscoveredModel{{Slug: "gpt-test"}}
	models := catalog.FillMissingLimits(context.Background(), input)
	if len(models) != 1 || models[0].ContextWindowTokens != nil {
		t.Fatalf("failed fetch should leave models unenriched: %+v", models)
	}
	catalog.FillMissingLimits(context.Background(), input)
	if got := requests.Load(); got != 1 {
		t.Fatalf("catalog fetches after failure = %d, want 1", got)
	}
}

func TestFillMissingLimitsTreatsEmptyCatalogAsFailure(t *testing.T) {
	t.Parallel()
	catalog, requests := testLimitsCatalog(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[]}`))
	})

	input := []DiscoveredModel{{Slug: "gpt-test"}}
	models := catalog.FillMissingLimits(context.Background(), input)
	if models[0].ContextWindowTokens != nil {
		t.Fatalf("empty catalog should not enrich models: %+v", models)
	}
	catalog.FillMissingLimits(context.Background(), input)
	if got := requests.Load(); got != 1 {
		t.Fatalf("catalog fetches after empty response = %d, want 1", got)
	}
}

func TestFillMissingLimitsSurvivesCanceledCaller(t *testing.T) {
	t.Parallel()
	catalog, _ := testLimitsCatalog(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(catalogTestResponse))
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	models := catalog.FillMissingLimits(ctx, []DiscoveredModel{{Slug: "gpt-test"}})
	if models[0].ContextWindowTokens == nil || *models[0].ContextWindowTokens != 128000 {
		t.Fatalf("canceled caller context should not block enrichment: %+v", models)
	}
}

func TestNewDiscovererEnrichesProviderModels(t *testing.T) {
	t.Parallel()
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-test","created":100}]}`))
	}))
	t.Cleanup(provider.Close)
	catalog, _ := testLimitsCatalog(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(catalogTestResponse))
	})

	config := discoveryProviderConfig(
		provider.URL+"/v1",
		modelprotocol.APIFormatOpenAIResponses,
		modelstore.ModelProviderAuthKindBearerToken,
		`{}`,
	)
	models, err := NewDiscoverer(catalog)(context.Background(), config, "sk", true)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(models) != 1 || models[0].ContextWindowTokens == nil ||
		*models[0].ContextWindowTokens != 128000 ||
		models[0].MaxOutputTokens == nil || *models[0].MaxOutputTokens != 16384 {
		t.Fatalf("discovered models were not enriched: %+v", models)
	}
}
