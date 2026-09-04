package modelstore

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/omnara-ai/omnara/internal/storage/management"
	"math"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func TestCreateModelProviderConfigTxRejectsInvalidNameBeforeDatabaseAccess(t *testing.T) {
	store := &Store{}
	_, err := store.createModelProviderConfigTx(
		context.Background(),
		nil,
		CreateModelProviderConfigInput{
			OrgID:              uuid.New(),
			Name:               "unsafe\u200dname",
			APIFormat:          modelprotocol.APIFormatOpenAIResponses,
			BaseURL:            "https://api.example.com/v1",
			CredentialSecretID: uuid.New(),
		},
	)
	if !errors.Is(err, storeerr.ErrInvalidRequest) {
		t.Fatalf("error = %v, want ErrInvalidRequest", err)
	}
}

func TestModelProviderAPIKeyHeaderRejectsTransportReplayHeaders(t *testing.T) {
	for _, headerName := range []string{
		"Idempotency-Key",
		"idempotency-key",
		"X-Idempotency-Key",
		"x-IDEMPOTENCY-key",
	} {
		t.Run(headerName, func(t *testing.T) {
			_, err := ModelProviderAPIKeyHeaderName(
				json.RawMessage(`{"header_name":"` + headerName + `"}`),
			)
			if !errors.Is(err, storeerr.ErrInvalidModelProviderConfig) {
				t.Fatalf("header %q error = %v, want storeerr.ErrInvalidModelProviderConfig", headerName, err)
			}
		})
	}
}

func TestResolveConfiguredModelOutputLimits(t *testing.T) {
	tests := []struct {
		name        string
		context     int
		max         *int
		defaultMax  *int
		wantMax     int
		wantDefault int
		wantErr     bool
	}{
		{name: "standard defaults", context: 128_000, wantMax: 8_192, wantDefault: 4_096},
		{name: "small window", context: 6_000, wantMax: 3_000, wantDefault: 3_000},
		{
			name:        "explicit overrides",
			context:     128_000,
			max:         intPtrForModelProviderConfigStoreTest(16_000),
			defaultMax:  intPtrForModelProviderConfigStoreTest(6_000),
			wantMax:     16_000,
			wantDefault: 6_000,
		},
		{name: "window too small", context: 1, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			maxOutput, defaultOutput, err := ResolveConfiguredModelOutputLimits(
				test.context,
				test.max,
				test.defaultMax,
			)
			if test.wantErr {
				if !errors.Is(err, storeerr.ErrInvalidModelProviderConfig) {
					t.Fatalf("error = %v, want storeerr.ErrInvalidModelProviderConfig", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolve output limits: %v", err)
			}
			if defaultOutput == nil || maxOutput != test.wantMax || *defaultOutput != test.wantDefault {
				t.Fatalf(
					"resolved output limits = %d/%v, want %d/%d",
					maxOutput,
					defaultOutput,
					test.wantMax,
					test.wantDefault,
				)
			}
		})
	}
}

func TestValidateConfiguredModelOptionsTokenBounds(t *testing.T) {
	if err := validateConfiguredModelOptions(modelprotocol.APIFormatOpenAIResponses, configuredModelOptions{
		ContextWindowTokens:    math.MaxInt32,
		MaxOutputTokens:        math.MaxInt32 - 1,
		DefaultMaxOutputTokens: intPtrForModelProviderConfigStoreTest(math.MaxInt32 - 1),
	}); err != nil {
		t.Fatalf("valid token bounds rejected: %v", err)
	}

	for _, tc := range []struct {
		name            string
		input           configuredModelOptions
		messageContains string
	}{
		{
			name: "positive field below minimum",
			input: configuredModelOptions{
				ContextWindowTokens: 0,
				MaxOutputTokens:     1,
			},
			messageContains: "context_window_tokens",
		},
		{
			name: "int32 overflow",
			input: configuredModelOptions{
				ContextWindowTokens: 100,
				MaxOutputTokens:     math.MaxInt32 + 1,
			},
			messageContains: "max_output_tokens",
		},
		{
			name: "default exceeds max",
			input: configuredModelOptions{
				ContextWindowTokens:    100,
				MaxOutputTokens:        10,
				DefaultMaxOutputTokens: intPtrForModelProviderConfigStoreTest(11),
			},
			messageContains: "default_max_output_tokens",
		},
		{
			name: "max output exhausts context",
			input: configuredModelOptions{
				ContextWindowTokens:    100,
				MaxOutputTokens:        100,
				DefaultMaxOutputTokens: intPtrForModelProviderConfigStoreTest(100),
			},
			messageContains: "max_output_tokens",
		},
		{
			name: "invalid cache retention",
			input: configuredModelOptions{
				ContextWindowTokens:   100,
				MaxOutputTokens:       1,
				DefaultCacheRetention: "future",
			},
			messageContains: "default_cache_retention",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateConfiguredModelOptions(
				modelprotocol.APIFormatOpenAIResponses,
				tc.input,
			)
			if !errors.Is(err, storeerr.ErrInvalidModelProviderConfig) {
				t.Fatalf("error = %v, want storeerr.ErrInvalidModelProviderConfig", err)
			}
			if !strings.Contains(err.Error(), tc.messageContains) {
				t.Fatalf("error = %v, want message containing %q", err, tc.messageContains)
			}
		})
	}
}

func TestModelProviderOpenAIChatCompletionsAndOpenRouterOptions(t *testing.T) {
	if err := validateModelProviderAPIFormat(modelprotocol.APIFormatOpenAIChatCompletions); err != nil {
		t.Fatalf("openai chat completions API format rejected: %v", err)
	}
	if got := DefaultModelProviderEndpointPath(modelprotocol.APIFormatOpenAIChatCompletions); got != "/chat/completions" {
		t.Fatalf("chat completions endpoint path = %q, want /chat/completions", got)
	}
	if got := DefaultModelProviderAuthKind(
		modelprotocol.APIFormatOpenAIChatCompletions,
	); got != ModelProviderAuthKindBearerToken {
		t.Fatalf("chat completions auth kind = %q, want bearer_token", got)
	}
	if err := validateModelProviderAPIVariant(
		modelprotocol.APIFormatOpenAIChatCompletions,
		modelprotocol.APIVariantOpenRouter,
	); err != nil {
		t.Fatalf("openrouter API variant rejected for chat completions: %v", err)
	}
	if err := validateModelProviderAPIVariant(
		modelprotocol.APIFormatOpenAIResponses,
		modelprotocol.APIVariantOpenRouter,
	); err == nil {
		t.Fatal("openrouter API variant accepted for openai responses")
	}

	validOptions := json.RawMessage(
		`{"provider":{"only":["anthropic"],` +
			`"require_parameters":true,"data_collection":"deny",` +
			`"sort":{"by":"latency","partition":"model"},` +
			`"preferred_max_latency":{"p50":350,"p90":900},` +
			`"preferred_min_throughput":25,` +
			`"max_price":{"prompt":"0","completion":0,"request":"0.03","image":0.04,"audio":"0.05"}}}`,
	)
	options, err := ValidateAPIVariantOptions(
		validOptions,
	)
	if err != nil {
		t.Fatalf("validate openrouter API variant options: %v", err)
	}
	if !json.Valid(options) {
		t.Fatalf("unexpected openrouter options: %+v", options)
	}
	if _, err := ValidateAPIVariantOptions(
		json.RawMessage(`{"provider":{"unknown":true,"data_collection":"maybe","max_price":{"video":0.03},"sort":{"partition":"none"}}}`),
	); err != nil {
		t.Fatalf("provider pass-through rejected provider-owned fields: %v", err)
	}

	for _, tc := range []struct {
		name string
		raw  json.RawMessage
	}{
		{
			name: "rejects array",
			raw:  json.RawMessage(`["anthropic"]`),
		},
		{
			name: "rejects null",
			raw:  json.RawMessage(`null`),
		},
		{
			name: "rejects string",
			raw:  json.RawMessage(`"temperature=0"`),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ValidateAPIVariantOptions(tc.raw); err == nil {
				t.Fatal("API variant options unexpectedly accepted")
			}
		})
	}
}

func TestBedrockProviderVariantSupportsAllAPIFormats(t *testing.T) {
	for _, apiFormat := range []modelprotocol.APIFormat{
		modelprotocol.APIFormatOpenAIChatCompletions,
		modelprotocol.APIFormatOpenAIResponses,
		modelprotocol.APIFormatAnthropicMessages,
	} {
		if err := validateModelProviderAPIVariant(apiFormat, modelprotocol.APIVariantBedrock); err != nil {
			t.Fatalf("Bedrock variant rejected for %q: %v", apiFormat, err)
		}
	}
}

func TestAPIVariantOptionsPassThroughConfig(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  json.RawMessage
	}{
		{
			name: "chat completions provider params",
			raw: json.RawMessage(
				`{"temperature":0.2,"top_k":40,"stop_token_ids":[1,2],` +
					`"stream":true,"stream_options":{"include_usage":true},"n":2,` +
					`"max_tokens":16,"response_format":{"type":"json_object"},"reasoning":{"effort":"high"},` +
					`"cache_control":{"type":"ephemeral"}}`,
			),
		},
		{
			name: "core request fields accepted at config write",
			raw: json.RawMessage(
				`{"model":"override","messages":[],"provider":{"only":["anthropic"]},` +
					`"parallel_tool_calls":false,"store":false,"prompt_cache_retention":"24h",` +
					`"reasoning_effort":"high"}`,
			),
		},
		{
			name: "responses provider params",
			raw: json.RawMessage(
				`{"stream":true,"stream_options":{"include_usage":true},` +
					`"reasoning_effort":"high","cache_control":{"type":"ephemeral"}}`,
			),
		},
		{
			name: "anthropic provider params",
			raw: json.RawMessage(
				`{"stream":true,"thinking":{"type":"enabled","budget_tokens":1024},` +
					`"metadata":{"user_id":"u"},"cache_control":{"type":"ephemeral"}}`,
			),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ValidateAPIVariantOptions(tc.raw); err != nil {
				t.Fatalf("api_variant_options rejected: %v", err)
			}
		})
	}
}

func TestValidateOpenRouterAppCategories(t *testing.T) {
	if err := ValidateOpenRouterAppCategories(
		"OMNARA_OPENROUTER_APP_CATEGORIES",
		[]string{"cloud-agent", "programming-app"},
	); err != nil {
		t.Fatalf("valid categories rejected: %v", err)
	}
	for _, tc := range []struct {
		name       string
		categories []string
	}{
		{name: "empty category", categories: []string{""}},
		{name: "blank category", categories: []string{" "}},
		{name: "too many categories", categories: []string{"cli-agent", "cloud-agent", "programming-app"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateOpenRouterAppCategories("OMNARA_OPENROUTER_APP_CATEGORIES", tc.categories); err == nil {
				t.Fatal("categories unexpectedly accepted")
			}
		})
	}
	if err := ValidateOpenRouterAppCategories(
		"OMNARA_OPENROUTER_APP_CATEGORIES",
		[]string{"future-category"},
	); err != nil {
		t.Fatalf("future category rejected: %v", err)
	}
}

func TestEffectiveConfiguredModelRevisionForProjectGrant(t *testing.T) {
	configuredModelID := uuid.New()
	baseRevision := ConfiguredModelRevisionRecord{
		ID:                        uuid.New(),
		ConfiguredModelID:         configuredModelID,
		ContextWindowTokens:       1000,
		MaxOutputTokens:           200,
		DefaultMaxOutputTokens:    intPtrForModelProviderConfigStoreTest(100),
		DefaultCacheRetention:     ModelCacheRetentionLong,
		SupportsTools:             true,
		SupportsReasoning:         true,
		DefaultReasoningEffort:    "medium",
		SupportedReasoningEfforts: []string{"low", "medium", "high"},
		InputModalities:           []string{"text", "image"},
		OutputModalities:          []string{"text"},
	}

	t.Run("inherits omitted grant fields", func(t *testing.T) {
		effective, err := EffectiveConfiguredModelRevisionForProjectGrant(
			modelprotocol.APIFormatOpenAIResponses,
			baseRevision,
			ProjectModelGrantRecord{ConfiguredModelID: configuredModelID},
		)
		if err != nil {
			t.Fatalf("effective grant: %v", err)
		}
		if effective.ContextWindowTokens != baseRevision.ContextWindowTokens || !effective.SupportsTools ||
			!effective.SupportsReasoning ||
			effective.DefaultReasoningEffort != "medium" ||
			!slices.Equal(effective.InputModalities, []string{"text", "image"}) {
			t.Fatalf("unexpected inherited effective revision: %+v", effective)
		}
	})

	t.Run("narrows token bounds and modalities", func(t *testing.T) {
		effective, err := EffectiveConfiguredModelRevisionForProjectGrant(
			modelprotocol.APIFormatOpenAIResponses,
			baseRevision,
			ProjectModelGrantRecord{
				ConfiguredModelID:         configuredModelID,
				ContextWindowTokens:       intPtrForModelProviderConfigStoreTest(800),
				MaxOutputTokens:           intPtrForModelProviderConfigStoreTest(150),
				DefaultMaxOutputTokens:    intPtrForModelProviderConfigStoreTest(120),
				SupportsTools:             boolPtrForModelProviderConfigStoreTest(false),
				DefaultCacheRetention:     ModelCacheRetentionShort,
				SupportedReasoningEfforts: []string{"low", "medium"},
				InputModalities:           []string{"text"},
				OutputModalities:          []string{"text"},
			},
		)
		if err != nil {
			t.Fatalf("effective grant: %v", err)
		}
		if effective.ContextWindowTokens != 800 || effective.MaxOutputTokens != 150 ||
			*effective.DefaultMaxOutputTokens != 120 ||
			effective.SupportsTools ||
			effective.DefaultCacheRetention != ModelCacheRetentionShort ||
			!slices.Equal(effective.SupportedReasoningEfforts, []string{"low", "medium"}) ||
			!slices.Equal(effective.InputModalities, []string{"text"}) {
			t.Fatalf("unexpected narrowed effective revision: %+v", effective)
		}
	})

	t.Run("disable reasoning clears inherited reasoning fields", func(t *testing.T) {
		effective, err := EffectiveConfiguredModelRevisionForProjectGrant(
			modelprotocol.APIFormatOpenAIResponses,
			baseRevision,
			ProjectModelGrantRecord{
				ConfiguredModelID: configuredModelID,
				SupportsReasoning: boolPtrForModelProviderConfigStoreTest(false),
			},
		)
		if err != nil {
			t.Fatalf("effective grant: %v", err)
		}
		if effective.SupportsReasoning || effective.DefaultReasoningEffort != "" ||
			len(effective.SupportedReasoningEfforts) != 0 {
			t.Fatalf("reasoning fields were not cleared: %+v", effective)
		}
	})

	for _, tc := range []struct {
		name     string
		revision ConfiguredModelRevisionRecord
		grant    ProjectModelGrantRecord
	}{
		{
			name:     "reject wider context",
			revision: baseRevision,
			grant: ProjectModelGrantRecord{
				ConfiguredModelID:   configuredModelID,
				ContextWindowTokens: intPtrForModelProviderConfigStoreTest(1001),
			},
		},
		{
			name:     "reject tool enable when revision disables tools",
			revision: revisionWithToolSupport(baseRevision, false),
			grant: ProjectModelGrantRecord{
				ConfiguredModelID: configuredModelID,
				SupportsTools:     boolPtrForModelProviderConfigStoreTest(true),
			},
		},
		{
			name:     "reject effort outside revision subset",
			revision: baseRevision,
			grant: ProjectModelGrantRecord{
				ConfiguredModelID:         configuredModelID,
				SupportedReasoningEfforts: []string{"xhigh"},
			},
		},
		{
			name:     "reject modality outside revision subset",
			revision: baseRevision,
			grant:    ProjectModelGrantRecord{ConfiguredModelID: configuredModelID, InputModalities: []string{"audio"}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := EffectiveConfiguredModelRevisionForProjectGrant(
				modelprotocol.APIFormatOpenAIResponses,
				tc.revision,
				tc.grant,
			); !errors.Is(
				err,
				storeerr.ErrInvalidModelProviderConfig,
			) {
				t.Fatalf("error = %v, want storeerr.ErrInvalidModelProviderConfig", err)
			}
		})
	}
}

func TestEffectiveConfiguredModelRevisionForAgentOptions(t *testing.T) {
	configuredModelID := uuid.New()
	projectEffectiveRevision := ConfiguredModelRevisionRecord{
		ID:                        uuid.New(),
		ConfiguredModelID:         configuredModelID,
		ContextWindowTokens:       1000,
		MaxOutputTokens:           200,
		DefaultMaxOutputTokens:    intPtrForModelProviderConfigStoreTest(100),
		DefaultCacheRetention:     ModelCacheRetentionLong,
		SupportsTools:             true,
		SupportsReasoning:         true,
		DefaultReasoningEffort:    "medium",
		SupportedReasoningEfforts: []string{"low", "medium", "high"},
	}

	effective, err := EffectiveConfiguredModelRevisionForAgentOptions(
		modelprotocol.APIFormatOpenAIResponses,
		projectEffectiveRevision,
		agentconfig.ModelOverrides{
			ContextWindowTokens:    intPtrForModelProviderConfigStoreTest(800),
			DefaultMaxOutputTokens: intPtrForModelProviderConfigStoreTest(120),
			CacheRetention:         ModelCacheRetentionShort,
			ReasoningEffort:        "high",
		},
	)
	if err != nil {
		t.Fatalf("effective runtime options: %v", err)
	}
	if effective.ContextWindowTokens != 800 || *effective.DefaultMaxOutputTokens != 120 ||
		effective.DefaultCacheRetention != ModelCacheRetentionShort ||
		effective.DefaultReasoningEffort != "high" {
		t.Fatalf("unexpected effective runtime options: %+v", effective)
	}
	if effective.MaxOutputTokens != 200 {
		t.Fatalf("runtime max_output_tokens changed ceiling = %d, want 200", effective.MaxOutputTokens)
	}

	for _, tc := range []struct {
		name    string
		options agentconfig.ModelOverrides
	}{
		{
			name:    "reject wider context",
			options: agentconfig.ModelOverrides{ContextWindowTokens: intPtrForModelProviderConfigStoreTest(1001)},
		},
		{
			name:    "reject default output over ceiling",
			options: agentconfig.ModelOverrides{DefaultMaxOutputTokens: intPtrForModelProviderConfigStoreTest(201)},
		},
		{
			name:    "reject unsupported reasoning",
			options: agentconfig.ModelOverrides{ReasoningEffort: "xhigh"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := EffectiveConfiguredModelRevisionForAgentOptions(
				modelprotocol.APIFormatOpenAIResponses,
				projectEffectiveRevision,
				tc.options,
			); !errors.Is(
				err,
				storeerr.ErrInvalidModelProviderConfig,
			) {
				t.Fatalf("error = %v, want storeerr.ErrInvalidModelProviderConfig", err)
			}
		})
	}
}

func intPtrForModelProviderConfigStoreTest(value int) *int {
	return &value
}

func boolPtrForModelProviderConfigStoreTest(value bool) *bool {
	return &value
}

func revisionWithToolSupport(revision ConfiguredModelRevisionRecord, supportsTools bool) ConfiguredModelRevisionRecord {
	revision.SupportsTools = supportsTools
	return revision
}

func TestValidateTenantModelOnClusterProvider(t *testing.T) {
	shared := func(slug, options string, wantErr bool) struct {
		modelKind, providerKind management.Kind
		variant                 modelprotocol.APIVariant
		slug, options           string
		wantErr                 bool
	} {
		return struct {
			modelKind, providerKind management.Kind
			variant                 modelprotocol.APIVariant
			slug, options           string
			wantErr                 bool
		}{management.Tenant, management.Cluster, modelprotocol.APIVariantOpenRouter, slug, options, wantErr}
	}
	cases := map[string]struct {
		modelKind, providerKind management.Kind
		variant                 modelprotocol.APIVariant
		slug, options           string
		wantErr                 bool
	}{
		"free variant":                  shared("qwen/qwen3-coder-plus:free", `{}`, true),
		"free variant chained":          shared("qwen/qwen3-coder-plus:free:online", `{}`, true),
		"routing variant":               shared("qwen/qwen3-coder-plus:nitro", `{}`, false),
		"alias":                         shared("~anthropic/claude-sonnet-latest", `{}`, false),
		"provider pin":                  shared("moonshotai/kimi-k3", `{"provider":{"only":["moonshotai"]}}`, false),
		"model fallback":                shared("qwen/qwen3-coder-plus", `{"models":["qwen/qwen3-max"]}`, false),
		"free model fallback":           shared("qwen/qwen3-coder-plus", `{"models":["qwen/qwen3-max:free"]}`, true),
		"end-user identity":             shared("qwen/qwen3-coder-plus", `{"user":"someone-else"}`, true),
		"web plugin":                    shared("qwen/qwen3-coder-plus", `{"plugins":[{"id":"web"}]}`, true),
		"sampling and conversation key": shared("qwen/qwen3-coder-plus", `{"temperature":0.2,"session_id":"mine"}`, false),
		"tenant's own provider": {
			management.Tenant, management.Tenant, modelprotocol.APIVariantOpenRouter,
			"qwen/qwen3-coder-plus:free", `{"user":"x","plugins":[{"id":"web"}]}`, false,
		},
		"cluster model on shared provider": {
			management.Cluster, management.Cluster, modelprotocol.APIVariantOpenRouter,
			"qwen/qwen3-coder-plus:free", `{"user":"x"}`, false,
		},
		"bedrock version suffix": {
			management.Tenant, management.Cluster, modelprotocol.APIVariantBedrock,
			"anthropic.claude-sonnet-4-5-20250929-v1:0", `{}`, false,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := validateTenantModelOnClusterProvider(
				tc.modelKind, tc.providerKind, tc.variant, tc.slug, json.RawMessage(tc.options),
			)
			if tc.wantErr != (err != nil) || (err != nil && !errors.Is(err, storeerr.ErrInvalidModelProviderConfig)) {
				t.Fatalf("err = %v, want error %v", err, tc.wantErr)
			}
		})
	}
}
