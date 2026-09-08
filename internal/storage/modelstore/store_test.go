package modelstore

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage/management"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
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

func TestValidateConfiguredModelOptionsUnknownCapacity(t *testing.T) {
	for _, tc := range []struct {
		name      string
		context   int
		capacity  *int
		allowance *int
		wantErr   bool
	}{
		{name: "unknown", context: 128000},
		{name: "unknown with allowance", context: 128000, allowance: new(32000)},
		{name: "known without allowance", context: 128000, capacity: new(64000)},
		{name: "capacity exhausts configured context", context: 128000, capacity: new(128000), wantErr: true},
		{name: "zero capacity", context: 128000, capacity: new(0), wantErr: true},
		{name: "negative capacity", context: 128000, capacity: new(-1), wantErr: true},
		{name: "zero allowance", context: 128000, allowance: new(0), wantErr: true},
		{name: "allowance exhausts context", context: 128000, allowance: new(128000), wantErr: true},
		{
			name:      "allowance exceeds capacity",
			context:   128000,
			capacity:  new(32000),
			allowance: new(64000),
			wantErr:   true,
		},
		{name: "window too small", context: 1, wantErr: true},
	} {
		for _, format := range []modelprotocol.APIFormat{
			modelprotocol.APIFormatOpenAIChatCompletions,
			modelprotocol.APIFormatOpenAIResponses,
			modelprotocol.APIFormatAnthropicMessages,
		} {
			t.Run(tc.name+"/"+string(format), func(t *testing.T) {
				err := validateConfiguredModelOptions(format, configuredModelOptions{
					ContextWindowTokens: tc.context, MaxOutputTokens: tc.capacity, DefaultMaxOutputTokens: tc.allowance,
				})
				if tc.wantErr {
					if !errors.Is(err, storeerr.ErrInvalidModelProviderConfig) {
						t.Fatalf("error = %v, want invalid configuration", err)
					}
				} else if err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestValidateConfiguredModelOptionsTokenBounds(t *testing.T) {
	if err := validateConfiguredModelOptions(modelprotocol.APIFormatOpenAIResponses, configuredModelOptions{
		ContextWindowTokens:    math.MaxInt32,
		MaxOutputTokens:        new(math.MaxInt32 - 1),
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
				MaxOutputTokens:     new(1),
			},
			messageContains: "context_window_tokens",
		},
		{
			name: "int32 overflow",
			input: configuredModelOptions{
				ContextWindowTokens: 100,
				MaxOutputTokens:     new(math.MaxInt32 + 1),
			},
			messageContains: "max_output_tokens",
		},
		{
			name: "default exceeds max",
			input: configuredModelOptions{
				ContextWindowTokens:    100,
				MaxOutputTokens:        new(10),
				DefaultMaxOutputTokens: intPtrForModelProviderConfigStoreTest(11),
			},
			messageContains: "default_max_output_tokens",
		},
		{
			name: "max output exhausts context",
			input: configuredModelOptions{
				ContextWindowTokens:    100,
				MaxOutputTokens:        new(100),
				DefaultMaxOutputTokens: intPtrForModelProviderConfigStoreTest(100),
			},
			messageContains: "max_output_tokens",
		},
		{
			name: "invalid cache retention",
			input: configuredModelOptions{
				ContextWindowTokens:   100,
				MaxOutputTokens:       new(1),
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
		MaxOutputTokens:           new(200),
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
		if effective.ContextWindowTokens != 800 ||
			(effective.MaxOutputTokens == nil ||
				*effective.MaxOutputTokens != 150) ||

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
			name:     "reject explicit capacity exhausting narrowed context",
			revision: baseRevision,
			grant: ProjectModelGrantRecord{
				ConfiguredModelID:   configuredModelID,
				ContextWindowTokens: new(150),
				MaxOutputTokens:     new(150),
			},
		},
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
		MaxOutputTokens:           new(200),
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
	if effective.MaxOutputTokens == nil || *effective.MaxOutputTokens != 200 {
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
		"free variant":         shared("qwen/qwen3-coder-plus:free", `{}`, true),
		"free variant chained": shared("qwen/qwen3-coder-plus:nitro:free", `{}`, true),
		"free router":          shared("openrouter/free", `{}`, true),
		"online variant":       shared("qwen/qwen3-coder-plus:online", `{}`, true),
		"preset model":         shared("@preset/shared-thing", `{}`, true),
		"preset on a model":    shared("openai/gpt-5.6@preset/shared-thing", `{}`, true),
		"routing variant":      shared("qwen/qwen3-coder-plus:nitro", `{}`, false),
		"paid router":          shared("openrouter/auto", `{}`, false),
		"alias":                shared("~anthropic/claude-sonnet-latest", `{}`, false),
		"provider pin":         shared("moonshotai/kimi-k3", `{"provider":{"only":["moonshotai"]}}`, false),
		"paid fallback": shared(
			"qwen/qwen3-coder-plus",
			`{"models":["qwen/qwen3-max"],"route":"fallback"}`,
			false,
		),
		"free fallback":     shared("qwen/qwen3-coder-plus", `{"models":["openrouter/free"]}`, true),
		"online fallback":   shared("qwen/qwen3-coder-plus", `{"models":["qwen/qwen3-max:online"]}`, true),
		"end-user identity": shared("qwen/qwen3-coder-plus", `{"user":"someone-else"}`, true),
		"web plugin":        shared("qwen/qwen3-coder-plus", `{"plugins":[{"id":"web"}]}`, true),
		"web search options": shared(
			"qwen/qwen3-coder-plus",
			`{"web_search_options":{"search_context_size":"low"}}`, true,
		),
		"preset option":       shared("qwen/qwen3-coder-plus", `{"preset":"shared-thing"}`, true),
		"unclassified option": shared("qwen/qwen3-coder-plus", `{"future_knob":true}`, true),
		"tenant-scoped options": shared(
			"qwen/qwen3-coder-plus",
			`{"temperature":0.2,"transforms":[],"session_id":"mine"}`, false,
		),
		"tenant's own provider": {
			management.Tenant, management.Tenant, modelprotocol.APIVariantOpenRouter,
			"openrouter/free", `{"user":"x","plugins":[{"id":"web"}]}`, false,
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

func TestReconciliationOutputCapacityComparisonRemainsStrict(t *testing.T) {
	input := normalizeCreateConfiguredModelInput(CreateConfiguredModelInput{
		ModelProviderConfigID: uuid.New(),
		Name:                  "model",
		ProviderModelSlug:     "model",
		ContextWindowTokens:   128000,
	})
	record := ConfiguredModelRecord{
		ModelProviderConfigID: input.ModelProviderConfigID,
		Name:                  input.Name,
		ProviderModelSlug:     input.ProviderModelSlug,
		ContextWindowTokens:   input.ContextWindowTokens,
		SupportsTools:         true,
	}
	if !sameConfiguredModelIntent(record, input) {
		t.Fatal("equal unknown capacities differ")
	}
	record.MaxOutputTokens = new(64000)
	if sameConfiguredModelIntent(record, input) {
		t.Fatal("reconciliation ignored desired unknown capacity")
	}
	input.MaxOutputTokens = new(64000)
	if !sameConfiguredModelIntent(record, input) {
		t.Fatal("equal known capacities differ")
	}
	record.MaxOutputTokens = nil
	if sameConfiguredModelIntent(record, input) {
		t.Fatal("reconciliation ignored desired known capacity")
	}
}

func TestUnknownCapacityPreservesOverrideAllowances(t *testing.T) {
	for _, tc := range []struct {
		name                                       string
		capacity, projectAllowance, agentAllowance *int
		wantAllowance                              *int
		invalid                                    bool
	}{
		{name: "project allowance", projectAllowance: new(32000), wantAllowance: new(32000)},
		{name: "agent allowance", agentAllowance: new(48000), wantAllowance: new(48000)},
		{name: "project capacity", capacity: new(64000)},
		{
			name:             "agent overrides project default",
			capacity:         new(64000),
			projectAllowance: new(32000),
			agentAllowance:   new(48000),
			wantAllowance:    new(48000),
		},
		{name: "project capacity exhausts context", capacity: new(100000), invalid: true},
		{name: "project allowance exhausts context", projectAllowance: new(100000), invalid: true},
		{name: "agent allowance exhausts context", agentAllowance: new(100000), invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			revision := ConfiguredModelRevisionRecord{
				ContextWindowTokens: 100000,
				ProviderModelSlug:   "custom-model",
			}
			effective, err := EffectiveConfiguredModelRevisionForProjectGrant(
				modelprotocol.APIFormatAnthropicMessages,
				revision,
				ProjectModelGrantRecord{
					MaxOutputTokens:        tc.capacity,
					DefaultMaxOutputTokens: tc.projectAllowance,
				},
			)
			if err == nil {
				effective, err = EffectiveConfiguredModelRevisionForAgentOptions(
					modelprotocol.APIFormatAnthropicMessages,
					effective,
					agentconfig.ModelOverrides{
						DefaultMaxOutputTokens: tc.agentAllowance,
					},
				)
			}
			if tc.invalid {
				if !errors.Is(err, storeerr.ErrInvalidModelProviderConfig) {
					t.Fatalf("invalid allowance error=%v", err)
				}
				return
			}
			require.NoError(t, err)
			if diff := cmp.Diff(tc.capacity, effective.MaxOutputTokens); diff != "" {
				t.Fatalf("capacity (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantAllowance, effective.DefaultMaxOutputTokens); diff != "" {
				t.Fatalf("allowance (-want +got):\n%s", diff)
			}
			if effective.ContextWindowTokens != 100000 {
				t.Fatalf("context = %d, want 100000", effective.ContextWindowTokens)
			}
		})
	}
}

func TestCapacityIncreasePreservesNarrowedContextOverrides(t *testing.T) {
	for _, level := range []string{"project", "agent", "both"} {
		for _, allowance := range []struct {
			name    string
			value   *int
			invalid bool
		}{
			{name: "capacity only"},
			{name: "fitting inherited default", value: new(4000)},
			{name: "inherited default exhausts context", value: new(32000), invalid: true},
		} {
			t.Run(level+"/"+allowance.name, func(t *testing.T) {
				for _, capacity := range []int{8192, 64000} {
					if allowance.invalid && capacity == 8192 {
						continue // The source default itself must fit the source capacity.
					}
					revision := ConfiguredModelRevisionRecord{
						ContextWindowTokens: 128000, MaxOutputTokens: new(capacity),
						DefaultMaxOutputTokens: allowance.value,
					}
					grant := ProjectModelGrantRecord{}
					overrides := agentconfig.ModelOverrides{}
					if level != "agent" {
						grant.ContextWindowTokens = new(32000)
					}
					if level != "project" {
						overrides.ContextWindowTokens = new(32000)
					}
					effective, err := EffectiveConfiguredModelRevisionForProjectGrant(
						modelprotocol.APIFormatAnthropicMessages, revision, grant,
					)
					if err == nil {
						effective, err = EffectiveConfiguredModelRevisionForAgentOptions(
							modelprotocol.APIFormatAnthropicMessages, effective, overrides,
						)
					}
					if allowance.invalid {
						if !errors.Is(err, storeerr.ErrInvalidModelProviderConfig) {
							t.Fatalf("inherited allowance error=%v", err)
						}
						continue
					}
					if err != nil {
						t.Fatalf("capacity=%d: %v", capacity, err)
					}
					if effective.ContextWindowTokens != 32000 {
						t.Fatalf("capacity=%d: context = %d, want 32000", capacity, effective.ContextWindowTokens)
					}
					if diff := cmp.Diff(new(capacity), effective.MaxOutputTokens); diff != "" {
						t.Fatalf("capacity (-want +got):\n%s", diff)
					}
					if diff := cmp.Diff(allowance.value, effective.DefaultMaxOutputTokens); diff != "" {
						t.Fatalf("capacity=%d: allowance (-want +got):\n%s", capacity, diff)
					}
				}
			})
		}
	}
}
