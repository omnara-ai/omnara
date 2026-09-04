package modelstore

import (
	"encoding/json"
	"time"

	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage/listing"
	"github.com/omnara-ai/omnara/internal/storage/management"
	"github.com/omnara-ai/omnara/internal/storage/patch"
)

const (
	ModelProviderAuthKindBearerToken  = "bearer_token"
	ModelProviderAuthKindAPIKeyHeader = "api_key_header"
	ModelProviderAuthKindSigV4        = "sigv4"

	ModelCacheRetentionNone  = "none"
	ModelCacheRetentionShort = "short"
	ModelCacheRetentionLong  = "long"

	DefaultModelProviderRequestTimeoutMS = int64((10 * time.Minute) / time.Millisecond)
)

type CreateModelProviderConfigInput struct {
	OrgID              ID
	Name               string
	APIFormat          modelprotocol.APIFormat
	APIVariant         modelprotocol.APIVariant
	BaseURL            string
	EndpointPath       string
	RequestTimeoutMS   int
	AuthKind           string
	AuthOptions        json.RawMessage
	CredentialSecretID ID
	managementKind     management.Kind
}

type modelProviderConfigUpdate struct {
	OrgID              ID
	ID                 ID
	BaseURL            string
	EndpointPath       string
	RequestTimeoutMS   int
	AuthKind           string
	AuthOptions        json.RawMessage
	CredentialSecretID ID
	APIFormat          modelprotocol.APIFormat
	APIVariant         modelprotocol.APIVariant
}

type PatchModelProviderConfigInput struct {
	OrgID              ID
	ID                 ID
	BaseURL            *string
	EndpointPath       *string
	RequestTimeoutMS   *int
	AuthKind           *string
	AuthOptions        *json.RawMessage
	CredentialSecretID *ID
}

type ModelProviderConfigRecord struct {
	ID                 ID                       `json:"id"`
	OrgID              ID                       `json:"org_id"`
	ManagementKind     management.Kind          `json:"management_kind"`
	Name               string                   `json:"name"`
	APIFormat          modelprotocol.APIFormat  `json:"api_format"`
	APIVariant         modelprotocol.APIVariant `json:"api_variant"`
	BaseURL            string                   `json:"base_url"`
	EndpointPath       string                   `json:"endpoint_path"`
	RequestTimeoutMS   int                      `json:"request_timeout_ms"`
	AuthKind           string                   `json:"auth_kind"`
	AuthOptions        json.RawMessage          `json:"auth_options"`
	CredentialSecretID ID                       `json:"credential_secret_id"`
	DeletedAt          *time.Time               `json:"deleted_at,omitempty"`
	CreatedAt          time.Time                `json:"created_at"`
	UpdatedAt          time.Time                `json:"updated_at"`
	Created            bool                     `json:"-"`
}

type CreateConfiguredModelInput struct {
	OrgID                     ID
	ModelProviderConfigID     ID
	Name                      string
	ProviderModelSlug         string
	ContextWindowTokens       int
	MaxOutputTokens           int
	DefaultMaxOutputTokens    *int
	DefaultCacheRetention     string
	SupportsTools             *bool
	SupportsReasoning         bool
	DefaultReasoningEffort    string
	SupportedReasoningEfforts []string
	InputModalities           []string
	OutputModalities          []string
	APIVariantOptions         json.RawMessage
}

type configuredModelUpdate struct {
	OrgID                     ID
	ModelProviderConfigID     ID
	ID                        ID
	Name                      string
	ProviderModelSlug         string
	ContextWindowTokens       int
	MaxOutputTokens           int
	DefaultMaxOutputTokens    *int
	DefaultCacheRetention     string
	SupportsTools             *bool
	SupportsReasoning         bool
	DefaultReasoningEffort    string
	SupportedReasoningEfforts []string
	InputModalities           []string
	OutputModalities          []string
	APIVariantOptions         json.RawMessage
}

type PatchConfiguredModelInput struct {
	OrgID                     ID
	ModelProviderConfigID     ID
	ID                        ID
	Name                      *string
	ProviderModelSlug         *string
	ContextWindowTokens       *int
	MaxOutputTokens           *int
	DefaultMaxOutputTokens    patch.NullableInt
	DefaultCacheRetention     *string
	SupportsTools             *bool
	SupportsReasoning         *bool
	DefaultReasoningEffort    *string
	SupportedReasoningEfforts *[]string
	InputModalities           *[]string
	OutputModalities          *[]string
	APIVariantOptions         *json.RawMessage
}

type ConfiguredModelRecord struct {
	ID                        ID              `json:"id"`
	OrgID                     ID              `json:"org_id"`
	ModelProviderConfigID     ID              `json:"model_provider_config_id"`
	ManagementKind            management.Kind `json:"management_kind"`
	Name                      string          `json:"name"`
	CurrentRevisionID         ID              `json:"current_revision_id"`
	ProviderModelSlug         string          `json:"provider_model_slug"`
	ContextWindowTokens       int             `json:"context_window_tokens"`
	MaxOutputTokens           int             `json:"max_output_tokens"`
	DefaultMaxOutputTokens    *int            `json:"default_max_output_tokens,omitempty"`
	DefaultCacheRetention     string          `json:"default_cache_retention,omitempty"`
	SupportsTools             bool            `json:"supports_tools"`
	SupportsReasoning         bool            `json:"supports_reasoning"`
	DefaultReasoningEffort    string          `json:"default_reasoning_effort"`
	SupportedReasoningEfforts []string        `json:"supported_reasoning_efforts"`
	InputModalities           []string        `json:"input_modalities"`
	OutputModalities          []string        `json:"output_modalities"`
	APIVariantOptions         json.RawMessage `json:"api_variant_options"`
	DeletedAt                 *time.Time      `json:"deleted_at,omitempty"`
	CreatedAt                 time.Time       `json:"created_at"`
	UpdatedAt                 time.Time       `json:"updated_at"`
	RevisionCreatedAt         time.Time       `json:"revision_created_at"`
	Created                   bool            `json:"-"`
}

type ConfiguredModelRevisionRecord struct {
	ID                        ID              `json:"id"`
	OrgID                     ID              `json:"org_id"`
	ConfiguredModelID         ID              `json:"configured_model_id"`
	ModelProviderConfigID     ID              `json:"model_provider_config_id"`
	ProviderModelSlug         string          `json:"provider_model_slug"`
	ContextWindowTokens       int             `json:"context_window_tokens"`
	MaxOutputTokens           int             `json:"max_output_tokens"`
	DefaultMaxOutputTokens    *int            `json:"default_max_output_tokens,omitempty"`
	DefaultCacheRetention     string          `json:"default_cache_retention,omitempty"`
	SupportsTools             bool            `json:"supports_tools"`
	SupportsReasoning         bool            `json:"supports_reasoning"`
	DefaultReasoningEffort    string          `json:"default_reasoning_effort"`
	SupportedReasoningEfforts []string        `json:"supported_reasoning_efforts"`
	InputModalities           []string        `json:"input_modalities"`
	OutputModalities          []string        `json:"output_modalities"`
	APIVariantOptions         json.RawMessage `json:"api_variant_options"`
	CreatedAt                 time.Time       `json:"created_at"`
}

type ConfiguredModelRevisionDisplayRecord struct {
	ConfiguredModelRevisionRecord
	ConfiguredModelName string
	ProviderConfigName  string
	APIFormat           modelprotocol.APIFormat
	APIVariant          modelprotocol.APIVariant
}

type CreateProjectModelGrantInput struct {
	OrgID                     ID
	ProjectID                 ID
	ConfiguredModelID         ID
	ContextWindowTokens       *int
	MaxOutputTokens           *int
	DefaultMaxOutputTokens    *int
	DefaultCacheRetention     string
	SupportsTools             *bool
	SupportsReasoning         *bool
	DefaultReasoningEffort    string
	SupportedReasoningEfforts []string
	InputModalities           []string
	OutputModalities          []string
}

type UpdateProjectModelGrantInput struct {
	OrgID                     ID
	ProjectID                 ID
	ID                        ID
	ContextWindowTokens       patch.NullableInt
	MaxOutputTokens           patch.NullableInt
	DefaultMaxOutputTokens    patch.NullableInt
	DefaultCacheRetention     *string
	SupportsTools             patch.NullableBool
	SupportsReasoning         patch.NullableBool
	DefaultReasoningEffort    *string
	SupportedReasoningEfforts *[]string
	InputModalities           *[]string
	OutputModalities          *[]string
}

type ProjectModelGrantRecord struct {
	ID                        ID        `json:"id"`
	OrgID                     ID        `json:"org_id"`
	ProjectID                 ID        `json:"project_id"`
	ConfiguredModelID         ID        `json:"configured_model_id"`
	ContextWindowTokens       *int      `json:"context_window_tokens,omitempty"`
	MaxOutputTokens           *int      `json:"max_output_tokens,omitempty"`
	DefaultMaxOutputTokens    *int      `json:"default_max_output_tokens,omitempty"`
	DefaultCacheRetention     string    `json:"default_cache_retention,omitempty"`
	SupportsTools             *bool     `json:"supports_tools,omitempty"`
	SupportsReasoning         *bool     `json:"supports_reasoning,omitempty"`
	DefaultReasoningEffort    string    `json:"default_reasoning_effort,omitempty"`
	SupportedReasoningEfforts []string  `json:"supported_reasoning_efforts"`
	InputModalities           []string  `json:"input_modalities"`
	OutputModalities          []string  `json:"output_modalities"`
	CreatedAt                 time.Time `json:"created_at"`
	UpdatedAt                 time.Time `json:"updated_at"`
	Created                   bool      `json:"-"`
}

type ListProjectModelGrantsInput struct {
	OrgID     ID
	ProjectID ID
	Limit     int
	List      listing.Options
}

type ConfiguredModelSummaryRecord struct {
	ID                    ID        `json:"id"`
	OrgID                 ID        `json:"org_id"`
	ModelProviderConfigID ID        `json:"model_provider_config_id"`
	Name                  string    `json:"name"`
	ProviderConfigName    string    `json:"provider_config"`
	CreatedAt             time.Time `json:"created_at"`
	UpdatedAt             time.Time `json:"updated_at"`
}

type ProjectModelGrantListRecord struct {
	Grant ProjectModelGrantRecord
	Model ConfiguredModelSummaryRecord
}

type ListProjectModelGrantsResult struct {
	Grants  []ProjectModelGrantListRecord
	HasMore bool
	Next    listing.Cursor
}
