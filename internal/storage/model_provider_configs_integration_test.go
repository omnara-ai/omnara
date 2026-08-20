//go:build integration

package storage

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/listing"
	"github.com/omnara-ai/omnara/internal/storage/management"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
	"github.com/omnara-ai/omnara/internal/storage/patch"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
)

func TestModelProviderConfigStorageLifecycle(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	admin := createSecretTestUser(t, ctx, store, "Provider Config Admin", "admin")

	credential, _, err := store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID:     testOrgID,
		OwnerKind: secretstore.SecretOwnerOrg,
		Name:      "openai-provider-key",
		Material:  secrets.GenericMaterial{Value: "sk-test-provider"},
		Actor:     userPrincipal(admin.ID),
	})
	if err != nil {
		t.Fatalf("create org credential: %v", err)
	}
	projectSecret, _, err := store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID:          testOrgID,
		OwnerKind:      secretstore.SecretOwnerProject,
		OwnerProjectID: testProjectID,
		Name:           "project-provider-key",
		Material:       secrets.GenericMaterial{Value: "sk-project-provider"},
		Actor:          userPrincipal(admin.ID),
	})
	if err != nil {
		t.Fatalf("create project credential: %v", err)
	}
	if _, err := store.Models().CreateModelProviderConfig(ctx, modelstore.CreateModelProviderConfigInput{
		OrgID:              testOrgID,
		Name:               "project-secret-should-not-work",
		APIFormat:          modelprotocol.APIFormatOpenAIResponses,
		BaseURL:            "https://api.openai.com/v1",
		CredentialSecretID: projectSecret.ID,
	}); err == nil {
		t.Fatal("project-owned secret should not be accepted as provider config credential")
	}
	oauthSecret, _, err := store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID:     testOrgID,
		OwnerKind: secretstore.SecretOwnerOrg,
		Name:      "oauth-provider-key",
		Material:  secrets.OAuthTokenSetMaterial{AccessToken: "oauth-token"},
		Actor:     userPrincipal(admin.ID),
	})
	if err != nil {
		t.Fatalf("create org oauth credential: %v", err)
	}
	if _, err := store.Models().CreateModelProviderConfig(ctx, modelstore.CreateModelProviderConfigInput{
		OrgID:              testOrgID,
		Name:               "oauth-secret-should-not-work",
		APIFormat:          modelprotocol.APIFormatOpenAIResponses,
		BaseURL:            "https://api.openai.com/v1",
		CredentialSecretID: oauthSecret.ID,
	}); err == nil {
		t.Fatal("non-generic org secret should not be accepted as provider config credential")
	}

	config, err := store.Models().CreateModelProviderConfig(ctx, modelstore.CreateModelProviderConfigInput{
		OrgID:              testOrgID,
		Name:               "openai-lifecycle",
		APIFormat:          modelprotocol.APIFormatOpenAIResponses,
		BaseURL:            "https://api.openai.com/v1",
		RequestTimeoutMS:   30000,
		CredentialSecretID: credential.ID,
	})
	if err != nil {
		t.Fatalf("create provider config: %v", err)
	}
	if !config.Created || config.APIVariant != "default" || config.CredentialSecretID != credential.ID ||
		config.EndpointPath != "/responses" ||
		config.AuthKind != modelstore.ModelProviderAuthKindBearerToken ||
		config.RequestTimeoutMS != 30000 ||
		!sameJSON(config.AuthOptions, json.RawMessage(`{}`)) {
		t.Fatalf("unexpected provider config: %+v", config)
	}
	for _, test := range []struct {
		name  string
		query string
		value any
	}{
		{name: "id", query: `UPDATE model_provider_configs SET id = $1 WHERE org_id = $2 AND id = $3`, value: testID("changed-provider-config-id")},
		{name: "organization", query: `UPDATE model_provider_configs SET org_id = $1 WHERE org_id = $2 AND id = $3`, value: testID("changed-provider-config-org")},
		{name: "management kind", query: `UPDATE model_provider_configs SET management_kind = $1 WHERE org_id = $2 AND id = $3`, value: "cluster"},
		{name: "API format", query: `UPDATE model_provider_configs SET api_format = $1 WHERE org_id = $2 AND id = $3`, value: "anthropic-messages"},
		{name: "API variant", query: `UPDATE model_provider_configs SET api_variant = $1 WHERE org_id = $2 AND id = $3`, value: "openrouter"},
	} {
		if _, err := pool.Exec(ctx, test.query, test.value, config.OrgID, config.ID); !isPgCode(err, "25006") {
			t.Fatalf("update model provider config %s error = %v, want SQLSTATE 25006", test.name, err)
		}
	}
	normalizedBaseURLConfig, err := store.Models().CreateModelProviderConfig(ctx, modelstore.CreateModelProviderConfigInput{
		OrgID:              testOrgID,
		Name:               "openai-normalized-base-url",
		APIFormat:          modelprotocol.APIFormatOpenAIResponses,
		BaseURL:            "https://api.openai.com/v1/",
		CredentialSecretID: credential.ID,
	})
	if err != nil {
		t.Fatalf("create provider config with trailing base_url slash: %v", err)
	}
	if normalizedBaseURLConfig.BaseURL != "https://api.openai.com/v1" {
		t.Fatalf("normalized base_url = %q, want https://api.openai.com/v1", normalizedBaseURLConfig.BaseURL)
	}
	if _, err := store.Models().CreateModelProviderConfig(ctx, modelstore.CreateModelProviderConfigInput{
		OrgID:              testOrgID,
		Name:               "openai-userinfo-base-url",
		APIFormat:          modelprotocol.APIFormatOpenAIResponses,
		BaseURL:            "https://user:secret@api.openai.com/v1",
		CredentialSecretID: credential.ID,
	}); err == nil {
		t.Fatal("provider base_url with user information should be rejected")
	}
	localHTTPConfig, err := store.Models().CreateModelProviderConfig(ctx, modelstore.CreateModelProviderConfigInput{
		OrgID:              testOrgID,
		Name:               "openai-local-http-base-url",
		APIFormat:          modelprotocol.APIFormatOpenAIResponses,
		BaseURL:            "http://localhost:8080/v1/",
		CredentialSecretID: credential.ID,
	})
	if err != nil {
		t.Fatalf("create local HTTP provider config: %v", err)
	}
	if localHTTPConfig.BaseURL != "http://localhost:8080/v1" {
		t.Fatalf("local HTTP base_url = %q, want http://localhost:8080/v1", localHTTPConfig.BaseURL)
	}
	replayedConfig, err := store.Models().CreateModelProviderConfig(ctx, modelstore.CreateModelProviderConfigInput{
		OrgID:              testOrgID,
		Name:               "openai-lifecycle",
		APIFormat:          modelprotocol.APIFormatOpenAIResponses,
		BaseURL:            "https://api.openai.com/v1",
		RequestTimeoutMS:   30000,
		CredentialSecretID: credential.ID,
	})
	if err != nil {
		t.Fatalf("replay provider config: %v", err)
	}
	if replayedConfig.ID != config.ID || replayedConfig.Created {
		t.Fatalf("provider config replay mismatch: first=%+v replay=%+v", config, replayedConfig)
	}
	if _, err := store.Models().CreateModelProviderConfig(ctx, modelstore.CreateModelProviderConfigInput{
		OrgID:              testOrgID,
		Name:               "openai-lifecycle",
		APIFormat:          modelprotocol.APIFormatAnthropicMessages,
		BaseURL:            "https://api.anthropic.com",
		CredentialSecretID: credential.ID,
	}); !errors.Is(err, storeerr.ErrIdempotencyConflict) {
		t.Fatalf("conflicting provider config replay error = %v, want ErrIdempotencyConflict", err)
	}
	if _, err := store.Models().CreateModelProviderConfig(ctx, modelstore.CreateModelProviderConfigInput{
		OrgID:              testOrgID,
		Name:               "unsupported-compat",
		APIFormat:          modelprotocol.APIFormatOpenAIResponses,
		APIVariant:         "openrouter",
		BaseURL:            "https://api.openai.com/v1",
		CredentialSecretID: credential.ID,
	}); err == nil {
		t.Fatal("unsupported API variant should be rejected")
	}
	if _, err := store.Models().CreateModelProviderConfig(ctx, modelstore.CreateModelProviderConfigInput{
		OrgID:              testOrgID,
		Name:               "bad-timeout-should-not-work",
		APIFormat:          modelprotocol.APIFormatOpenAIResponses,
		RequestTimeoutMS:   -1,
		BaseURL:            "https://api.openai.com/v1",
		CredentialSecretID: credential.ID,
	}); !errors.Is(err, storeerr.ErrInvalidModelProviderConfig) {
		t.Fatalf("invalid timeout_ms error = %v, want ErrInvalidModelProviderConfig", err)
	}
	for _, tc := range []struct {
		name    string
		baseURL string
	}{
		{name: "base-url-without-host", baseURL: "https:///v1"},
		{name: "base-url-unsupported-scheme", baseURL: "ftp://api.openai.com/v1"},
		{name: "base-url-public-http", baseURL: "http://api.openai.com/v1"},
		{name: "base-url-with-query", baseURL: "https://api.openai.com/v1?tenant=test"},
		{name: "base-url-with-fragment", baseURL: "https://api.openai.com/v1#responses"},
	} {
		if _, err := store.Models().CreateModelProviderConfig(ctx, modelstore.CreateModelProviderConfigInput{
			OrgID:              testOrgID,
			Name:               tc.name,
			APIFormat:          modelprotocol.APIFormatOpenAIResponses,
			BaseURL:            tc.baseURL,
			CredentialSecretID: credential.ID,
		}); !errors.Is(err, storeerr.ErrInvalidModelProviderConfig) {
			t.Fatalf("%s error = %v, want ErrInvalidModelProviderConfig", tc.name, err)
		}
	}
	if _, err := store.Models().CreateModelProviderConfig(ctx, modelstore.CreateModelProviderConfigInput{
		OrgID:              testOrgID,
		Name:               "bad-bearer-auth-options",
		APIFormat:          modelprotocol.APIFormatOpenAIResponses,
		AuthKind:           modelstore.ModelProviderAuthKindBearerToken,
		AuthOptions:        json.RawMessage(`{"header_name":"authorization"}`),
		BaseURL:            "https://api.openai.com/v1",
		CredentialSecretID: credential.ID,
	}); !errors.Is(err, storeerr.ErrInvalidModelProviderConfig) {
		t.Fatalf("bearer auth options error = %v, want ErrInvalidModelProviderConfig", err)
	}
	if _, err := store.Models().CreateModelProviderConfig(ctx, modelstore.CreateModelProviderConfigInput{
		OrgID:              testOrgID,
		Name:               "bad-header-auth-options",
		APIFormat:          modelprotocol.APIFormatOpenAIResponses,
		AuthKind:           modelstore.ModelProviderAuthKindAPIKeyHeader,
		BaseURL:            "https://api.openai.com/v1",
		CredentialSecretID: credential.ID,
	}); !errors.Is(err, storeerr.ErrInvalidModelProviderConfig) {
		t.Fatalf("missing header auth options error = %v, want ErrInvalidModelProviderConfig", err)
	}
	for _, tc := range []struct {
		name       string
		headerName string
	}{
		{name: "auth-header-content-type", headerName: "Content-Type"},
		{name: "auth-header-idempotency-key", headerName: "Idempotency-Key"},
		{name: "auth-header-x-idempotency-key", headerName: "x-IDEMPOTENCY-key"},
		{name: "auth-header-invalid-token", headerName: "api/key"},
	} {
		if _, err := store.Models().CreateModelProviderConfig(ctx, modelstore.CreateModelProviderConfigInput{
			OrgID:              testOrgID,
			Name:               tc.name,
			APIFormat:          modelprotocol.APIFormatOpenAIResponses,
			AuthKind:           modelstore.ModelProviderAuthKindAPIKeyHeader,
			AuthOptions:        json.RawMessage(`{"header_name":"` + tc.headerName + `"}`),
			BaseURL:            "https://api.openai.com/v1",
			CredentialSecretID: credential.ID,
		}); !errors.Is(err, storeerr.ErrInvalidModelProviderConfig) {
			t.Fatalf("%s error = %v, want ErrInvalidModelProviderConfig", tc.name, err)
		}
	}
	if _, err := store.Models().CreateModelProviderConfig(ctx, modelstore.CreateModelProviderConfigInput{
		OrgID:              testOrgID,
		Name:               "provider-protocol-header-auth-name",
		APIFormat:          modelprotocol.APIFormatOpenAIResponses,
		AuthKind:           modelstore.ModelProviderAuthKindAPIKeyHeader,
		AuthOptions:        json.RawMessage(`{"header_name":"Anthropic-Version"}`),
		BaseURL:            "https://api.openai.com/v1",
		CredentialSecretID: credential.ID,
	}); err != nil {
		t.Fatalf("provider protocol header auth name should be left to runtime/provider behavior: %v", err)
	}

	anthropicConfig, err := store.Models().CreateModelProviderConfig(ctx, modelstore.CreateModelProviderConfigInput{
		OrgID:              testOrgID,
		Name:               "anthropic-lifecycle",
		APIFormat:          modelprotocol.APIFormatAnthropicMessages,
		BaseURL:            "https://api.anthropic.com/v1",
		CredentialSecretID: credential.ID,
	})
	if err != nil {
		t.Fatalf("create anthropic provider config: %v", err)
	}
	if anthropicConfig.EndpointPath != "/messages" || anthropicConfig.AuthKind != modelstore.ModelProviderAuthKindAPIKeyHeader ||
		!sameJSON(anthropicConfig.AuthOptions, json.RawMessage(`{"header_name":"x-api-key"}`)) {
		t.Fatalf("unexpected anthropic provider defaults: %+v", anthropicConfig)
	}
	openRouterConfig, err := store.Models().CreateModelProviderConfig(ctx, modelstore.CreateModelProviderConfigInput{
		OrgID:              testOrgID,
		Name:               "openrouter-lifecycle",
		APIFormat:          modelprotocol.APIFormatOpenAIChatCompletions,
		APIVariant:         modelprotocol.APIVariantOpenRouter,
		BaseURL:            "https://openrouter.ai/api/v1",
		CredentialSecretID: credential.ID,
	})
	if err != nil {
		t.Fatalf("create openrouter provider config: %v", err)
	}
	if _, err := store.Models().CreateConfiguredModel(ctx, modelstore.CreateConfiguredModelInput{
		OrgID:                 testOrgID,
		ModelProviderConfigID: config.ID,
		Name:                  "null-options-should-not-work",
		ProviderModelSlug:     "null-options-should-not-work",
		ContextWindowTokens:   128000,
		MaxOutputTokens:       8192,
		APIVariantOptions:     json.RawMessage(`null`),
	}); !errors.Is(err, storeerr.ErrInvalidModelProviderConfig) {
		t.Fatalf("null api_variant_options error = %v, want ErrInvalidModelProviderConfig", err)
	}
	if _, err := store.Models().CreateConfiguredModel(ctx, modelstore.CreateConfiguredModelInput{
		OrgID:                 testOrgID,
		ModelProviderConfigID: config.ID,
		Name:                  "non-object-api-variant-options-should-not-work",
		ProviderModelSlug:     "non-object-api-variant-options-should-not-work",
		ContextWindowTokens:   128000,
		MaxOutputTokens:       8192,
		APIVariantOptions:     json.RawMessage(`["runtime","will","decide"]`),
	}); !errors.Is(err, storeerr.ErrInvalidModelProviderConfig) {
		t.Fatalf("non-object api_variant_options error = %v, want ErrInvalidModelProviderConfig", err)
	}
	openRouterModel, err := store.Models().CreateConfiguredModel(ctx, modelstore.CreateConfiguredModelInput{
		OrgID:                  testOrgID,
		ModelProviderConfigID:  openRouterConfig.ID,
		Name:                   "openrouter-model",
		ProviderModelSlug:      "openrouter/model",
		ContextWindowTokens:    128000,
		MaxOutputTokens:        8192,
		DefaultMaxOutputTokens: intPtr(4096),
		APIVariantOptions: json.RawMessage(
			`{"provider":{"only":["anthropic"],"data_collection":"deny"},"temperature":0.2}`,
		),
	})
	if err != nil {
		t.Fatalf("create openrouter configured model with api_variant_options: %v", err)
	}
	if !sameJSON(
		openRouterModel.APIVariantOptions,
		json.RawMessage(`{"provider":{"only":["anthropic"],"data_collection":"deny"},"temperature":0.2}`),
	) {
		t.Fatalf("openrouter configured model options mismatch: %+v", openRouterModel)
	}
	if _, err := store.Models().CreateConfiguredModel(ctx, modelstore.CreateConfiguredModelInput{
		OrgID:                 testOrgID,
		ModelProviderConfigID: anthropicConfig.ID,
		Name:                  "claude-missing-max-output",
		ProviderModelSlug:     "claude-missing-max-output",
		ContextWindowTokens:   200000,
	}); !errors.Is(err, storeerr.ErrInvalidModelProviderConfig) {
		t.Fatalf("anthropic-messages configured model without max output error = %v, want ErrInvalidModelProviderConfig", err)
	}
	if _, err := store.Models().CreateConfiguredModel(ctx, modelstore.CreateConfiguredModelInput{
		OrgID:                 testOrgID,
		ModelProviderConfigID: anthropicConfig.ID,
		Name:                  "claude-inherits-max-output",
		ProviderModelSlug:     "claude-inherits-max-output",
		ContextWindowTokens:   200000,
		MaxOutputTokens:       4096,
	}); err != nil {
		t.Fatalf("create anthropic-messages model without format-specific default: %v", err)
	}
	if _, err := store.Models().CreateConfiguredModel(ctx, modelstore.CreateConfiguredModelInput{
		OrgID:                  testOrgID,
		ModelProviderConfigID:  anthropicConfig.ID,
		Name:                   "claude-thinking-not-yet",
		ProviderModelSlug:      "claude-thinking-not-yet",
		ContextWindowTokens:    200000,
		MaxOutputTokens:        4096,
		SupportsReasoning:      true,
		DefaultReasoningEffort: "high",
	}); !errors.Is(err, storeerr.ErrInvalidModelProviderConfig) {
		t.Fatalf("anthropic reasoning options error = %v, want ErrInvalidModelProviderConfig", err)
	}

	configuredModel, err := store.Models().CreateConfiguredModel(ctx, modelstore.CreateConfiguredModelInput{
		OrgID:                     testOrgID,
		ModelProviderConfigID:     config.ID,
		Name:                      "gpt-5.4",
		ProviderModelSlug:         "gpt-5.4",
		ContextWindowTokens:       200000,
		MaxOutputTokens:           64000,
		DefaultMaxOutputTokens:    intPtr(32000),
		DefaultCacheRetention:     modelstore.ModelCacheRetentionShort,
		SupportsTools:             boolPtr(true),
		SupportsReasoning:         true,
		DefaultReasoningEffort:    "high",
		SupportedReasoningEfforts: []string{"low", "medium", "high"},
		InputModalities:           []string{"text", "image"},
		OutputModalities:          []string{"text"},
	})
	if err != nil {
		t.Fatalf("create configured model: %v", err)
	}
	if configuredModel.ManagementKind != management.Tenant {
		t.Fatalf("configured model management kind = %q, want tenant", configuredModel.ManagementKind)
	}
	if _, err := pool.Exec(
		ctx,
		`UPDATE configured_models SET management_kind = 'cluster' WHERE org_id = $1 AND id = $2`,
		configuredModel.OrgID,
		configuredModel.ID,
	); !isPgCode(err, "25006") {
		t.Fatalf("update configured model management kind error = %v, want SQLSTATE 25006", err)
	}
	if _, err := store.Models().CreateConfiguredModel(ctx, modelstore.CreateConfiguredModelInput{
		OrgID:                 testOrgID,
		ModelProviderConfigID: config.ID,
		Name:                  "zero-context-window",
		ProviderModelSlug:     "zero-context-window",
		MaxOutputTokens:       4096,
	}); !errors.Is(err, storeerr.ErrInvalidModelProviderConfig) {
		t.Fatalf("zero context_window_tokens error = %v, want ErrInvalidModelProviderConfig", err)
	}
	if _, err := store.Models().CreateConfiguredModel(ctx, modelstore.CreateConfiguredModelInput{
		OrgID:                     testOrgID,
		ModelProviderConfigID:     config.ID,
		Name:                      "bad-reasoning-default",
		ProviderModelSlug:         "bad-reasoning-default",
		ContextWindowTokens:       200000,
		MaxOutputTokens:           8192,
		DefaultReasoningEffort:    "xhigh",
		SupportsReasoning:         true,
		SupportedReasoningEfforts: []string{"low", "medium", "high"},
	}); !errors.Is(err, storeerr.ErrInvalidModelProviderConfig) {
		t.Fatalf("unsupported default_reasoning_effort error = %v, want ErrInvalidModelProviderConfig", err)
	}
	if _, err := store.Models().CreateConfiguredModel(ctx, modelstore.CreateConfiguredModelInput{
		OrgID:                     testOrgID,
		ModelProviderConfigID:     config.ID,
		Name:                      "reasoning-metadata-without-support",
		ProviderModelSlug:         "reasoning-metadata-without-support",
		ContextWindowTokens:       200000,
		MaxOutputTokens:           8192,
		DefaultReasoningEffort:    "high",
		SupportedReasoningEfforts: []string{"high"},
	}); !errors.Is(err, storeerr.ErrInvalidModelProviderConfig) {
		t.Fatalf("reasoning metadata without supports_reasoning error = %v, want ErrInvalidModelProviderConfig", err)
	}
	if configuredModel.Name != "gpt-5.4" || configuredModel.ProviderModelSlug != "gpt-5.4" ||
		configuredModel.MaxOutputTokens != 64000 ||
		configuredModel.DefaultCacheRetention != modelstore.ModelCacheRetentionShort ||
		!configuredModel.SupportsTools ||
		!configuredModel.SupportsReasoning ||
		configuredModel.DefaultReasoningEffort != "high" ||
		!slices.Equal(configuredModel.SupportedReasoningEfforts, []string{"low", "medium", "high"}) ||
		!slices.Equal(configuredModel.InputModalities, []string{"text", "image"}) ||
		!slices.Equal(configuredModel.OutputModalities, []string{"text"}) {
		t.Fatalf("unexpected configured model: %+v", configuredModel)
	}
	replayedModel, err := store.Models().CreateConfiguredModel(ctx, modelstore.CreateConfiguredModelInput{
		OrgID:                     testOrgID,
		ModelProviderConfigID:     config.ID,
		Name:                      "gpt-5.4",
		ProviderModelSlug:         "gpt-5.4",
		ContextWindowTokens:       200000,
		MaxOutputTokens:           64000,
		DefaultMaxOutputTokens:    intPtr(32000),
		DefaultCacheRetention:     modelstore.ModelCacheRetentionShort,
		SupportsTools:             boolPtr(true),
		SupportsReasoning:         true,
		DefaultReasoningEffort:    "high",
		SupportedReasoningEfforts: []string{"low", "medium", "high"},
		InputModalities:           []string{"text", "image"},
		OutputModalities:          []string{"text"},
	})
	if err != nil {
		t.Fatalf("replay configured model: %v", err)
	}
	if replayedModel.ID != configuredModel.ID || replayedModel.Created {
		t.Fatalf("configured model replay mismatch: first=%+v replay=%+v", configuredModel, replayedModel)
	}
	if _, err := store.Models().CreateConfiguredModel(ctx, modelstore.CreateConfiguredModelInput{
		OrgID:                 testOrgID,
		ModelProviderConfigID: config.ID,
		Name:                  "gpt-5.4",
		ProviderModelSlug:     "gpt-5.4",
		ContextWindowTokens:   200000,
		MaxOutputTokens:       1,
	}); !errors.Is(err, storeerr.ErrIdempotencyConflict) {
		t.Fatalf("conflicting configured model replay error = %v, want ErrIdempotencyConflict", err)
	}
	updatedProviderModelSlug := "gpt-5.4"
	updatedContextWindow := 240000
	updatedDefaultReasoningEffort := "medium"
	updatedSupportedReasoningEfforts := []string{"low", "medium", "high"}
	updatedInputModalities := []string{"text"}
	updatedOutputModalities := []string{"text"}
	updatedDefaultCacheRetention := modelstore.ModelCacheRetentionLong
	updatedSupportsTools := true
	updatedSupportsReasoning := true
	updatedModel, err := store.Models().PatchConfiguredModel(ctx, modelstore.PatchConfiguredModelInput{
		OrgID:                     testOrgID,
		ModelProviderConfigID:     config.ID,
		ID:                        configuredModel.ID,
		ProviderModelSlug:         &updatedProviderModelSlug,
		ContextWindowTokens:       &updatedContextWindow,
		MaxOutputTokens:           intPtr(96000),
		DefaultMaxOutputTokens:    nullableInt(48000),
		DefaultCacheRetention:     &updatedDefaultCacheRetention,
		SupportsTools:             &updatedSupportsTools,
		SupportsReasoning:         &updatedSupportsReasoning,
		DefaultReasoningEffort:    &updatedDefaultReasoningEffort,
		SupportedReasoningEfforts: &updatedSupportedReasoningEfforts,
		InputModalities:           &updatedInputModalities,
		OutputModalities:          &updatedOutputModalities,
	})
	if err != nil {
		t.Fatalf("update configured model: %v", err)
	}
	if updatedModel.ID != configuredModel.ID || updatedModel.CurrentRevisionID == configuredModel.CurrentRevisionID ||
		updatedModel.Name != "gpt-5.4" ||
		updatedModel.ProviderModelSlug != "gpt-5.4" ||
		updatedModel.MaxOutputTokens != 96000 ||
		updatedModel.DefaultCacheRetention != modelstore.ModelCacheRetentionLong ||
		updatedModel.DefaultReasoningEffort != "medium" {
		t.Fatalf("unexpected updated configured model: %+v", updatedModel)
	}
	previousRevisionID := updatedModel.CurrentRevisionID
	updatedAPIVariantOptions := json.RawMessage(`{"temperature":0.3}`)
	updatedModel, err = store.Models().PatchConfiguredModel(ctx, modelstore.PatchConfiguredModelInput{
		OrgID:                 testOrgID,
		ModelProviderConfigID: config.ID,
		ID:                    updatedModel.ID,
		APIVariantOptions:     &updatedAPIVariantOptions,
	})
	if err != nil {
		t.Fatalf("update configured model api variant options: %v", err)
	}
	if updatedModel.CurrentRevisionID == previousRevisionID ||
		!sameJSON(updatedModel.APIVariantOptions, updatedAPIVariantOptions) {
		t.Fatalf("api variant options update should create revision: before=%s after=%+v", previousRevisionID, updatedModel)
	}
	previousRevision, err := store.Models().GetConfiguredModelRevisionDisplay(ctx, testOrgID, previousRevisionID)
	if err != nil {
		t.Fatalf("load previous configured model revision: %v", err)
	}
	if !sameJSON(previousRevision.APIVariantOptions, json.RawMessage(`{}`)) {
		t.Fatalf("previous revision api variant options mutated: %+v", previousRevision)
	}
	conflictingModel, err := store.Models().CreateConfiguredModel(ctx, modelstore.CreateConfiguredModelInput{
		OrgID:                  testOrgID,
		ModelProviderConfigID:  config.ID,
		Name:                   "gpt-rename-conflict",
		ProviderModelSlug:      "gpt-rename-conflict",
		ContextWindowTokens:    128000,
		MaxOutputTokens:        8192,
		DefaultMaxOutputTokens: intPtr(4096),
	})
	if err != nil {
		t.Fatalf("create rename-conflict configured model: %v", err)
	}
	conflictingName := conflictingModel.Name
	if _, err := store.Models().PatchConfiguredModel(ctx, modelstore.PatchConfiguredModelInput{
		OrgID:                 testOrgID,
		ModelProviderConfigID: config.ID,
		ID:                    updatedModel.ID,
		Name:                  &conflictingName,
	}); !errors.Is(err, storeerr.ErrConflict) {
		t.Fatalf("duplicate configured model rename error = %v, want ErrConflict", err)
	}
	if _, err := store.Models().DeleteConfiguredModel(
		ctx,
		testOrgID,
		conflictingModel.ID,
	); err != nil {
		t.Fatalf("archive rename-conflict configured model: %v", err)
	}

	renamedName := "gpt-default"
	renamedModel, err := store.Models().PatchConfiguredModel(ctx, modelstore.PatchConfiguredModelInput{
		OrgID:                 testOrgID,
		ModelProviderConfigID: config.ID,
		ID:                    updatedModel.ID,
		Name:                  &renamedName,
	})
	if err != nil {
		t.Fatalf("rename configured model: %v", err)
	}
	if renamedModel.Name != renamedName || renamedModel.CurrentRevisionID != updatedModel.CurrentRevisionID {
		t.Fatalf("pure configured model rename should not create revision: before=%+v after=%+v", updatedModel, renamedModel)
	}
	if _, err := store.Models().GetConfiguredModelByName(ctx, testOrgID, config.ID, updatedModel.Name); !storeerr.IsNotFound(err) {
		t.Fatalf("old configured model name lookup error = %v, want not found", err)
	}
	if resolvedRenamed, err := store.Models().GetConfiguredModelByName(
		ctx,
		testOrgID,
		config.ID,
		renamedName,
	); err != nil ||
		resolvedRenamed.ID != renamedModel.ID {
		t.Fatalf("renamed configured model lookup mismatch: model=%+v err=%v", resolvedRenamed, err)
	}

	renamedAndUpdatedName := "gpt-default-plus"
	renamedAndUpdatedProviderSlug := "gpt-5.5"
	renamedAndUpdatedModel, err := store.Models().PatchConfiguredModel(ctx, modelstore.PatchConfiguredModelInput{
		OrgID:                 testOrgID,
		ModelProviderConfigID: config.ID,
		ID:                    renamedModel.ID,
		Name:                  &renamedAndUpdatedName,
		ProviderModelSlug:     &renamedAndUpdatedProviderSlug,
	})
	if err != nil {
		t.Fatalf("rename and update configured model: %v", err)
	}
	if renamedAndUpdatedModel.Name != renamedAndUpdatedName ||
		renamedAndUpdatedModel.ProviderModelSlug != renamedAndUpdatedProviderSlug ||
		renamedAndUpdatedModel.CurrentRevisionID == renamedModel.CurrentRevisionID {
		t.Fatalf(
			"rename plus behavior update should create revision: before=%+v after=%+v",
			renamedModel,
			renamedAndUpdatedModel,
		)
	}
	unchangedProviderModelSlug := renamedAndUpdatedModel.ProviderModelSlug
	unchangedModel, err := store.Models().PatchConfiguredModel(ctx, modelstore.PatchConfiguredModelInput{
		OrgID:                 testOrgID,
		ModelProviderConfigID: config.ID,
		ID:                    renamedAndUpdatedModel.ID,
		ProviderModelSlug:     &unchangedProviderModelSlug,
	})
	if err != nil {
		t.Fatalf("patch unchanged configured model behavior: %v", err)
	}
	if unchangedModel.CurrentRevisionID != renamedAndUpdatedModel.CurrentRevisionID {
		t.Fatalf(
			"unchanged configured model behavior should not create revision: before=%+v after=%+v",
			renamedAndUpdatedModel,
			unchangedModel,
		)
	}
	configuredModel = renamedAndUpdatedModel
	referencedConfig, err := store.Models().CreateModelProviderConfig(ctx, modelstore.CreateModelProviderConfigInput{
		OrgID:              testOrgID,
		Name:               "openai-referenced",
		APIFormat:          modelprotocol.APIFormatOpenAIResponses,
		BaseURL:            "https://api.openai.com/v1",
		CredentialSecretID: credential.ID,
	})
	if err != nil {
		t.Fatalf("create referenced provider config: %v", err)
	}
	referencedModel, err := store.Models().CreateConfiguredModel(ctx, modelstore.CreateConfiguredModelInput{
		OrgID:                  testOrgID,
		ModelProviderConfigID:  referencedConfig.ID,
		Name:                   "gpt-referenced",
		ProviderModelSlug:      "gpt-referenced",
		ContextWindowTokens:    128000,
		MaxOutputTokens:        8192,
		DefaultMaxOutputTokens: intPtr(4096),
	})
	if err != nil {
		t.Fatalf("create referenced configured model: %v", err)
	}
	referencedSource := `instruction: Test referenced model mutability.
model:
  provider_config: openai-referenced
  name: gpt-referenced
`
	referencedCompiled, err := agentconfig.Compile(
		agentconfig.SourceFormatYAML,
		[]byte(referencedSource),
		agentconfig.CompileOptions{
			ResolveModelSelection: func(providerConfigName string, configuredModelName string) (agentconfig.ResolvedModelSelection, error) {
				return resolvedTestModelSelection(referencedModel), nil
			},
		},
	)
	if err != nil {
		t.Fatalf("compile referenced configured model: %v", err)
	}
	if _, err := store.Models().CreateProjectModelGrant(ctx, modelstore.CreateProjectModelGrantInput{
		OrgID:             testOrgID,
		ProjectID:         testProjectID,
		ConfiguredModelID: referencedModel.ID,
	}); err != nil {
		t.Fatalf("grant referenced configured model: %v", err)
	}
	referencedAgentConfig, err := store.Execution().CreateAgentConfig(ctx, executionstore.CreateAgentConfigInput{
		ProjectID:               testProjectID,
		Definition:              json.RawMessage(referencedCompiled.CanonicalJSON),
		Source:                  referencedSource,
		SourceFormat:            string(agentconfig.SourceFormatYAML),
		ConfiguredModelID:       referencedModel.ID,
		CompiledDefinition:      json.RawMessage(referencedCompiled.CanonicalJSON),
		CompilerVersion:         agentconfig.CompilerVersion,
		EffectiveDefinitionHash: referencedCompiled.Hash,
	})
	if err != nil {
		t.Fatalf("create agent config referencing configured model: %v", err)
	}
	updatedReferencedProviderModelSlug := "gpt-referenced-v2"
	updatedReferencedContextWindow := 200000
	updatedReferencedModel, err := store.Models().PatchConfiguredModel(ctx, modelstore.PatchConfiguredModelInput{
		OrgID:                 testOrgID,
		ModelProviderConfigID: referencedConfig.ID,
		ID:                    referencedModel.ID,
		ProviderModelSlug:     &updatedReferencedProviderModelSlug,
		ContextWindowTokens:   &updatedReferencedContextWindow,
		MaxOutputTokens:       intPtr(16384),
	})
	if err != nil {
		t.Fatalf("update referenced configured model: %v", err)
	}
	if updatedReferencedModel.CurrentRevisionID == referencedModel.CurrentRevisionID {
		t.Fatalf(
			"configured model update should create a new revision: before=%+v after=%+v",
			referencedModel,
			updatedReferencedModel,
		)
	}
	if referencedAgentConfig.ConfiguredModelID != referencedModel.ID {
		t.Fatalf(
			"agent config should remain pinned to configured model alias: config=%+v model=%+v",
			referencedAgentConfig,
			referencedModel,
		)
	}

	grant, err := store.Models().CreateProjectModelGrant(ctx, modelstore.CreateProjectModelGrantInput{
		OrgID:                     testOrgID,
		ProjectID:                 testProjectID,
		ConfiguredModelID:         configuredModel.ID,
		ContextWindowTokens:       intPtr(200000),
		MaxOutputTokens:           intPtr(64000),
		DefaultMaxOutputTokens:    intPtr(32000),
		DefaultCacheRetention:     modelstore.ModelCacheRetentionShort,
		SupportsTools:             boolPtr(true),
		SupportsReasoning:         boolPtr(true),
		DefaultReasoningEffort:    "medium",
		SupportedReasoningEfforts: []string{"low", "medium"},
		InputModalities:           []string{"text"},
		OutputModalities:          []string{"text"},
	})
	if err != nil {
		t.Fatalf("create project model grant: %v", err)
	}
	if grant.ConfiguredModelID != configuredModel.ID {
		t.Fatalf("unexpected project model grant: %+v", grant)
	}
	if grant.ContextWindowTokens == nil || *grant.ContextWindowTokens != 200000 || grant.MaxOutputTokens == nil ||
		*grant.MaxOutputTokens != 64000 ||
		grant.DefaultMaxOutputTokens == nil ||
		*grant.DefaultMaxOutputTokens != 32000 ||
		grant.DefaultCacheRetention != modelstore.ModelCacheRetentionShort ||
		grant.SupportsTools == nil ||
		!*grant.SupportsTools ||
		grant.SupportsReasoning == nil ||
		!*grant.SupportsReasoning ||
		grant.DefaultReasoningEffort != "medium" ||
		!slices.Equal(grant.SupportedReasoningEfforts, []string{"low", "medium"}) ||
		!slices.Equal(grant.InputModalities, []string{"text"}) ||
		!slices.Equal(grant.OutputModalities, []string{"text"}) {
		t.Fatalf("project model grant overlay mismatch: %+v", grant)
	}
	if !grant.Created {
		t.Fatalf("project model grant should report Created on first create: %+v", grant)
	}
	replayedGrant, err := store.Models().CreateProjectModelGrant(ctx, modelstore.CreateProjectModelGrantInput{
		OrgID:                     testOrgID,
		ProjectID:                 testProjectID,
		ConfiguredModelID:         configuredModel.ID,
		ContextWindowTokens:       intPtr(200000),
		MaxOutputTokens:           intPtr(64000),
		DefaultMaxOutputTokens:    intPtr(32000),
		DefaultCacheRetention:     modelstore.ModelCacheRetentionShort,
		SupportsTools:             boolPtr(true),
		SupportsReasoning:         boolPtr(true),
		DefaultReasoningEffort:    "medium",
		SupportedReasoningEfforts: []string{"low", "medium"},
		InputModalities:           []string{"text"},
		OutputModalities:          []string{"text"},
	})
	if err != nil {
		t.Fatalf("replay project model grant: %v", err)
	}
	if replayedGrant.ID != grant.ID {
		t.Fatalf("project model grant replay mismatch: first=%+v replay=%+v", grant, replayedGrant)
	}
	if replayedGrant.Created {
		t.Fatalf("project model grant replay should not report Created: %+v", replayedGrant)
	}
	grantUpdatedAt := now.Add(10250 * time.Millisecond)
	if _, err := pool.Exec(
		ctx,
		`UPDATE project_model_grants
		 SET max_output_tokens = max_output_tokens - 1, updated_at = $4
		 WHERE org_id = $1 AND project_id = $2 AND id = $3`,
		testOrgID,
		testProjectID,
		grant.ID,
		grantUpdatedAt,
	); err != nil {
		t.Fatalf("update project model grant: %v", err)
	}
	var updatedMaxOutputTokens int
	var storedGrantUpdatedAt time.Time
	if err := pool.QueryRow(ctx, `
		SELECT max_output_tokens, updated_at
		FROM project_model_grants
		WHERE org_id = $1 AND project_id = $2 AND id = $3
	`, testOrgID, testProjectID, grant.ID).Scan(
		&updatedMaxOutputTokens,
		&storedGrantUpdatedAt,
	); err != nil {
		t.Fatalf("load updated project model grant: %v", err)
	}
	if updatedMaxOutputTokens != 63999 || !storedGrantUpdatedAt.Equal(grantUpdatedAt) {
		t.Fatalf(
			"updated project model grant = max %d at %s, want 63999 at %s",
			updatedMaxOutputTokens,
			storedGrantUpdatedAt,
			grantUpdatedAt,
		)
	}
	if _, err := store.Models().CreateProjectModelGrant(ctx, modelstore.CreateProjectModelGrantInput{
		OrgID:                  testOrgID,
		ProjectID:              testProjectID,
		ConfiguredModelID:      configuredModel.ID,
		MaxOutputTokens:        intPtr(48000),
		DefaultMaxOutputTokens: intPtr(24000),
	}); !errors.Is(err, storeerr.ErrConflict) {
		t.Fatalf("conflicting project model grant replay error = %v, want ErrConflict", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO project_model_grants(
			org_id, project_id, configured_model_id,
			max_output_tokens, default_max_output_tokens,
			created_at, updated_at
		)
		VALUES ($1, $2, $3, 4096, 64000, $4, $4)
	`, testOrgID, testProjectID, configuredModel.ID, now.Add(10750*time.Millisecond)); !isSQLCheckViolation(err) {
		t.Fatalf(
			"project model grant with default_max_output_tokens above max_output_tokens error = %v, want check violation",
			err,
		)
	}
	if _, err := store.Models().CreateProjectModelGrant(ctx, modelstore.CreateProjectModelGrantInput{
		OrgID:               testOrgID,
		ProjectID:           testProjectID,
		ConfiguredModelID:   configuredModel.ID,
		ContextWindowTokens: intPtr(300000),
	}); !errors.Is(err, storeerr.ErrInvalidModelProviderConfig) {
		t.Fatalf("over-wide project model grant error = %v, want ErrInvalidModelProviderConfig", err)
	}
	if _, err := store.Models().CreateProjectModelGrant(ctx, modelstore.CreateProjectModelGrantInput{
		OrgID:             testOrgID,
		ProjectID:         testProjectID,
		ConfiguredModelID: configuredModel.ID,
		InputModalities:   []string{"audio"},
	}); !errors.Is(err, storeerr.ErrInvalidModelProviderConfig) {
		t.Fatalf("invalid project model grant modality error = %v, want ErrInvalidModelProviderConfig", err)
	}
	if _, err := store.Models().DeleteConfiguredModel(
		ctx,
		testOrgID,
		configuredModel.ID,
	); !errors.Is(
		err,
		storeerr.ErrConflict,
	) {
		t.Fatalf("archive configured model with active grant error = %v, want ErrConflict", err)
	}
	if _, err := store.Models().DeleteProjectModelGrant(ctx, testOrgID, testProjectID, grant.ID); err != nil {
		t.Fatalf("delete project model grant: %v", err)
	}
	secondGrant, err := store.Models().CreateProjectModelGrant(ctx, modelstore.CreateProjectModelGrantInput{
		OrgID:             testOrgID,
		ProjectID:         testProjectID,
		ConfiguredModelID: configuredModel.ID,
	})
	if err != nil {
		t.Fatalf("create grant after revoke: %v", err)
	}
	if secondGrant.ID == grant.ID {
		t.Fatalf("grant after delete mismatch: old=%+v new=%+v", grant, secondGrant)
	}
	if _, err := store.Models().DeleteModelProviderConfig(
		ctx,
		testOrgID,
		config.ID,
	); !errors.Is(
		err,
		storeerr.ErrConflict,
	) {
		t.Fatalf("archive provider config with active model error = %v, want ErrConflict", err)
	}
	if _, err := store.Models().DeleteProjectModelGrant(ctx, testOrgID, testProjectID, secondGrant.ID); err != nil {
		t.Fatalf("delete second project model grant: %v", err)
	}
	if _, err := store.Models().DeleteConfiguredModel(ctx, testOrgID, configuredModel.ID); err != nil {
		t.Fatalf("archive configured model after grants revoked: %v", err)
	}
	if _, err := store.Models().DeleteModelProviderConfig(ctx, testOrgID, config.ID); err != nil {
		t.Fatalf("archive provider config: %v", err)
	}
	modelsAfterArchivePage, err := store.Models().ListConfiguredModels(
		ctx,
		modelstore.ListConfiguredModelsInput{OrgID: testOrgID, ProviderConfigID: config.ID, Limit: 10},
	)
	if err != nil {
		t.Fatalf("list configured models after parent archive: %v", err)
	}
	modelsAfterArchive := modelsAfterArchivePage.Models
	if len(modelsAfterArchive) != 0 {
		t.Fatalf("models under archived provider config = %+v, want none", modelsAfterArchive)
	}
	if _, err := store.Models().GetConfiguredModel(ctx, testOrgID, configuredModel.ID); err == nil {
		t.Fatal("configured model under archived provider config should not resolve")
	}
}

func TestDeleteConfiguredModelAllowsHistoricalAgentConfigReferences(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool)
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)

	sourceYAML := `
instruction: test
model:
  provider_config: openai-prod
  name: archive-test
`
	config := mustCreateAgentConfigFromYAML(t, ctx, store, "archived-model", sourceYAML, now)
	configuredModel, err := store.Models().GetConfiguredModel(ctx, testOrgID, config.ConfiguredModelID)
	if err != nil {
		t.Fatalf("load configured model before archive: %v", err)
	}
	revisionID := configuredModel.CurrentRevisionID
	grant, err := store.Models().GetActiveProjectModelGrantForConfiguredModel(
		ctx,
		testOrgID,
		testProjectID,
		config.ConfiguredModelID,
	)
	if err != nil {
		t.Fatalf("load active model grant: %v", err)
	}
	if _, err := store.Models().DeleteProjectModelGrant(ctx, testOrgID, testProjectID, grant.ID); err != nil {
		t.Fatalf("revoke model grant: %v", err)
	}
	if _, err := store.Models().DeleteConfiguredModel(
		ctx,
		testOrgID,
		config.ConfiguredModelID,
	); err != nil {
		t.Fatalf("archive configured model with historical agent config reference: %v", err)
	}
	if _, found, err := store.Execution().GetAgentConfig(ctx, testProjectID, config.ID); err != nil || !found {
		t.Fatalf("historical agent config after model archive found=%t err=%v", found, err)
	}
	if _, err := store.Models().GetConfiguredModelRevisionDisplay(ctx, testOrgID, revisionID); err != nil {
		t.Fatalf("historical configured model revision display after archive: %v", err)
	}
	if _, err := store.Models().GetConfiguredModelRevisionForUse(ctx, testOrgID, revisionID); !storeerr.IsNotFound(err) {
		t.Fatalf("configured model revision for use after archive error = %v, want not found", err)
	}
}

func TestPatchConfiguredModelRejectsInvalidStoredName(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	admin := createSecretTestUser(t, ctx, store, "Invalid Model Admin", "admin")
	credential, _, err := store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID:     testOrgID,
		OwnerKind: secretstore.SecretOwnerOrg,
		Name:      "stored-model-credential",
		Material:  secrets.GenericMaterial{Value: "sk-test"},
		Actor:     userPrincipal(admin.ID),
	})
	if err != nil {
		t.Fatalf("create credential: %v", err)
	}
	provider, err := store.Models().CreateModelProviderConfig(ctx, modelstore.CreateModelProviderConfigInput{
		OrgID:              testOrgID,
		Name:               "stored-model-provider",
		APIFormat:          modelprotocol.APIFormatOpenAIResponses,
		BaseURL:            "https://api.openai.com/v1",
		CredentialSecretID: credential.ID,
	})
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	model, err := store.Models().CreateConfiguredModel(ctx, modelstore.CreateConfiguredModelInput{
		OrgID:                 testOrgID,
		ModelProviderConfigID: provider.ID,
		Name:                  "stored-model",
		ProviderModelSlug:     "model-v1",
		ContextWindowTokens:   128000,
		MaxOutputTokens:       8192,
	})
	if err != nil {
		t.Fatalf("create configured model: %v", err)
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE configured_models DROP CONSTRAINT configured_models_name_policy`); err != nil {
		t.Fatalf("drop configured model name constraint: %v", err)
	}
	const invalidStoredName = " invalid model "
	if _, err := pool.Exec(ctx, `UPDATE configured_models SET name = $1 WHERE id = $2`, invalidStoredName, model.ID); err != nil {
		t.Fatalf("seed invalid configured model name: %v", err)
	}

	providerModelSlug := "model-v2"
	if _, err := store.Models().PatchConfiguredModel(ctx, modelstore.PatchConfiguredModelInput{
		OrgID:                 testOrgID,
		ModelProviderConfigID: provider.ID,
		ID:                    model.ID,
		ProviderModelSlug:     &providerModelSlug,
	}); !errors.Is(err, storeerr.ErrInvalidRequest) {
		t.Fatalf("patch with invalid stored configured model name error = %v, want invalid request", err)
	}
	repairedName := "Repaired model"
	repaired, err := store.Models().PatchConfiguredModel(ctx, modelstore.PatchConfiguredModelInput{
		OrgID:                 testOrgID,
		ModelProviderConfigID: provider.ID,
		ID:                    model.ID,
		Name:                  &repairedName,
	})
	if err != nil {
		t.Fatalf("repair configured model name: %v", err)
	}
	if repaired.Name != repairedName || repaired.ProviderModelSlug != "model-v1" {
		t.Fatalf("repaired configured model = %+v", repaired)
	}
}

func TestDeleteConfiguredModelDoesNotRequireActiveProviderConfig(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool)
	now := time.Date(2026, 6, 24, 12, 0, 0, 0, time.UTC)

	config := mustCreateAgentConfigFromYAML(t, ctx, store, "archive-model-parent-archived", `
instruction: test
model:
  provider_config: openai-prod
  name: archive-parent-test
`, now)
	configuredModel, err := store.Models().GetConfiguredModel(ctx, testOrgID, config.ConfiguredModelID)
	if err != nil {
		t.Fatalf("load configured model before archive: %v", err)
	}
	grant, err := store.Models().GetActiveProjectModelGrantForConfiguredModel(
		ctx,
		testOrgID,
		testProjectID,
		config.ConfiguredModelID,
	)
	if err != nil {
		t.Fatalf("load active model grant: %v", err)
	}
	if _, err := store.Models().DeleteProjectModelGrant(ctx, testOrgID, testProjectID, grant.ID); err != nil {
		t.Fatalf("revoke model grant: %v", err)
	}
	if _, err := pool.Exec(
		ctx,
		`UPDATE model_provider_configs SET deleted_at = $1, updated_at = $1 WHERE org_id = $2 AND id = $3`,
		now.Add(2*time.Second),
		testOrgID,
		configuredModel.ModelProviderConfigID,
	); err != nil {
		t.Fatalf("force archive parent provider config: %v", err)
	}
	archived, err := store.Models().DeleteConfiguredModel(ctx, testOrgID, config.ConfiguredModelID)
	if err != nil {
		t.Fatalf("archive configured model under archived provider config: %v", err)
	}
	if archived.DeletedAt == nil {
		t.Fatalf("configured model deleted_at = nil")
	}
}

func TestConfiguredModelUpdateSerializesWithAgentConfigCreation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	admin := createSecretTestUser(t, ctx, store, "Provider Config Lock Admin", "admin")

	credential, _, err := store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID:     testOrgID,
		OwnerKind: secretstore.SecretOwnerOrg,
		Name:      "provider-lock-key",
		Material:  secrets.GenericMaterial{Value: "sk-provider-lock"},
		Actor:     userPrincipal(admin.ID),
	})
	if err != nil {
		t.Fatalf("create org credential: %v", err)
	}
	config, err := store.Models().CreateModelProviderConfig(ctx, modelstore.CreateModelProviderConfigInput{
		OrgID:              testOrgID,
		Name:               "openai-lock",
		APIFormat:          modelprotocol.APIFormatOpenAIResponses,
		BaseURL:            "https://api.openai.com/v1",
		CredentialSecretID: credential.ID,
	})
	if err != nil {
		t.Fatalf("create provider config: %v", err)
	}
	configuredModel, err := store.Models().CreateConfiguredModel(ctx, modelstore.CreateConfiguredModelInput{
		OrgID:                  testOrgID,
		ModelProviderConfigID:  config.ID,
		Name:                   "gpt-lock",
		ProviderModelSlug:      "gpt-lock",
		ContextWindowTokens:    128000,
		MaxOutputTokens:        8192,
		DefaultMaxOutputTokens: intPtr(4096),
		SupportsTools:          boolPtr(true),
	})
	if err != nil {
		t.Fatalf("create configured model: %v", err)
	}
	if _, err := store.Models().CreateProjectModelGrant(ctx, modelstore.CreateProjectModelGrantInput{
		OrgID:             testOrgID,
		ProjectID:         testProjectID,
		ConfiguredModelID: configuredModel.ID,
	}); err != nil {
		t.Fatalf("grant lock-test configured model: %v", err)
	}

	source := `instruction: Test configured model lock.
model:
  provider_config: openai-lock
  name: gpt-lock
`
	compiled, err := agentconfig.Compile(agentconfig.SourceFormatYAML, []byte(source), agentconfig.CompileOptions{
		ResolveModelSelection: func(providerConfigName string, configuredModelName string) (agentconfig.ResolvedModelSelection, error) {
			return resolvedTestModelSelection(configuredModel), nil
		},
	})
	if err != nil {
		t.Fatalf("compile config: %v", err)
	}
	control, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire control connection: %v", err)
	}
	defer control.Release()
	if _, err := control.Exec(ctx, `SELECT pg_advisory_lock(742001, 2)`); err != nil {
		t.Fatalf("acquire agent config insert release lock: %v", err)
	}
	defer func() { _, _ = control.Exec(context.Background(), `SELECT pg_advisory_unlock(742001, 2)`) }()
	if _, err := pool.Exec(ctx, `
CREATE OR REPLACE FUNCTION test_pause_agent_config_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  PERFORM pg_advisory_lock(742001, 1);
  PERFORM pg_advisory_lock(742001, 2);
  PERFORM pg_advisory_unlock(742001, 2);
  PERFORM pg_advisory_unlock(742001, 1);
  RETURN NEW;
END;
$$;

CREATE TRIGGER test_pause_agent_config_insert
BEFORE INSERT ON agent_configs
FOR EACH ROW EXECUTE FUNCTION test_pause_agent_config_insert();
`); err != nil {
		t.Fatalf("install pause trigger: %v", err)
	}
	defer func() {
		_, _ = pool.Exec(context.Background(), `
DROP TRIGGER IF EXISTS test_pause_agent_config_insert ON agent_configs;
DROP FUNCTION IF EXISTS test_pause_agent_config_insert();
`)
	}()

	createDone := make(chan error, 1)
	go func() {
		_, createErr := store.Execution().CreateAgentConfig(ctx, executionstore.CreateAgentConfigInput{
			ProjectID:               testProjectID,
			Definition:              json.RawMessage(compiled.CanonicalJSON),
			Source:                  source,
			SourceFormat:            string(agentconfig.SourceFormatYAML),
			ConfiguredModelID:       configuredModel.ID,
			CompiledDefinition:      json.RawMessage(compiled.CanonicalJSON),
			CompilerVersion:         agentconfig.CompilerVersion,
			EffectiveDefinitionHash: compiled.Hash,
		})
		createDone <- createErr
	}()

	integrationdb.WaitForGrantedAdvisoryLock(t, ctx, pool, 742001, 1)

	updateDone := make(chan error, 1)
	go func() {
		updateCtx, cancelUpdate := context.WithTimeout(ctx, 5*time.Second)
		defer cancelUpdate()
		updatedProviderModelSlug := "gpt-lock-v2"
		updatedContextWindow := 200000
		updatedSupportsTools := true
		_, updateErr := store.Models().PatchConfiguredModel(updateCtx, modelstore.PatchConfiguredModelInput{
			OrgID:                 testOrgID,
			ModelProviderConfigID: config.ID,
			ID:                    configuredModel.ID,
			ProviderModelSlug:     &updatedProviderModelSlug,
			ContextWindowTokens:   &updatedContextWindow,
			MaxOutputTokens:       intPtr(16384),
			SupportsTools:         &updatedSupportsTools,
		})
		updateDone <- updateErr
	}()

	integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "LockConfiguredModelForMutation", 1)
	select {
	case updateErr := <-updateDone:
		t.Fatalf("configured model update completed before agent config commit: %v", updateErr)
	default:
	}
	if _, err := control.Exec(ctx, `SELECT pg_advisory_unlock(742001, 2)`); err != nil {
		t.Fatalf("release agent config insert trigger: %v", err)
	}

	select {
	case createErr := <-createDone:
		if createErr != nil {
			t.Fatalf("create agent config: %v", createErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for agent config creation")
	}
	select {
	case updateErr := <-updateDone:
		if updateErr != nil {
			t.Fatalf("configured model update after agent config commit: %v", updateErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for configured model update")
	}
}

func TestCreateAgentConfigRejectsStaleToolRequirementAfterGrantChanges(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	admin := createSecretTestUser(t, ctx, store, "Provider Config Stale Grant Admin", "admin")

	credential, _, err := store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID:     testOrgID,
		OwnerKind: secretstore.SecretOwnerOrg,
		Name:      "provider-stale-grant-key",
		Material:  secrets.GenericMaterial{Value: "sk-provider-stale-grant"},
		Actor:     userPrincipal(admin.ID),
	})
	if err != nil {
		t.Fatalf("create org credential: %v", err)
	}
	config, err := store.Models().CreateModelProviderConfig(ctx, modelstore.CreateModelProviderConfigInput{
		OrgID:              testOrgID,
		Name:               "openai-stale-grant",
		APIFormat:          modelprotocol.APIFormatOpenAIResponses,
		BaseURL:            "https://api.openai.com/v1",
		CredentialSecretID: credential.ID,
	})
	if err != nil {
		t.Fatalf("create provider config: %v", err)
	}
	configuredModel, err := store.Models().CreateConfiguredModel(ctx, modelstore.CreateConfiguredModelInput{
		OrgID:                  testOrgID,
		ModelProviderConfigID:  config.ID,
		Name:                   "gpt-stale-grant",
		ProviderModelSlug:      "gpt-stale-grant",
		ContextWindowTokens:    128000,
		MaxOutputTokens:        8192,
		DefaultMaxOutputTokens: intPtr(4096),
		SupportsTools:          boolPtr(true),
	})
	if err != nil {
		t.Fatalf("create configured model: %v", err)
	}
	grant, err := store.Models().CreateProjectModelGrant(ctx, modelstore.CreateProjectModelGrantInput{
		OrgID:             testOrgID,
		ProjectID:         testProjectID,
		ConfiguredModelID: configuredModel.ID,
		SupportsTools:     boolPtr(true),
	})
	if err != nil {
		t.Fatalf("grant configured model with tools: %v", err)
	}

	source := `instruction: Test stale project grant tools.
model:
  provider_config: openai-stale-grant
  name: gpt-stale-grant
tools:
  run_command:
    permission:
      mode: always_allow
      parameters: {}
`
	compiled, err := agentconfig.Compile(agentconfig.SourceFormatYAML, []byte(source), agentconfig.CompileOptions{
		ResolveModelSelection: func(providerConfigName string, configuredModelName string) (agentconfig.ResolvedModelSelection, error) {
			return resolvedTestModelSelection(configuredModel), nil
		},
	})
	if err != nil {
		t.Fatalf("compile tool-using config: %v", err)
	}
	if _, err := store.Models().DeleteProjectModelGrant(ctx, testOrgID, testProjectID, grant.ID); err != nil {
		t.Fatalf("revoke tool grant: %v", err)
	}
	if _, err := store.Models().CreateProjectModelGrant(ctx, modelstore.CreateProjectModelGrantInput{
		OrgID:             testOrgID,
		ProjectID:         testProjectID,
		ConfiguredModelID: configuredModel.ID,
		SupportsTools:     boolPtr(false),
	}); err != nil {
		t.Fatalf("grant configured model without tools: %v", err)
	}

	_, err = store.Execution().CreateAgentConfig(ctx, executionstore.CreateAgentConfigInput{
		ProjectID:               testProjectID,
		Definition:              json.RawMessage(compiled.CanonicalJSON),
		Source:                  source,
		SourceFormat:            string(agentconfig.SourceFormatYAML),
		ConfiguredModelID:       configuredModel.ID,
		CompiledDefinition:      json.RawMessage(compiled.CanonicalJSON),
		CompilerVersion:         agentconfig.CompilerVersion,
		EffectiveDefinitionHash: compiled.Hash,
	})
	if !errors.Is(err, storeerr.ErrInvalidModelProviderConfig) {
		t.Fatalf("create stale tool-using agent config error = %v, want ErrInvalidModelProviderConfig", err)
	}
}

func TestPatchConfiguredModelMergesAgainstLockedCurrentRevision(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	admin := createSecretTestUser(t, ctx, store, "Provider Config Patch Admin", "admin")

	credential, _, err := store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID:     testOrgID,
		OwnerKind: secretstore.SecretOwnerOrg,
		Name:      "provider-patch-key",
		Material:  secrets.GenericMaterial{Value: "sk-provider-patch"},
		Actor:     userPrincipal(admin.ID),
	})
	if err != nil {
		t.Fatalf("create org credential: %v", err)
	}
	config, err := store.Models().CreateModelProviderConfig(ctx, modelstore.CreateModelProviderConfigInput{
		OrgID:              testOrgID,
		Name:               "openai-patch",
		APIFormat:          modelprotocol.APIFormatOpenAIResponses,
		BaseURL:            "https://api.openai.com/v1",
		CredentialSecretID: credential.ID,
	})
	if err != nil {
		t.Fatalf("create provider config: %v", err)
	}
	configuredModel, err := store.Models().CreateConfiguredModel(ctx, modelstore.CreateConfiguredModelInput{
		OrgID:                  testOrgID,
		ModelProviderConfigID:  config.ID,
		Name:                   "gpt-patch",
		ProviderModelSlug:      "gpt-patch-v1",
		ContextWindowTokens:    128000,
		MaxOutputTokens:        8192,
		DefaultMaxOutputTokens: intPtr(4096),
		SupportsTools:          boolPtr(true),
	})
	if err != nil {
		t.Fatalf("create configured model: %v", err)
	}

	control, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire control connection: %v", err)
	}
	defer control.Release()
	if _, err := control.Exec(ctx, `SELECT pg_advisory_lock(742002, 2)`); err != nil {
		t.Fatalf("acquire configured model patch release lock: %v", err)
	}
	defer func() { _, _ = control.Exec(context.Background(), `SELECT pg_advisory_unlock(742002, 2)`) }()
	if _, err := pool.Exec(ctx, `
CREATE OR REPLACE FUNCTION test_pause_configured_model_revision_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF pg_try_advisory_lock(742002, 1) THEN
    PERFORM pg_advisory_lock(742002, 2);
    PERFORM pg_advisory_unlock(742002, 2);
    PERFORM pg_advisory_unlock(742002, 1);
  END IF;
  RETURN NEW;
END;
$$;

CREATE TRIGGER test_pause_configured_model_revision_insert
BEFORE INSERT ON configured_model_revisions
FOR EACH ROW EXECUTE FUNCTION test_pause_configured_model_revision_insert();
`); err != nil {
		t.Fatalf("install configured model revision pause trigger: %v", err)
	}
	defer func() {
		_, _ = pool.Exec(context.Background(), `
DROP TRIGGER IF EXISTS test_pause_configured_model_revision_insert ON configured_model_revisions;
DROP FUNCTION IF EXISTS test_pause_configured_model_revision_insert();
`)
	}()

	maxOutput := 16384
	firstDone := make(chan error, 1)
	go func() {
		_, patchErr := store.Models().PatchConfiguredModel(ctx, modelstore.PatchConfiguredModelInput{
			OrgID:                 testOrgID,
			ModelProviderConfigID: config.ID,
			ID:                    configuredModel.ID,
			MaxOutputTokens:       &maxOutput,
		})
		firstDone <- patchErr
	}()
	integrationdb.WaitForGrantedAdvisoryLock(t, ctx, pool, 742002, 1)

	providerModelSlug := "gpt-patch-v2"
	secondDone := make(chan error, 1)
	go func() {
		_, patchErr := store.Models().PatchConfiguredModel(ctx, modelstore.PatchConfiguredModelInput{
			OrgID:                 testOrgID,
			ModelProviderConfigID: config.ID,
			ID:                    configuredModel.ID,
			ProviderModelSlug:     &providerModelSlug,
		})
		secondDone <- patchErr
	}()

	integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "LockConfiguredModelForMutation", 1)
	select {
	case secondErr := <-secondDone:
		t.Fatalf("second patch completed before first patch released row lock: %v", secondErr)
	default:
	}
	if _, err := control.Exec(ctx, `SELECT pg_advisory_unlock(742002, 2)`); err != nil {
		t.Fatalf("release configured model revision insert trigger: %v", err)
	}

	select {
	case firstErr := <-firstDone:
		if firstErr != nil {
			t.Fatalf("first patch configured model: %v", firstErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for first configured model patch")
	}
	select {
	case secondErr := <-secondDone:
		if secondErr != nil {
			t.Fatalf("second patch configured model: %v", secondErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for second configured model patch")
	}

	current, err := store.Models().GetConfiguredModel(ctx, testOrgID, configuredModel.ID)
	if err != nil {
		t.Fatalf("load patched configured model: %v", err)
	}
	if current.ProviderModelSlug != "gpt-patch-v2" || current.MaxOutputTokens != 16384 {
		t.Fatalf("concurrent patches did not merge against locked current revision: %+v", current)
	}
}

func TestPatchModelProviderConfigMergesAgainstLockedCurrentConfig(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	admin := createSecretTestUser(t, ctx, store, "Provider Config Patch Race Admin", "admin")

	credential, _, err := store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID:     testOrgID,
		OwnerKind: secretstore.SecretOwnerOrg,
		Name:      "provider-config-patch-race-key",
		Material:  secrets.GenericMaterial{Value: "sk-provider-config-patch-race"},
		Actor:     userPrincipal(admin.ID),
	})
	if err != nil {
		t.Fatalf("create org credential: %v", err)
	}
	config, err := store.Models().CreateModelProviderConfig(ctx, modelstore.CreateModelProviderConfigInput{
		OrgID:              testOrgID,
		Name:               "openai-provider-config-patch-race",
		APIFormat:          modelprotocol.APIFormatOpenAIResponses,
		BaseURL:            "https://api.openai.com/v1",
		CredentialSecretID: credential.ID,
	})
	if err != nil {
		t.Fatalf("create provider config: %v", err)
	}

	control, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire control connection: %v", err)
	}
	defer control.Release()
	if _, err := control.Exec(ctx, `SELECT pg_advisory_lock(742004, 2)`); err != nil {
		t.Fatalf("acquire provider config patch release lock: %v", err)
	}
	defer func() { _, _ = control.Exec(context.Background(), `SELECT pg_advisory_unlock(742004, 2)`) }()
	if _, err := pool.Exec(ctx, `
CREATE OR REPLACE FUNCTION test_pause_model_provider_config_update()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF OLD.name = 'openai-provider-config-patch-race' AND pg_try_advisory_lock(742004, 1) THEN
    PERFORM pg_advisory_lock(742004, 2);
    PERFORM pg_advisory_unlock(742004, 2);
    PERFORM pg_advisory_unlock(742004, 1);
  END IF;
  RETURN NEW;
END;
$$;

CREATE TRIGGER test_pause_model_provider_config_update
BEFORE UPDATE ON model_provider_configs
FOR EACH ROW EXECUTE FUNCTION test_pause_model_provider_config_update();
`); err != nil {
		t.Fatalf("install provider config update pause trigger: %v", err)
	}
	defer func() {
		_, _ = pool.Exec(context.Background(), `
DROP TRIGGER IF EXISTS test_pause_model_provider_config_update ON model_provider_configs;
DROP FUNCTION IF EXISTS test_pause_model_provider_config_update();
`)
	}()

	baseURL := "https://proxy.example.test/v1"
	firstDone := make(chan error, 1)
	go func() {
		_, patchErr := store.Models().PatchModelProviderConfig(ctx, modelstore.PatchModelProviderConfigInput{
			OrgID:   testOrgID,
			ID:      config.ID,
			BaseURL: &baseURL,
		})
		firstDone <- patchErr
	}()
	integrationdb.WaitForGrantedAdvisoryLock(t, ctx, pool, 742004, 1)

	endpointPath := "/custom-responses"
	secondDone := make(chan error, 1)
	go func() {
		_, patchErr := store.Models().PatchModelProviderConfig(ctx, modelstore.PatchModelProviderConfigInput{
			OrgID:        testOrgID,
			ID:           config.ID,
			EndpointPath: &endpointPath,
		})
		secondDone <- patchErr
	}()

	integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "LockModelProviderConfigForMutation", 1)
	select {
	case secondErr := <-secondDone:
		t.Fatalf("second provider config patch completed before first patch released row lock: %v", secondErr)
	default:
	}
	if _, err := control.Exec(ctx, `SELECT pg_advisory_unlock(742004, 2)`); err != nil {
		t.Fatalf("release provider config update trigger: %v", err)
	}

	select {
	case firstErr := <-firstDone:
		if firstErr != nil {
			t.Fatalf("first provider config patch: %v", firstErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for first provider config patch")
	}
	select {
	case secondErr := <-secondDone:
		if secondErr != nil {
			t.Fatalf("second provider config patch: %v", secondErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for second provider config patch")
	}

	current, err := store.Models().GetModelProviderConfig(ctx, testOrgID, config.ID)
	if err != nil {
		t.Fatalf("load patched provider config: %v", err)
	}
	if current.BaseURL != baseURL || current.EndpointPath != endpointPath {
		t.Fatalf("concurrent provider config patches did not merge against locked current config: %+v", current)
	}
}

func TestDeleteConfiguredModelUsesLockedCurrentRevisionAfterConcurrentPatch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	admin := createSecretTestUser(t, ctx, store, "Provider Config Archive Race Admin", "admin")

	credential, _, err := store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID:     testOrgID,
		OwnerKind: secretstore.SecretOwnerOrg,
		Name:      "provider-archive-race-key",
		Material:  secrets.GenericMaterial{Value: "sk-provider-archive-race"},
		Actor:     userPrincipal(admin.ID),
	})
	if err != nil {
		t.Fatalf("create org credential: %v", err)
	}
	config, err := store.Models().CreateModelProviderConfig(ctx, modelstore.CreateModelProviderConfigInput{
		OrgID:              testOrgID,
		Name:               "openai-archive-race",
		APIFormat:          modelprotocol.APIFormatOpenAIResponses,
		BaseURL:            "https://api.openai.com/v1",
		CredentialSecretID: credential.ID,
	})
	if err != nil {
		t.Fatalf("create provider config: %v", err)
	}
	configuredModel, err := store.Models().CreateConfiguredModel(ctx, modelstore.CreateConfiguredModelInput{
		OrgID:                  testOrgID,
		ModelProviderConfigID:  config.ID,
		Name:                   "gpt-archive-race",
		ProviderModelSlug:      "gpt-archive-race-v1",
		ContextWindowTokens:    128000,
		MaxOutputTokens:        8192,
		DefaultMaxOutputTokens: intPtr(4096),
		SupportsTools:          boolPtr(true),
	})
	if err != nil {
		t.Fatalf("create configured model: %v", err)
	}

	control, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire control connection: %v", err)
	}
	defer control.Release()
	if _, err := control.Exec(ctx, `SELECT pg_advisory_lock(742003, 2)`); err != nil {
		t.Fatalf("acquire configured model archive release lock: %v", err)
	}
	defer func() { _, _ = control.Exec(context.Background(), `SELECT pg_advisory_unlock(742003, 2)`) }()
	if _, err := pool.Exec(ctx, `
CREATE OR REPLACE FUNCTION test_pause_archive_race_revision_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF pg_try_advisory_lock(742003, 1) THEN
    PERFORM pg_advisory_lock(742003, 2);
    PERFORM pg_advisory_unlock(742003, 2);
    PERFORM pg_advisory_unlock(742003, 1);
  END IF;
  RETURN NEW;
END;
$$;

CREATE TRIGGER test_pause_archive_race_revision_insert
BEFORE INSERT ON configured_model_revisions
FOR EACH ROW EXECUTE FUNCTION test_pause_archive_race_revision_insert();
`); err != nil {
		t.Fatalf("install configured model archive race trigger: %v", err)
	}
	defer func() {
		_, _ = pool.Exec(context.Background(), `
DROP TRIGGER IF EXISTS test_pause_archive_race_revision_insert ON configured_model_revisions;
DROP FUNCTION IF EXISTS test_pause_archive_race_revision_insert();
`)
	}()

	maxOutput := 16384
	patchDone := make(chan error, 1)
	go func() {
		_, patchErr := store.Models().PatchConfiguredModel(ctx, modelstore.PatchConfiguredModelInput{
			OrgID:                 testOrgID,
			ModelProviderConfigID: config.ID,
			ID:                    configuredModel.ID,
			MaxOutputTokens:       &maxOutput,
		})
		patchDone <- patchErr
	}()
	integrationdb.WaitForGrantedAdvisoryLock(t, ctx, pool, 742003, 1)

	archiveDone := make(chan struct {
		record modelstore.ConfiguredModelRecord
		err    error
	}, 1)
	go func() {
		record, archiveErr := store.Models().DeleteConfiguredModel(ctx, testOrgID, configuredModel.ID)
		archiveDone <- struct {
			record modelstore.ConfiguredModelRecord
			err    error
		}{record: record, err: archiveErr}
	}()

	integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "LockConfiguredModelForDelete", 1)
	select {
	case result := <-archiveDone:
		t.Fatalf("archive completed before patch released row lock: record=%+v err=%v", result.record, result.err)
	default:
	}
	if _, err := control.Exec(ctx, `SELECT pg_advisory_unlock(742003, 2)`); err != nil {
		t.Fatalf("release configured model archive trigger: %v", err)
	}

	select {
	case patchErr := <-patchDone:
		if patchErr != nil {
			t.Fatalf("patch configured model before archive: %v", patchErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for configured model patch")
	}
	select {
	case result := <-archiveDone:
		if result.err != nil {
			t.Fatalf("archive configured model after concurrent patch: %v", result.err)
		}
		if result.record.DeletedAt == nil || result.record.MaxOutputTokens != 16384 {
			t.Fatalf("archive did not use patched current revision facts: %+v", result.record)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for configured model archive")
	}
}

func TestCreateAgentConfigUsesConfiguredModelAliasAfterRevisionUpdate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	admin := createSecretTestUser(t, ctx, store, "Provider Config Stale Admin", "admin")

	credential, _, err := store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID:     testOrgID,
		OwnerKind: secretstore.SecretOwnerOrg,
		Name:      "provider-stale-key",
		Material:  secrets.GenericMaterial{Value: "sk-provider-stale"},
		Actor:     userPrincipal(admin.ID),
	})
	if err != nil {
		t.Fatalf("create org credential: %v", err)
	}
	config, err := store.Models().CreateModelProviderConfig(ctx, modelstore.CreateModelProviderConfigInput{
		OrgID:              testOrgID,
		Name:               "openai-stale",
		APIFormat:          modelprotocol.APIFormatOpenAIResponses,
		BaseURL:            "https://api.openai.com/v1",
		CredentialSecretID: credential.ID,
	})
	if err != nil {
		t.Fatalf("create provider config: %v", err)
	}
	configuredModel, err := store.Models().CreateConfiguredModel(ctx, modelstore.CreateConfiguredModelInput{
		OrgID:                  testOrgID,
		ModelProviderConfigID:  config.ID,
		Name:                   "gpt-stale",
		ProviderModelSlug:      "gpt-stale-v1",
		ContextWindowTokens:    128000,
		MaxOutputTokens:        8192,
		DefaultMaxOutputTokens: intPtr(4096),
		SupportsTools:          boolPtr(true),
	})
	if err != nil {
		t.Fatalf("create configured model: %v", err)
	}
	if _, err := store.Models().CreateProjectModelGrant(ctx, modelstore.CreateProjectModelGrantInput{
		OrgID:             testOrgID,
		ProjectID:         testProjectID,
		ConfiguredModelID: configuredModel.ID,
	}); err != nil {
		t.Fatalf("grant stale-test configured model: %v", err)
	}

	source := `instruction: Test configured model alias after revision update.
model:
  provider_config: openai-stale
  name: gpt-stale
`
	compiled, err := agentconfig.Compile(agentconfig.SourceFormatYAML, []byte(source), agentconfig.CompileOptions{
		ResolveModelSelection: func(providerConfigName string, configuredModelName string) (agentconfig.ResolvedModelSelection, error) {
			return resolvedTestModelSelection(configuredModel), nil
		},
	})
	if err != nil {
		t.Fatalf("compile config: %v", err)
	}
	staleProviderModelSlug := "gpt-stale-v2"
	staleContextWindow := 200000
	staleSupportsTools := true
	updated, err := store.Models().PatchConfiguredModel(ctx, modelstore.PatchConfiguredModelInput{
		OrgID:                  testOrgID,
		ModelProviderConfigID:  config.ID,
		ID:                     configuredModel.ID,
		ProviderModelSlug:      &staleProviderModelSlug,
		ContextWindowTokens:    &staleContextWindow,
		MaxOutputTokens:        intPtr(16384),
		DefaultMaxOutputTokens: nullableInt(8192),
		SupportsTools:          &staleSupportsTools,
	})
	if err != nil {
		t.Fatalf("update configured model: %v", err)
	}
	if updated.CurrentRevisionID == configuredModel.CurrentRevisionID {
		t.Fatal("configured model update did not create a new revision")
	}

	agentConfig, err := store.Execution().CreateAgentConfig(ctx, executionstore.CreateAgentConfigInput{
		ProjectID:               testProjectID,
		Definition:              json.RawMessage(compiled.CanonicalJSON),
		Source:                  source,
		SourceFormat:            string(agentconfig.SourceFormatYAML),
		ConfiguredModelID:       configuredModel.ID,
		CompiledDefinition:      json.RawMessage(compiled.CanonicalJSON),
		CompilerVersion:         agentconfig.CompilerVersion,
		EffectiveDefinitionHash: compiled.Hash,
	})
	if err != nil {
		t.Fatalf("create agent config after configured model revision update: %v", err)
	}
	if agentConfig.ConfiguredModelID != configuredModel.ID {
		t.Fatalf("agent config configured model id = %s, want %s", agentConfig.ConfiguredModelID, configuredModel.ID)
	}
}

func intPtr(value int) *int {
	return &value
}

func nullableInt(value int) patch.NullableInt {
	return patch.NullableInt{Set: true, Value: intPtr(value)}
}

func boolPtr(value bool) *bool {
	return &value
}

func isSQLCheckViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23514"
}

func TestListProjectModelGrantsSearchSortAndEmbeddedModel(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	admin := createSecretTestUser(t, ctx, store, "Model Grant Lister", "admin")

	credential, _, err := store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID:     testOrgID,
		OwnerKind: secretstore.SecretOwnerOrg,
		Name:      "model-grant-list-key",
		Material:  secrets.GenericMaterial{Value: "sk-test-list"},
		Actor:     userPrincipal(admin.ID),
	})
	if err != nil {
		t.Fatalf("create credential: %v", err)
	}
	providerConfig, err := store.Models().CreateModelProviderConfig(ctx, modelstore.CreateModelProviderConfigInput{
		OrgID:              testOrgID,
		Name:               "openai-list",
		APIFormat:          modelprotocol.APIFormatOpenAIResponses,
		BaseURL:            "https://api.openai.com/v1",
		CredentialSecretID: credential.ID,
	})
	if err != nil {
		t.Fatalf("create provider config: %v", err)
	}
	betaModel, err := store.Models().CreateConfiguredModel(ctx, modelstore.CreateConfiguredModelInput{
		OrgID:                 testOrgID,
		ModelProviderConfigID: providerConfig.ID,
		Name:                  "gpt-beta",
		ProviderModelSlug:     "gpt-beta",
		ContextWindowTokens:   128000,
		MaxOutputTokens:       8192,
	})
	if err != nil {
		t.Fatalf("create beta model: %v", err)
	}
	alphaModel, err := store.Models().CreateConfiguredModel(ctx, modelstore.CreateConfiguredModelInput{
		OrgID:                 testOrgID,
		ModelProviderConfigID: providerConfig.ID,
		Name:                  "gpt-alpha",
		ProviderModelSlug:     "gpt-alpha",
		ContextWindowTokens:   128000,
		MaxOutputTokens:       8192,
	})
	if err != nil {
		t.Fatalf("create alpha model: %v", err)
	}
	for i, modelID := range []ID{betaModel.ID, alphaModel.ID} {
		if _, err := store.Models().CreateProjectModelGrant(ctx, modelstore.CreateProjectModelGrantInput{
			OrgID:             testOrgID,
			ProjectID:         testProjectID,
			ConfiguredModelID: modelID,
		}); err != nil {
			t.Fatalf("grant model %d: %v", i, err)
		}
	}

	page, err := store.Models().ListProjectModelGrants(ctx, modelstore.ListProjectModelGrantsInput{
		OrgID: testOrgID, ProjectID: testProjectID, Limit: 10,
		List: listing.Options{SortField: "name"},
	})
	if err != nil {
		t.Fatalf("list model grants: %v", err)
	}
	if len(page.Grants) != 2 || page.HasMore {
		t.Fatalf("model grant list = %+v, want 2 rows without more", page)
	}
	if page.Grants[0].Model.Name != "gpt-alpha" || page.Grants[1].Model.Name != "gpt-beta" {
		t.Fatalf("model grants not sorted by model name: %+v", page.Grants)
	}
	first := page.Grants[0]
	if first.Grant.ConfiguredModelID != alphaModel.ID || first.Model.ID != alphaModel.ID ||
		first.Model.ModelProviderConfigID != providerConfig.ID ||
		first.Model.ProviderConfigName != "openai-list" {
		t.Fatalf("embedded model summary mismatch: %+v", first)
	}

	filtered, err := store.Models().ListProjectModelGrants(ctx, modelstore.ListProjectModelGrantsInput{
		OrgID: testOrgID, ProjectID: testProjectID, Limit: 10,
		List: listing.Options{SortField: "name", NamePattern: "%beta%"},
	})
	if err != nil {
		t.Fatalf("list filtered model grants: %v", err)
	}
	if len(filtered.Grants) != 1 || filtered.Grants[0].Model.Name != "gpt-beta" {
		t.Fatalf("filtered model grants = %+v, want only gpt-beta", filtered.Grants)
	}
}

func TestModelProviderConfigListSupportsServerSideSearchSortAndPagination(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	admin := createSecretTestUser(t, ctx, store, "Provider List Admin", "admin")
	credential, _, err := store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID:     testOrgID,
		OwnerKind: secretstore.SecretOwnerOrg,
		Name:      "provider-list-key",
		Material:  secrets.GenericMaterial{Value: "sk-provider-list"},
		Actor:     userPrincipal(admin.ID),
	})
	if err != nil {
		t.Fatalf("create provider list credential: %v", err)
	}
	for _, name := range []string{"provider-beta", "provider-alpha", "provider-gamma"} {
		if _, err := store.Models().CreateModelProviderConfig(ctx, modelstore.CreateModelProviderConfigInput{
			OrgID:              testOrgID,
			Name:               name,
			APIFormat:          modelprotocol.APIFormatOpenAIResponses,
			BaseURL:            "https://api.openai.com/v1",
			CredentialSecretID: credential.ID,
		}); err != nil {
			t.Fatalf("create provider %q: %v", name, err)
		}
	}

	first, err := store.Models().ListModelProviderConfigs(ctx, modelstore.ListModelProviderConfigsInput{
		OrgID: testOrgID,
		Limit: 2,
		List:  listing.Options{SortField: "name", NamePattern: "provider-%"},
	})
	if err != nil {
		t.Fatalf("list first provider page: %v", err)
	}
	if len(first.Configs) != 2 || !first.HasMore ||
		first.Configs[0].Name != "provider-alpha" || first.Configs[1].Name != "provider-beta" {
		t.Fatalf("first provider page = %+v, want alpha and beta with more", first)
	}
	second, err := store.Models().ListModelProviderConfigs(ctx, modelstore.ListModelProviderConfigsInput{
		OrgID: testOrgID,
		Limit: 2,
		List: listing.Options{
			SortField:   "name",
			NamePattern: "provider-%",
			After:       first.Next,
		},
	})
	if err != nil {
		t.Fatalf("list second provider page: %v", err)
	}
	if len(second.Configs) != 1 || second.HasMore || second.Configs[0].Name != "provider-gamma" {
		t.Fatalf("second provider page = %+v, want gamma without more", second)
	}

	descending, err := store.Models().ListModelProviderConfigs(ctx, modelstore.ListModelProviderConfigsInput{
		OrgID: testOrgID,
		Limit: 10,
		List: listing.Options{
			SortField:   "name",
			SortDesc:    true,
			NamePattern: "provider-%",
		},
	})
	if err != nil {
		t.Fatalf("list providers descending: %v", err)
	}
	if len(descending.Configs) != 3 || descending.Configs[0].Name != "provider-gamma" ||
		descending.Configs[1].Name != "provider-beta" || descending.Configs[2].Name != "provider-alpha" {
		t.Fatalf("descending providers = %+v, want gamma, beta, alpha", descending.Configs)
	}

	filtered, err := store.Models().ListModelProviderConfigs(ctx, modelstore.ListModelProviderConfigsInput{
		OrgID: testOrgID,
		Limit: 10,
		List: listing.Options{
			SortField:   "name",
			SortDesc:    true,
			NamePattern: "%provider-b%",
		},
	})
	if err != nil {
		t.Fatalf("list filtered providers: %v", err)
	}
	if len(filtered.Configs) != 1 || filtered.Configs[0].Name != "provider-beta" {
		t.Fatalf("filtered providers = %+v, want only provider-beta", filtered.Configs)
	}
}

func TestUpdateProjectModelGrantAppliesPatchSemantics(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	admin := createSecretTestUser(t, ctx, store, "Model Grant Updater", "admin")

	credential, _, err := store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID:     testOrgID,
		OwnerKind: secretstore.SecretOwnerOrg,
		Name:      "model-grant-update-key",
		Material:  secrets.GenericMaterial{Value: "sk-test-update"},
		Actor:     userPrincipal(admin.ID),
	})
	if err != nil {
		t.Fatalf("create credential: %v", err)
	}
	providerConfig, err := store.Models().CreateModelProviderConfig(ctx, modelstore.CreateModelProviderConfigInput{
		OrgID:              testOrgID,
		Name:               "openai-grant-update",
		APIFormat:          modelprotocol.APIFormatOpenAIResponses,
		BaseURL:            "https://api.openai.com/v1",
		CredentialSecretID: credential.ID,
	})
	if err != nil {
		t.Fatalf("create provider config: %v", err)
	}
	configuredModel, err := store.Models().CreateConfiguredModel(ctx, modelstore.CreateConfiguredModelInput{
		OrgID:                     testOrgID,
		ModelProviderConfigID:     providerConfig.ID,
		Name:                      "gpt-grant-update",
		ProviderModelSlug:         "gpt-grant-update",
		ContextWindowTokens:       128000,
		MaxOutputTokens:           8192,
		DefaultMaxOutputTokens:    intPtr(4096),
		SupportsReasoning:         true,
		DefaultReasoningEffort:    "medium",
		SupportedReasoningEfforts: []string{"low", "medium", "high"},
		InputModalities:           []string{"text", "image"},
		OutputModalities:          []string{"text"},
	})
	if err != nil {
		t.Fatalf("create configured model: %v", err)
	}
	grant, err := store.Models().CreateProjectModelGrant(ctx, modelstore.CreateProjectModelGrantInput{
		OrgID:                  testOrgID,
		ProjectID:              testProjectID,
		ConfiguredModelID:      configuredModel.ID,
		ContextWindowTokens:    intPtr(64000),
		MaxOutputTokens:        intPtr(4096),
		DefaultMaxOutputTokens: intPtr(2048),
		SupportsTools:          boolPtr(true),
	})
	if err != nil {
		t.Fatalf("create project model grant: %v", err)
	}

	updated, err := store.Models().UpdateProjectModelGrant(ctx, modelstore.UpdateProjectModelGrantInput{
		OrgID:                     testOrgID,
		ProjectID:                 testProjectID,
		ID:                        grant.ID,
		ContextWindowTokens:       patch.NullableInt{Set: true},
		MaxOutputTokens:           nullableInt(2048),
		SupportsTools:             patch.NullableBool{Set: true, Value: boolPtr(false)},
		DefaultReasoningEffort:    strPtrForModelGrantUpdateTest("low"),
		SupportedReasoningEfforts: &[]string{"low", "medium"},
	})
	if err != nil {
		t.Fatalf("update project model grant: %v", err)
	}
	if updated.ContextWindowTokens != nil ||
		updated.MaxOutputTokens == nil || *updated.MaxOutputTokens != 2048 ||
		updated.DefaultMaxOutputTokens == nil || *updated.DefaultMaxOutputTokens != 2048 ||
		updated.SupportsTools == nil || *updated.SupportsTools ||
		updated.DefaultReasoningEffort != "low" ||
		!slices.Equal(updated.SupportedReasoningEfforts, []string{"low", "medium"}) ||
		updated.ConfiguredModelID != configuredModel.ID {
		t.Fatalf("updated project model grant patch mismatch: %+v", updated)
	}
	if !updated.CreatedAt.Equal(grant.CreatedAt) || updated.UpdatedAt.Before(grant.UpdatedAt) {
		t.Fatalf("updated project model grant timestamps mismatch: %+v", updated)
	}

	cleared, err := store.Models().UpdateProjectModelGrant(ctx, modelstore.UpdateProjectModelGrantInput{
		OrgID:                     testOrgID,
		ProjectID:                 testProjectID,
		ID:                        grant.ID,
		SupportsTools:             patch.NullableBool{Set: true},
		DefaultReasoningEffort:    strPtrForModelGrantUpdateTest(""),
		SupportedReasoningEfforts: &[]string{},
	})
	if err != nil {
		t.Fatalf("clear project model grant overrides: %v", err)
	}
	if cleared.SupportsTools != nil || cleared.DefaultReasoningEffort != "" ||
		len(cleared.SupportedReasoningEfforts) != 0 ||
		cleared.MaxOutputTokens == nil || *cleared.MaxOutputTokens != 2048 {
		t.Fatalf("cleared project model grant mismatch: %+v", cleared)
	}

	if _, err := store.Models().UpdateProjectModelGrant(ctx, modelstore.UpdateProjectModelGrantInput{
		OrgID:               testOrgID,
		ProjectID:           testProjectID,
		ID:                  grant.ID,
		ContextWindowTokens: nullableInt(256000),
	}); err == nil || !errors.Is(err, storeerr.ErrInvalidModelProviderConfig) {
		t.Fatalf("widening context window should fail validation, got %v", err)
	}

	if _, err := store.Models().UpdateProjectModelGrant(ctx, modelstore.UpdateProjectModelGrantInput{
		OrgID:           testOrgID,
		ProjectID:       testProjectID,
		ID:              grant.ID,
		InputModalities: &[]string{"audio"},
	}); err == nil || !errors.Is(err, storeerr.ErrInvalidModelProviderConfig) {
		t.Fatalf("non-subset input modalities should fail validation, got %v", err)
	}

	if _, err := store.Models().UpdateProjectModelGrant(ctx, modelstore.UpdateProjectModelGrantInput{
		OrgID:           testOrgID,
		ProjectID:       testProjectID,
		ID:              uuid.New(),
		MaxOutputTokens: nullableInt(1024),
	}); !errors.Is(err, storeerr.ErrNotFound) {
		t.Fatalf("update missing project model grant error = %v, want storeerr.ErrNotFound", err)
	}
}

func strPtrForModelGrantUpdateTest(value string) *string {
	return &value
}
