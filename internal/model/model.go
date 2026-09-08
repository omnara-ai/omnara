package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
)

type Selection struct {
	OrgID                     string
	ProjectID                 string
	ConfiguredModelRevisionID string
	Overrides                 agentconfig.ModelOverrides
}

type Capabilities struct {
	ContextWindowTokens       int            `json:"context_window_tokens"`
	MaxOutputTokens           *int           `json:"max_output_tokens"`
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

type OutputTokenLimits struct {
	Minimum  int
	Required bool
}

// OutputTokenLimitProvider reports constraints imposed by fixed provider
// options that are not part of model capabilities.
type OutputTokenLimitProvider interface {
	OutputTokenLimits() (OutputTokenLimits, error)
}

var ErrOutputTokenLimitIncompatible = errors.New("output token limit is incompatible with provider options")

const (
	OutputTokenLimitIncompatibleCode    = "output_token_limit_incompatible"
	InvalidOutputTokenConfigurationCode = "invalid_output_token_configuration"
	OutputTokenLimitRequiredCode        = "output_token_limit_required"
)

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
	MaxOutputTokens    int
}

type PrepareForSendInput struct {
	Context     modelcontext.Bundle
	Policy      RequestPolicy
	ErrorSource string
	// ReserveFullOutputAllowance preserves the complete summary allowance during compaction.
	ReserveFullOutputAllowance bool
}

type InputBudgetAssessment struct {
	EstimatedInputTokens int `json:"estimated_input_tokens"`
	UsableInputTokens    int `json:"usable_input_tokens"`
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
	limits, err := OutputTokenLimitsForClient(client, input.ErrorSource)
	if err != nil {
		return PreparedRequest{}, err
	}
	if err := limits.Validate(input.Policy.MaxOutputTokens, input.ErrorSource); err != nil {
		return PreparedRequest{}, err
	}
	if err := validateRequestModalities(
		input.Context,
		client,
		input.ErrorSource,
	); err != nil {
		return PreparedRequest{}, err
	}
	capabilities := CapabilitiesForClient(client)
	window := modelWindowForRequest(capabilities, input.Policy)
	window.OutputReserveTokens = max(window.OutputReserveTokens, limits.Minimum)
	if input.ReserveFullOutputAllowance {
		window.OutputReserveTokens = input.Policy.MaxOutputTokens
	}
	if capabilities.ContextWindowTokens-window.SafetyMarginTokens-limits.Minimum <= 0 && limits.Minimum > 0 {
		return PreparedRequest{}, ProviderError{
			Kind: ErrorKindInvalidRequest, Source: input.ErrorSource, Code: InvalidOutputTokenConfigurationCode,
			Message: "The configured context window cannot accommodate the provider's minimum output allowance. " +
				"Increase the context window or reduce the configured thinking budget.",
		}
	}
	prepared, err := prepareRequest(ctx, client, input)
	if err != nil {
		return PreparedRequest{}, err
	}
	usable := window.UsableInputTokens()
	if !input.ReserveFullOutputAllowance && input.Policy.MaxOutputTokens > 0 && prepared.InputTokenEstimate <= usable {
		remaining := capabilities.ContextWindowTokens - window.SafetyMarginTokens - prepared.InputTokenEstimate
		if remaining < input.Policy.MaxOutputTokens {
			// The reduced allowance leaves exactly this much room for input. Check
			// the final body too in case an adapter changes its input projection.
			usable = prepared.InputTokenEstimate
			input.Policy.MaxOutputTokens = remaining
			prepared, err = prepareRequest(ctx, client, input)
			if err != nil {
				return PreparedRequest{}, err
			}
		}
	}
	prepared.MaxOutputTokens = input.Policy.MaxOutputTokens
	prepared.InputBudget = InputBudgetAssessment{
		EstimatedInputTokens: prepared.InputTokenEstimate, UsableInputTokens: usable,
	}
	return prepared, nil
}

func prepareRequest(ctx context.Context, client Client, input PrepareForSendInput) (PreparedRequest, error) {
	prepared, err := client.Prepare(ctx, PrepareInput{Context: input.Context, Policy: input.Policy})
	if err != nil {
		return PreparedRequest{}, err
	}
	if len(prepared.Body) == 0 {
		return PreparedRequest{}, ProviderError{
			Kind: ErrorKindInvalidRequest, Source: input.ErrorSource, Code: "empty_prepared_request",
			Message: "The configured model produced an empty provider request.",
		}
	}
	if prepared.InputTokenEstimate <= 0 {
		prepared.InputTokenEstimate = modelcontext.EstimatePreparedRequest(prepared.Body, input.Context.RenderedMedia)
	}
	return prepared, nil
}

func (l OutputTokenLimits) Validate(maxOutputTokens int, errorSource string) error {
	if l.Required && maxOutputTokens == 0 {
		return ProviderError{
			Kind: ErrorKindInvalidRequest, Source: errorSource, Code: OutputTokenLimitRequiredCode,
			Message: "This provider requires an output token allowance. Configure the model's output capacity, " +
				"or set default_max_output_tokens on the model, project grant, or agent.",
		}
	}
	if l.Minimum <= 0 || maxOutputTokens >= l.Minimum {
		return nil
	}
	cause := fmt.Errorf(
		"%w: max output tokens (%d) must be at least %d",
		ErrOutputTokenLimitIncompatible,
		maxOutputTokens,
		l.Minimum,
	)
	return ProviderError{
		Kind:    ErrorKindInvalidRequest,
		Source:  errorSource,
		Code:    OutputTokenLimitIncompatibleCode,
		Message: cause.Error() + ". Configure a larger output allowance or reduce the thinking budget.",
		Cause:   cause,
	}
}

func OutputTokenLimitsForClient(
	client Client,
	errorSource string,
) (OutputTokenLimits, error) {
	provider, ok := client.(OutputTokenLimitProvider)
	if !ok {
		return OutputTokenLimits{}, nil
	}
	limits, err := provider.OutputTokenLimits()
	if err == nil && limits.Minimum < 0 {
		err = errors.New("minimum output token limit cannot be negative")
	}
	if err == nil {
		return limits, nil
	}
	if _, classified := ClassifyError(err); classified {
		return OutputTokenLimits{}, err
	}
	return OutputTokenLimits{}, ProviderError{
		Kind:    ErrorKindInvalidRequest,
		Source:  errorSource,
		Code:    InvalidOutputTokenConfigurationCode,
		Message: err.Error(),
		Cause:   err,
	}
}

func validateRequestModalities(bundle modelcontext.Bundle, client Client, errorSource string) error {
	capabilities := CapabilitiesForClient(client)
	if len(capabilities.InputModalities) > 0 {
		if !containsModality(capabilities.InputModalities, modelcontext.InputModalityText) {
			return ProviderError{
				Kind:    ErrorKindInvalidRequest,
				Source:  errorSource,
				Code:    "unsupported_input_modality",
				Message: "The live model grant does not allow the text input required by this agent request.",
			}
		}
		for _, media := range bundle.RenderedMedia {
			modality := media.InputModality()
			if modality != "" && !containsModality(capabilities.InputModalities, modality) {
				return ProviderError{
					Kind:   ErrorKindInvalidRequest,
					Source: errorSource,
					Code:   "unsupported_input_modality",
					Message: "The live model grant does not allow " + modality +
						" input required by this agent request.",
				}
			}
		}
	}
	if len(capabilities.OutputModalities) > 0 &&
		!containsModality(capabilities.OutputModalities, modelcontext.InputModalityText) {
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

func (c Capabilities) AllowsInputModality(modality string) bool {
	return len(c.InputModalities) == 0 || containsModality(c.InputModalities, modality)
}

type RequestPolicy struct {
	MaxOutputTokens                   int            `json:"max_output_tokens,omitempty"`
	CacheRetention                    CacheRetention `json:"cache_retention,omitempty"`
	SupportsTools                     *bool          `json:"supports_tools,omitempty"`
	SupportsReasoning                 *bool          `json:"supports_reasoning,omitempty"`
	ReasoningEffort                   string         `json:"reasoning_effort,omitempty"`
	ProviderReplayCutoffEventSequence int64          `json:"provider_replay_cutoff_event_sequence,omitempty"`
}

// AllowsProviderReplay reports whether replay is newer than the inclusive cutoff.
func (p RequestPolicy) AllowsProviderReplay(eventSequence int64) bool {
	return p.ProviderReplayCutoffEventSequence <= 0 ||
		eventSequence > p.ProviderReplayCutoffEventSequence
}

func RequestPolicyFromCapabilities(capabilities Capabilities) RequestPolicy {
	supportsReasoning := capabilities.SupportsReasoning
	maxOutputTokens := capabilities.DefaultMaxOutputTokens
	if maxOutputTokens == 0 && capabilities.MaxOutputTokens != nil {
		maxOutputTokens = *capabilities.MaxOutputTokens
	}
	return RequestPolicy{
		MaxOutputTokens:   maxOutputTokens,
		CacheRetention:    capabilities.DefaultCacheRetention,
		SupportsTools:     capabilities.SupportsTools,
		SupportsReasoning: &supportsReasoning,
		ReasoningEffort:   capabilities.DefaultReasoningEffort,
	}
}

// Normal admission reserves useful generation headroom independently of a
// model's maximum output capacity. The wire allowance uses the remaining window.
const normalOutputHeadroomTokens = 32_768

func modelWindowForRequest(capabilities Capabilities, policy RequestPolicy) modelcontext.ModelWindow {
	reserve := min(normalOutputHeadroomTokens, capabilities.ContextWindowTokens/2)
	if policy.MaxOutputTokens > 0 {
		reserve = min(reserve, policy.MaxOutputTokens)
	}
	return modelcontext.ModelWindow{
		ContextTokens:       capabilities.ContextWindowTokens,
		OutputReserveTokens: reserve,
		SafetyMarginTokens:  modelcontext.DefaultSafetyMarginTokens(capabilities.ContextWindowTokens),
	}
}

func UsableInputTokensForRequest(capabilities Capabilities, policy RequestPolicy) int {
	return modelWindowForRequest(capabilities, policy).UsableInputTokens()
}
