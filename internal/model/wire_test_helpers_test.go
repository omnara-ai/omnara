package model_test

import (
	"net/http"
	"time"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/model/anthropicmessages"
	"github.com/omnara-ai/omnara/internal/model/openaichatcompletions"
	"github.com/omnara-ai/omnara/internal/model/openairesponses"
)

type wireClientConfig struct {
	capabilities model.Capabilities
	httpClient   *http.Client
	idleTimeout  time.Duration
}

func adapterWireClients(config wireClientConfig) []struct {
	name, field string
	client      model.Client
} {
	return []struct {
		name, field string
		client      model.Client
	}{
		{
			"chat",
			"max_completion_tokens",
			openaichatcompletions.Client{
				EndpointPath:      "/chat/completions",
				ProviderModelSlug: "test-model",
				ModelCapabilities: config.capabilities,
				BaseURL:           "https://example.test",
				HTTPClient:        config.httpClient,
				IdleTimeout:       config.idleTimeout,
			},
		},
		{
			"responses",
			"max_output_tokens",
			openairesponses.Client{
				EndpointPath:      "/responses",
				ProviderModelSlug: "test-model",
				ModelCapabilities: config.capabilities,
				BaseURL:           "https://example.test",
				HTTPClient:        config.httpClient,
				IdleTimeout:       config.idleTimeout,
			},
		},
		{
			"anthropic",
			"max_tokens",
			anthropicmessages.Client{
				EndpointPath:      "/messages",
				ProviderModelSlug: "test-model",
				ModelCapabilities: config.capabilities,
				BaseURL:           "https://example.test",
				HTTPClient:        config.httpClient,
				IdleTimeout:       config.idleTimeout,
			},
		},
	}
}
