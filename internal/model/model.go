package model

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
)

type Selection struct {
	OrgID                     string
	ProjectID                 string
	ConfiguredModelRevisionID string
	Options                   SelectionOptions
}

type SelectionOptions struct {
	ContextWindowTokens    *int
	DefaultMaxOutputTokens *int
	CacheRetention         CacheRetention
	ReasoningEffort        string
}

type Capabilities struct {
	ContextWindowTokens       int            `json:"context_window_tokens"`
	MaxOutputTokens           int            `json:"max_output_tokens"`
	DefaultMaxOutputTokens    int            `json:"default_max_output_tokens"`
	DefaultCacheRetention     CacheRetention `json:"default_cache_retention"`
	SupportsTools             *bool          `json:"supports_tools,omitempty"`
	SupportsReasoning         bool           `json:"supports_reasoning"`
	DefaultReasoningEffort    string         `json:"default_reasoning_effort,omitempty"`
	SupportedReasoningEfforts []string       `json:"supported_reasoning_efforts,omitempty"`
	InputModalities           []string       `json:"input_modalities,omitempty"`
	OutputModalities          []string       `json:"output_modalities,omitempty"`
}

type Request struct {
	ProviderRequest json.RawMessage
	DeltaSink       StreamSink
}

type Client interface {
	RequestedProviderModelSlug() string
	APIFormat() modelprotocol.APIFormat
	ModelAPIVariant() modelprotocol.APIVariant
	Capabilities() Capabilities
	Prepare(ctx context.Context, input PrepareInput) (PreparedRequest, error)
	Respond(ctx context.Context, input Request) (Response, error)
}

func APIFormatForClient(client Client) modelprotocol.APIFormat {
	if client == nil {
		return ""
	}
	return client.APIFormat()
}

func MediaProjectorForClient(client Client) modelcontext.MediaProjector {
	projector, _ := client.(modelcontext.MediaProjector)
	return projector
}

func APIVariantForClient(client Client) modelprotocol.APIVariant {
	if client == nil {
		return ""
	}
	return client.ModelAPIVariant()
}

func APIIdentityForClient(
	client Client,
) (modelprotocol.APIFormat, modelprotocol.APIVariant, bool) {
	apiFormat := APIFormatForClient(client)
	apiVariant := APIVariantForClient(client)
	if apiFormat == "" || apiVariant == "" {
		return "", "", false
	}
	return apiFormat, apiVariant, true
}

func ProviderReplayIdentityForClient(
	modelProviderConfigID string,
	client Client,
) modelenvelope.ProviderReplayIdentity {
	if client == nil {
		return modelenvelope.ProviderReplayIdentity{}
	}
	apiFormat, apiVariant, _ := APIIdentityForClient(client)
	return modelenvelope.ProviderReplayIdentity{
		ModelProviderConfigID:      modelProviderConfigID,
		RequestedProviderModelSlug: client.RequestedProviderModelSlug(),
		APIFormat:                  apiFormat,
		APIVariant:                 apiVariant,
	}
}

func CapabilitiesForClient(client Client) Capabilities {
	if client == nil {
		return Capabilities{}
	}
	return client.Capabilities()
}

type Resolver interface {
	Resolve(context.Context, Selection) (ResolvedClient, error)
}

type ResolvedClient struct {
	Client                    Client
	ConfiguredModelRevisionID string
}

type PrepareInput struct {
	Context modelcontext.Bundle
	Policy  RequestPolicy
}

type PreparedRequest struct {
	// Body is the exact JSON byte sequence authorized by Prepare and passed to
	// the provider transport. Respond must not rebuild or mutate it.
	Body               json.RawMessage
	InputTokenEstimate int
	InputBudget        InputBudgetAssessment
}

type RequestLineage struct {
	AgentConfigID             string
	ConfiguredModelRevisionID string
}

type PrepareForSendInput struct {
	Context     modelcontext.Bundle
	Policy      RequestPolicy
	ErrorSource string
	Lineage     RequestLineage
}

type InputEstimateSource string

const (
	InputEstimatePreparedRequest InputEstimateSource = "prepared_request"
	InputEstimateProviderUsage   InputEstimateSource = "provider_usage_floor"
)

// InputBudgetAssessment is the admission result for the exact prepared body.
// Exceeding the configured budget is ordinary control flow, not a provider or
// request-preparation error.
type InputBudgetAssessment struct {
	EstimatedInputTokens     int                 `json:"estimated_input_tokens"`
	UsableInputTokens        int                 `json:"usable_input_tokens"`
	LocalEstimateTokens      int                 `json:"local_estimate_tokens"`
	ProviderUsageFloorTokens int                 `json:"provider_usage_floor_tokens,omitempty"`
	EstimateSource           InputEstimateSource `json:"estimate_source"`
}

func (a InputBudgetAssessment) Fits() bool {
	return a.EstimatedInputTokens > 0 &&
		a.EstimatedInputTokens <= a.UsableInputTokens
}

func (a InputBudgetAssessment) OverBudget() bool {
	return !a.Fits()
}

func PrepareForSend(
	ctx context.Context,
	client Client,
	input PrepareForSendInput,
) (PreparedRequest, error) {
	if client == nil {
		return PreparedRequest{}, errors.New("model client is required")
	}
	if err := validateRequestModalities(
		input.Context,
		CapabilitiesForClient(client),
		input.ErrorSource,
	); err != nil {
		return PreparedRequest{}, err
	}
	prepared, err := client.Prepare(ctx, PrepareInput{Context: input.Context, Policy: input.Policy})
	if err != nil {
		return PreparedRequest{}, err
	}
	if len(prepared.Body) == 0 {
		return PreparedRequest{}, ProviderError{
			Kind:    ErrorKindInvalidRequest,
			Source:  input.ErrorSource,
			Code:    "empty_prepared_request",
			Message: "The configured model produced an empty provider request.",
		}
	}
	estimate := prepared.InputTokenEstimate
	if estimate <= 0 {
		estimate = modelcontext.EstimatePreparedRequest(prepared.Body, input.Context.RenderedMedia)
	}
	prepared.InputTokenEstimate = estimate
	providerUsageFloor, hasProviderUsageFloor := modelcontext.ProviderUsageInputFloor(
		input.Context,
		modelcontext.ModelRequestIdentity{
			AgentConfigID:             input.Lineage.AgentConfigID,
			ConfiguredModelRevisionID: input.Lineage.ConfiguredModelRevisionID,
			RequestedModelSlug:        client.RequestedProviderModelSlug(),
			APIFormat:                 client.APIFormat(),
			APIVariant:                client.ModelAPIVariant(),
		},
		input.Policy.SuppressProviderReplay,
	)
	effectiveEstimate := estimate
	estimateSource := InputEstimatePreparedRequest
	if hasProviderUsageFloor && providerUsageFloor > effectiveEstimate {
		effectiveEstimate = providerUsageFloor
		estimateSource = InputEstimateProviderUsage
	}
	window := modelWindowForRequest(CapabilitiesForClient(client), input.Policy)
	prepared.InputBudget = InputBudgetAssessment{
		EstimatedInputTokens:     effectiveEstimate,
		UsableInputTokens:        window.UsableInputTokens(),
		LocalEstimateTokens:      estimate,
		ProviderUsageFloorTokens: providerUsageFloor,
		EstimateSource:           estimateSource,
	}
	return prepared, nil
}

func validateRequestModalities(bundle modelcontext.Bundle, capabilities Capabilities, errorSource string) error {
	if len(capabilities.InputModalities) > 0 {
		if !containsModality(capabilities.InputModalities, "text") {
			return ProviderError{
				Kind:    ErrorKindInvalidRequest,
				Source:  errorSource,
				Code:    "unsupported_input_modality",
				Message: "The live model grant does not allow the text input required by this agent request.",
			}
		}
		for _, media := range bundle.RenderedMedia {
			requiredModality := ""
			requiredDescription := ""
			switch media.Media.Kind {
			case modelcontext.AttachmentKindImage:
				requiredModality = "image"
				requiredDescription = "image"
			case modelcontext.AttachmentKindDocument:
				requiredModality = "file"
				requiredDescription = "file"
			}
			if requiredModality != "" && !containsModality(capabilities.InputModalities, requiredModality) {
				return ProviderError{
					Kind:   ErrorKindInvalidRequest,
					Source: errorSource,
					Code:   "unsupported_input_modality",
					Message: "The live model grant does not allow " + requiredDescription +
						" input required by this agent request.",
				}
			}
		}
	}
	if len(capabilities.OutputModalities) > 0 &&
		!containsModality(capabilities.OutputModalities, "text") {
		return ProviderError{
			Kind:    ErrorKindInvalidRequest,
			Source:  errorSource,
			Code:    "unsupported_output_modality",
			Message: "The live model grant does not allow the text output required by this agent runtime.",
		}
	}
	return nil
}

func containsModality(values []string, want string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), want) {
			return true
		}
	}
	return false
}

type CacheRetention string

const (
	CacheRetentionNone  CacheRetention = "none"
	CacheRetentionShort CacheRetention = "short"
	CacheRetentionLong  CacheRetention = "long"
)

type RequestPolicy struct {
	MaxOutputTokens        int            `json:"max_output_tokens,omitempty"`
	CacheRetention         CacheRetention `json:"cache_retention,omitempty"`
	SupportsTools          *bool          `json:"supports_tools,omitempty"`
	SupportsReasoning      *bool          `json:"supports_reasoning,omitempty"`
	DefaultReasoningEffort string         `json:"default_reasoning_effort,omitempty"`
	SuppressProviderReplay bool           `json:"suppress_provider_replay,omitempty"`
}

func RequestPolicyFromCapabilities(capabilities Capabilities) RequestPolicy {
	supportsReasoning := capabilities.SupportsReasoning
	maxOutputTokens := capabilities.DefaultMaxOutputTokens
	if maxOutputTokens == 0 {
		maxOutputTokens = capabilities.MaxOutputTokens
	}
	return RequestPolicy{
		MaxOutputTokens:        maxOutputTokens,
		CacheRetention:         capabilities.DefaultCacheRetention,
		SupportsTools:          capabilities.SupportsTools,
		SupportsReasoning:      &supportsReasoning,
		DefaultReasoningEffort: capabilities.DefaultReasoningEffort,
	}
}

func modelWindowForRequest(capabilities Capabilities, policy RequestPolicy) modelcontext.ModelWindow {
	return modelcontext.ModelWindow{
		ContextTokens:          capabilities.ContextWindowTokens,
		RequestMaxOutputTokens: policy.MaxOutputTokens,
		SafetyMarginTokens:     modelcontext.DefaultSafetyMarginTokens(capabilities.ContextWindowTokens),
	}
}

func UsableInputTokensForRequest(capabilities Capabilities, policy RequestPolicy) int {
	return modelWindowForRequest(capabilities, policy).UsableInputTokens()
}
