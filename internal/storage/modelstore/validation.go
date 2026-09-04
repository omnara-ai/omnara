package modelstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/omnara-ai/omnara/internal/storage/management"
	"math"
	"net"
	"net/url"
	"slices"
	"strings"

	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func sameModelProviderConfigIntent(record ModelProviderConfigRecord, input CreateModelProviderConfigInput) bool {
	return record.ManagementKind == input.managementKind &&
		record.Name == input.Name &&
		record.APIFormat == input.APIFormat &&
		record.APIVariant == input.APIVariant &&
		record.BaseURL == input.BaseURL &&
		record.EndpointPath == input.EndpointPath &&
		record.RequestTimeoutMS == input.RequestTimeoutMS &&
		record.AuthKind == input.AuthKind &&
		storeutil.SameJSON(storeutil.NormalizeJSON(record.AuthOptions), storeutil.NormalizeJSON(input.AuthOptions)) &&
		record.CredentialSecretID == input.CredentialSecretID
}

func sameConfiguredModelIntent(record ConfiguredModelRecord, input CreateConfiguredModelInput) bool {
	return record.ModelProviderConfigID == input.ModelProviderConfigID &&
		record.Name == input.Name &&
		record.ProviderModelSlug == input.ProviderModelSlug &&
		record.ContextWindowTokens == input.ContextWindowTokens &&
		record.MaxOutputTokens == input.MaxOutputTokens &&
		storeutil.SameIntPtr(record.DefaultMaxOutputTokens, input.DefaultMaxOutputTokens) &&
		record.DefaultCacheRetention == input.DefaultCacheRetention &&
		record.SupportsTools == boolPtrDefault(input.SupportsTools, true) &&
		record.SupportsReasoning == input.SupportsReasoning &&
		record.DefaultReasoningEffort == input.DefaultReasoningEffort &&
		slices.Equal(record.SupportedReasoningEfforts, input.SupportedReasoningEfforts) &&
		slices.Equal(record.InputModalities, input.InputModalities) &&
		slices.Equal(record.OutputModalities, input.OutputModalities) &&
		storeutil.SameJSON(
			storeutil.NormalizeJSON(record.APIVariantOptions),
			storeutil.NormalizeJSON(input.APIVariantOptions),
		)
}

type configuredModelOptions struct {
	ContextWindowTokens       int
	MaxOutputTokens           int
	DefaultMaxOutputTokens    *int
	DefaultCacheRetention     string
	SupportsReasoning         bool
	DefaultReasoningEffort    string
	SupportedReasoningEfforts []string
}

const (
	configuredModelDefaultMaxOutputTokens     = 8_192
	configuredModelDefaultRequestOutputTokens = 4_096
)

func ResolveConfiguredModelOutputLimits(
	contextWindowTokens int,
	maxOutputTokens, defaultMaxOutputTokens *int,
) (int, *int, error) {
	if contextWindowTokens < 2 {
		return 0, nil, fmt.Errorf(
			"context_window_tokens must be at least 2: %w",
			storeerr.ErrInvalidModelProviderConfig,
		)
	}
	resolvedMax := min(configuredModelDefaultMaxOutputTokens, contextWindowTokens/2)
	if maxOutputTokens != nil {
		resolvedMax = *maxOutputTokens
	}
	resolvedDefault := min(configuredModelDefaultRequestOutputTokens, resolvedMax)
	if defaultMaxOutputTokens != nil {
		resolvedDefault = *defaultMaxOutputTokens
	}
	return resolvedMax, &resolvedDefault, nil
}

func normalizeCreateConfiguredModelInput(input CreateConfiguredModelInput) CreateConfiguredModelInput {
	input.ProviderModelSlug = strings.TrimSpace(input.ProviderModelSlug)
	input.DefaultCacheRetention,
		input.DefaultReasoningEffort,
		input.SupportedReasoningEfforts,
		input.InputModalities,
		input.OutputModalities = normalizeConfiguredModelOptionFields(
		input.DefaultCacheRetention,
		input.DefaultReasoningEffort,
		input.SupportedReasoningEfforts,
		input.InputModalities,
		input.OutputModalities,
	)
	input.APIVariantOptions = storeutil.NormalizeJSON(input.APIVariantOptions)
	return input
}

func normalizeConfiguredModelUpdate(input configuredModelUpdate) configuredModelUpdate {
	input.ProviderModelSlug = strings.TrimSpace(input.ProviderModelSlug)
	input.DefaultCacheRetention,
		input.DefaultReasoningEffort,
		input.SupportedReasoningEfforts,
		input.InputModalities,
		input.OutputModalities = normalizeConfiguredModelOptionFields(
		input.DefaultCacheRetention,
		input.DefaultReasoningEffort,
		input.SupportedReasoningEfforts,
		input.InputModalities,
		input.OutputModalities,
	)
	input.APIVariantOptions = storeutil.NormalizeJSON(input.APIVariantOptions)
	return input
}

func normalizeConfiguredModelOptionFields(
	cacheRetention, defaultReasoningEffort string,
	supportedReasoningEfforts, inputModalities, outputModalities []string,
) (string, string, []string, []string, []string) {
	return cacheRetention,
		strings.TrimSpace(defaultReasoningEffort),
		nonNilStringSlice(supportedReasoningEfforts),
		nonNilStringSlice(inputModalities),
		nonNilStringSlice(outputModalities)
}

func configuredModelOptionsFromCreate(input CreateConfiguredModelInput) configuredModelOptions {
	return configuredModelOptions{
		ContextWindowTokens:       input.ContextWindowTokens,
		MaxOutputTokens:           input.MaxOutputTokens,
		DefaultMaxOutputTokens:    input.DefaultMaxOutputTokens,
		DefaultCacheRetention:     input.DefaultCacheRetention,
		SupportsReasoning:         input.SupportsReasoning,
		DefaultReasoningEffort:    input.DefaultReasoningEffort,
		SupportedReasoningEfforts: input.SupportedReasoningEfforts,
	}
}

func configuredModelOptionsFromUpdate(input configuredModelUpdate) configuredModelOptions {
	return configuredModelOptions{
		ContextWindowTokens:       input.ContextWindowTokens,
		MaxOutputTokens:           input.MaxOutputTokens,
		DefaultMaxOutputTokens:    input.DefaultMaxOutputTokens,
		DefaultCacheRetention:     input.DefaultCacheRetention,
		SupportsReasoning:         input.SupportsReasoning,
		DefaultReasoningEffort:    input.DefaultReasoningEffort,
		SupportedReasoningEfforts: input.SupportedReasoningEfforts,
	}
}

func boolPtrDefault(value *bool, fallback bool) bool {
	if value == nil {
		return fallback
	}
	return *value
}

func cloneIntPtr(value *int) *int {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneBoolPtr(value *bool) *bool {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func nonNilStringSlice(value []string) []string {
	if value == nil {
		return []string{}
	}
	return value
}

func sameProjectModelGrantIntent(record ProjectModelGrantRecord, input CreateProjectModelGrantInput) bool {
	return record.ProjectID == input.ProjectID &&
		record.ConfiguredModelID == input.ConfiguredModelID &&
		storeutil.SameIntPtr(record.ContextWindowTokens, input.ContextWindowTokens) &&
		storeutil.SameIntPtr(record.MaxOutputTokens, input.MaxOutputTokens) &&
		storeutil.SameIntPtr(record.DefaultMaxOutputTokens, input.DefaultMaxOutputTokens) &&
		record.DefaultCacheRetention == input.DefaultCacheRetention &&
		sameBoolPtr(record.SupportsTools, input.SupportsTools) &&
		sameBoolPtr(record.SupportsReasoning, input.SupportsReasoning) &&
		record.DefaultReasoningEffort == input.DefaultReasoningEffort &&
		slices.Equal(record.SupportedReasoningEfforts, input.SupportedReasoningEfforts) &&
		slices.Equal(record.InputModalities, input.InputModalities) &&
		slices.Equal(record.OutputModalities, input.OutputModalities)
}

func normalizeProjectModelGrantInput(input CreateProjectModelGrantInput) CreateProjectModelGrantInput {
	input.DefaultCacheRetention,
		input.DefaultReasoningEffort,
		input.SupportedReasoningEfforts,
		input.InputModalities,
		input.OutputModalities = normalizeConfiguredModelOptionFields(
		input.DefaultCacheRetention,
		input.DefaultReasoningEffort,
		input.SupportedReasoningEfforts,
		input.InputModalities,
		input.OutputModalities,
	)
	return input
}

func sameBoolPtr(left, right *bool) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func validateModelProviderAPIFormat(apiFormat modelprotocol.APIFormat) error {
	switch apiFormat {
	case modelprotocol.APIFormatOpenAIResponses,
		modelprotocol.APIFormatOpenAIChatCompletions,
		modelprotocol.APIFormatAnthropicMessages:
		return nil
	default:
		return fmt.Errorf("unsupported api_format %q", apiFormat)
	}
}

func DefaultModelProviderEndpointPath(apiFormat modelprotocol.APIFormat) string {
	switch apiFormat {
	case modelprotocol.APIFormatOpenAIResponses:
		return "/responses"
	case modelprotocol.APIFormatOpenAIChatCompletions:
		return "/chat/completions"
	case modelprotocol.APIFormatAnthropicMessages:
		return "/messages"
	default:
		return ""
	}
}

func DefaultModelProviderAuthKind(apiFormat modelprotocol.APIFormat) string {
	switch apiFormat {
	case modelprotocol.APIFormatOpenAIResponses, modelprotocol.APIFormatOpenAIChatCompletions:
		return ModelProviderAuthKindBearerToken
	case modelprotocol.APIFormatAnthropicMessages:
		return ModelProviderAuthKindAPIKeyHeader
	default:
		return ""
	}
}

func DefaultModelProviderAuthOptions(apiFormat modelprotocol.APIFormat, authKind string) json.RawMessage {
	switch authKind {
	case ModelProviderAuthKindBearerToken:
		return json.RawMessage(`{}`)
	case ModelProviderAuthKindAPIKeyHeader:
		// Only API formats with a standard API-key header get a default.
		// Other API-key-header configs must provide auth_options.header_name.
		if apiFormat == modelprotocol.APIFormatAnthropicMessages {
			return json.RawMessage(`{"header_name":"x-api-key"}`)
		}
		return json.RawMessage(`{}`)
	default:
		return json.RawMessage(`{}`)
	}
}

func normalizeModelProviderEndpointPath(apiFormat modelprotocol.APIFormat, endpointPath string) string {
	endpointPath = strings.TrimSpace(endpointPath)
	if endpointPath == "" {
		endpointPath = DefaultModelProviderEndpointPath(apiFormat)
	}
	return endpointPath
}

func normalizeModelProviderBaseURL(baseURL string) (string, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return "", fmt.Errorf("base_url is required: %w", storeerr.ErrInvalidModelProviderConfig)
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("base_url is invalid: %w", storeerr.ErrInvalidModelProviderConfig)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("base_url must use http or https: %w", storeerr.ErrInvalidModelProviderConfig)
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("base_url must include a host: %w", storeerr.ErrInvalidModelProviderConfig)
	}
	if parsed.User != nil {
		return "", fmt.Errorf("base_url cannot include user information: %w", storeerr.ErrInvalidModelProviderConfig)
	}
	if parsed.Scheme == "http" && !isLocalModelProviderHTTPHost(parsed.Hostname()) {
		return "", fmt.Errorf(
			"base_url must use https unless it targets localhost or a loopback IP: %w",
			storeerr.ErrInvalidModelProviderConfig,
		)
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("base_url cannot include query or fragment: %w", storeerr.ErrInvalidModelProviderConfig)
	}
	return baseURL, nil
}

func isLocalModelProviderHTTPHost(hostname string) bool {
	host := strings.ToLower(strings.TrimSpace(hostname))
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func normalizeModelProviderAuthKind(apiFormat modelprotocol.APIFormat, authKind string) string {
	authKind = strings.TrimSpace(authKind)
	if authKind == "" {
		authKind = DefaultModelProviderAuthKind(apiFormat)
	}
	return authKind
}

func normalizeModelProviderAuthOptions(
	apiFormat modelprotocol.APIFormat,
	authKind string,
	authOptions json.RawMessage,
) json.RawMessage {
	authOptions = storeutil.NormalizeJSON(authOptions)
	if string(authOptions) == "{}" {
		return storeutil.NormalizeJSON(DefaultModelProviderAuthOptions(apiFormat, authKind))
	}
	return authOptions
}

func validateModelProviderEndpointPath(endpointPath string) error {
	if endpointPath == "" {
		return fmt.Errorf("endpoint_path is required: %w", storeerr.ErrInvalidModelProviderConfig)
	}
	if !strings.HasPrefix(endpointPath, "/") {
		return fmt.Errorf("endpoint_path must start with /: %w", storeerr.ErrInvalidModelProviderConfig)
	}
	if strings.ContainsAny(endpointPath, "?#") {
		return fmt.Errorf("endpoint_path cannot include query or fragment: %w", storeerr.ErrInvalidModelProviderConfig)
	}
	return nil
}

// ValidateModelProviderAuth validates non-secret credential placement settings for a provider config.
func ValidateModelProviderAuth(authKind string, authOptions json.RawMessage) error {
	switch authKind {
	case ModelProviderAuthKindBearerToken:
		return validateEmptyJSONObject("auth_options", authOptions)
	case ModelProviderAuthKindAPIKeyHeader:
		_, err := ModelProviderAPIKeyHeaderName(authOptions)
		return err
	default:
		return fmt.Errorf("unsupported model provider auth_kind %q: %w", authKind, storeerr.ErrInvalidModelProviderConfig)
	}
}

// ModelProviderAPIKeyHeaderName returns the configured API-key header after validating it is safe for auth placement.
func ModelProviderAPIKeyHeaderName(authOptions json.RawMessage) (string, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(authOptions, &fields); err != nil {
		return "", fmt.Errorf("auth_options must be a JSON object: %w", err)
	}
	if fields == nil {
		return "", fmt.Errorf("auth_options must be a JSON object: %w", storeerr.ErrInvalidModelProviderConfig)
	}
	for key := range fields {
		if key != "header_name" {
			return "", fmt.Errorf("auth_options.%s is not supported for api_key_header: %w", key, storeerr.ErrInvalidModelProviderConfig)
		}
	}
	rawHeaderName, ok := fields["header_name"]
	if !ok {
		return "", fmt.Errorf("auth_options.header_name is required for api_key_header: %w", storeerr.ErrInvalidModelProviderConfig)
	}
	var headerName string
	if err := json.Unmarshal(rawHeaderName, &headerName); err != nil {
		return "", fmt.Errorf("auth_options.header_name must be a string: %w", err)
	}
	if err := validateModelProviderAuthHeaderName(headerName); err != nil {
		return "", err
	}
	return headerName, nil
}

func validateModelProviderAuthHeaderName(headerName string) error {
	if strings.TrimSpace(headerName) == "" {
		return fmt.Errorf(
			"auth_options.header_name is required for api_key_header: %w",
			storeerr.ErrInvalidModelProviderConfig,
		)
	}
	if !validHTTPHeaderFieldName(headerName) {
		return fmt.Errorf("auth_options.header_name is invalid: %w", storeerr.ErrInvalidModelProviderConfig)
	}
	switch strings.ToLower(headerName) {
	case "authorization",
		"content-type",
		"content-length",
		"host",
		"connection",
		"transfer-encoding",
		"idempotency-key",
		"x-idempotency-key":
		return fmt.Errorf(
			"auth_options.header_name %q is reserved for transport or auth headers: %w",
			headerName,
			storeerr.ErrInvalidModelProviderConfig,
		)
	default:
		return nil
	}
}

func validHTTPHeaderFieldName(name string) bool {
	if name == "" {
		return false
	}
	for i := range len(name) {
		if !isHTTPTokenChar(name[i]) {
			return false
		}
	}
	return true
}

func isHTTPTokenChar(value byte) bool {
	if value >= '0' && value <= '9' {
		return true
	}
	if value >= 'a' && value <= 'z' {
		return true
	}
	if value >= 'A' && value <= 'Z' {
		return true
	}
	switch value {
	case '!', '#', '$', '%', '&', '\'', '*', '+', '-', '.', '^', '_', '`', '|', '~':
		return true
	default:
		return false
	}
}

// Options a tenant may set on a cluster-managed OpenRouter provider: sampling and reasoning
// controls per https://openrouter.ai/docs/api-reference/parameters. Provider routing, model
// fallbacks, plugins, and transforms spend the shared account and are refused.
var clusterOpenRouterTenantOptionKeys = map[string]bool{
	"temperature": true, "top_p": true, "top_k": true, "min_p": true, "top_a": true,
	"frequency_penalty": true, "presence_penalty": true, "repetition_penalty": true,
	"seed": true, "stop": true, "logit_bias": true, "logprobs": true, "top_logprobs": true,
	"response_format": true, "parallel_tool_calls": true, "verbosity": true,
	"reasoning": true, "reasoning_effort": true,
	"usage": true, "user": true, "session_id": true, "prompt_cache_key": true,
}

func validateTenantModelOnClusterProvider(
	modelKind, providerKind management.Kind,
	apiVariant modelprotocol.APIVariant,
	providerModelSlug string,
	apiVariantOptions json.RawMessage,
) error {
	if modelKind != management.Tenant || providerKind != management.Cluster ||
		apiVariant != modelprotocol.APIVariantOpenRouter {
		return nil
	}
	if strings.Contains(providerModelSlug, ":") {
		return fmt.Errorf(
			"provider_model_slug cannot carry a routing variant suffix on a cluster-managed provider: %w",
			storeerr.ErrInvalidModelProviderConfig,
		)
	}
	var options map[string]json.RawMessage
	if err := json.Unmarshal(storeutil.NormalizeJSON(apiVariantOptions), &options); err != nil {
		return fmt.Errorf(
			"%s must be a JSON object: %w",
			apiVariantOptionsPath, errors.Join(err, storeerr.ErrInvalidModelProviderConfig),
		)
	}
	for key := range options {
		if !clusterOpenRouterTenantOptionKeys[key] {
			return fmt.Errorf(
				"api_variant_options.%s is not allowed on a cluster-managed provider: %w",
				key, storeerr.ErrInvalidModelProviderConfig,
			)
		}
	}
	return nil
}

func validateModelProviderAPIVariant(
	apiFormat modelprotocol.APIFormat,
	value modelprotocol.APIVariant,
) error {
	switch value {
	case modelprotocol.APIVariantDefault:
		return nil
	case modelprotocol.APIVariantOpenRouter:
		if apiFormat == modelprotocol.APIFormatOpenAIChatCompletions {
			return nil
		}
		return fmt.Errorf("api_variant %q requires api_format %q", value, modelprotocol.APIFormatOpenAIChatCompletions)
	case modelprotocol.APIVariantBedrock:
		return nil
	default:
		return fmt.Errorf("unsupported api_variant %q", value)
	}
}

func validateModelDefaultCacheRetention(value string) error {
	switch value {
	case "", ModelCacheRetentionNone, ModelCacheRetentionShort, ModelCacheRetentionLong:
		return nil
	default:
		return fmt.Errorf("unsupported model default_cache_retention %q: %w", value, storeerr.ErrInvalidModelProviderConfig)
	}
}

func validateEmptyJSONObject(name string, value json.RawMessage) error {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(value, &object); err != nil {
		return fmt.Errorf("%s must be a JSON object: %w", name, err)
	}
	if object == nil {
		return fmt.Errorf("%s must be a JSON object: %w", name, storeerr.ErrInvalidModelProviderConfig)
	}
	if len(object) != 0 {
		return fmt.Errorf(
			"%s has no supported options for this API format yet: %w",
			name,
			storeerr.ErrInvalidModelProviderConfig,
		)
	}
	return nil
}

func normalizeModelProviderRequestTimeoutMS(value int) int {
	if value == 0 {
		return int(DefaultModelProviderRequestTimeoutMS)
	}
	return value
}

func validateModelProviderRequestTimeoutMS(value int) error {
	if value <= 0 {
		return fmt.Errorf("request_timeout_ms must be positive: %w", storeerr.ErrInvalidModelProviderConfig)
	}
	if value > math.MaxInt32 {
		return fmt.Errorf("request_timeout_ms cannot exceed %d: %w", math.MaxInt32, storeerr.ErrInvalidModelProviderConfig)
	}
	return nil
}

const apiVariantOptionsPath = "api_variant_options"

func ValidateAPIVariantOptions(value json.RawMessage) (json.RawMessage, error) {
	value = storeutil.NormalizeJSON(value)
	if err := validateJSONObject(apiVariantOptionsPath, value); err != nil {
		return nil, err
	}
	return value, nil
}

func validateJSONObject(name string, value json.RawMessage) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(value, &raw); err != nil {
		return fmt.Errorf("%s must be a JSON object: %w", name, errors.Join(err, storeerr.ErrInvalidModelProviderConfig))
	}
	if raw == nil {
		return fmt.Errorf("%s must be a JSON object: %w", name, storeerr.ErrInvalidModelProviderConfig)
	}
	return nil
}

func validateOptionString(name, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s cannot be blank: %w", name, storeerr.ErrInvalidModelProviderConfig)
	}
	if strings.ContainsAny(value, "\r\n") {
		return fmt.Errorf("%s cannot contain control characters: %w", name, storeerr.ErrInvalidModelProviderConfig)
	}
	for _, r := range value {
		if r < 0x20 && r != '\t' {
			return fmt.Errorf("%s cannot contain control characters: %w", name, storeerr.ErrInvalidModelProviderConfig)
		}
	}
	if len(value) > 1024 {
		return fmt.Errorf("%s is too long: %w", name, storeerr.ErrInvalidModelProviderConfig)
	}
	return nil
}

func ValidateOpenRouterAppCategories(name string, categories []string) error {
	if len(categories) > 2 {
		return fmt.Errorf("%s may contain at most 2 categories: %w", name, storeerr.ErrInvalidModelProviderConfig)
	}
	for index, category := range categories {
		if err := validateOptionString(fmt.Sprintf("%s[%d]", name, index), category); err != nil {
			return err
		}
	}
	return nil
}

func validateConfiguredModelOptions(apiFormat modelprotocol.APIFormat, input configuredModelOptions) error {
	contextWindowTokens := input.ContextWindowTokens
	for _, field := range []struct {
		name  string
		value *int
		min   int
	}{
		{name: "context_window_tokens", value: &contextWindowTokens, min: 1},
		{name: "max_output_tokens", value: &input.MaxOutputTokens, min: 1},
		{name: "default_max_output_tokens", value: input.DefaultMaxOutputTokens, min: 1},
	} {
		if err := validateModelTokenField(field.name, field.value, field.min); err != nil {
			return err
		}
	}
	if input.DefaultMaxOutputTokens != nil && *input.DefaultMaxOutputTokens > input.MaxOutputTokens {
		return fmt.Errorf(
			"default_max_output_tokens cannot exceed max_output_tokens: %w",
			storeerr.ErrInvalidModelProviderConfig,
		)
	}
	if input.ContextWindowTokens <= input.MaxOutputTokens {
		return fmt.Errorf("context_window_tokens must exceed max_output_tokens: %w", storeerr.ErrInvalidModelProviderConfig)
	}
	if err := validateModelDefaultCacheRetention(input.DefaultCacheRetention); err != nil {
		return err
	}
	if !input.SupportsReasoning && (input.DefaultReasoningEffort != "" || len(input.SupportedReasoningEfforts) > 0) {
		return fmt.Errorf("reasoning defaults require supports_reasoning: %w", storeerr.ErrInvalidModelProviderConfig)
	}
	if input.DefaultReasoningEffort != "" && len(input.SupportedReasoningEfforts) > 0 &&
		!slices.Contains(input.SupportedReasoningEfforts, input.DefaultReasoningEffort) {
		return fmt.Errorf(
			"default_reasoning_effort must be listed in supported_reasoning_efforts: %w",
			storeerr.ErrInvalidModelProviderConfig,
		)
	}
	switch apiFormat {
	case modelprotocol.APIFormatOpenAIResponses,
		modelprotocol.APIFormatOpenAIChatCompletions:
	case modelprotocol.APIFormatAnthropicMessages:
		if input.SupportsReasoning || input.DefaultReasoningEffort != "" || len(input.SupportedReasoningEfforts) > 0 {
			return fmt.Errorf(
				"anthropic-messages reasoning options are not supported: %w",
				storeerr.ErrInvalidModelProviderConfig,
			)
		}
	}
	return nil
}

func configuredModelOptionsFromRevision(input ConfiguredModelRevisionRecord) configuredModelOptions {
	return configuredModelOptions{
		ContextWindowTokens:       input.ContextWindowTokens,
		MaxOutputTokens:           input.MaxOutputTokens,
		DefaultMaxOutputTokens:    input.DefaultMaxOutputTokens,
		DefaultCacheRetention:     input.DefaultCacheRetention,
		SupportsReasoning:         input.SupportsReasoning,
		DefaultReasoningEffort:    input.DefaultReasoningEffort,
		SupportedReasoningEfforts: input.SupportedReasoningEfforts,
	}
}

func configuredModelRevisionFromConfiguredModel(input ConfiguredModelRecord) ConfiguredModelRevisionRecord {
	return ConfiguredModelRevisionRecord{
		ID:                        input.CurrentRevisionID,
		OrgID:                     input.OrgID,
		ConfiguredModelID:         input.ID,
		ModelProviderConfigID:     input.ModelProviderConfigID,
		ProviderModelSlug:         input.ProviderModelSlug,
		ContextWindowTokens:       input.ContextWindowTokens,
		MaxOutputTokens:           input.MaxOutputTokens,
		DefaultMaxOutputTokens:    cloneIntPtr(input.DefaultMaxOutputTokens),
		DefaultCacheRetention:     input.DefaultCacheRetention,
		SupportsTools:             input.SupportsTools,
		SupportsReasoning:         input.SupportsReasoning,
		DefaultReasoningEffort:    input.DefaultReasoningEffort,
		SupportedReasoningEfforts: append([]string(nil), input.SupportedReasoningEfforts...),
		InputModalities:           append([]string(nil), input.InputModalities...),
		OutputModalities:          append([]string(nil), input.OutputModalities...),
		APIVariantOptions:         storeutil.NormalizeJSON(input.APIVariantOptions),
		CreatedAt:                 input.RevisionCreatedAt,
	}
}

func EffectiveConfiguredModelRevisionForProjectGrant(
	apiFormat modelprotocol.APIFormat,
	revision ConfiguredModelRevisionRecord,
	grant ProjectModelGrantRecord,
) (ConfiguredModelRevisionRecord, error) {
	if revision.ConfiguredModelID != grant.ConfiguredModelID {
		return ConfiguredModelRevisionRecord{}, fmt.Errorf(
			"project model grant does not match configured model revision: %w",
			storeerr.ErrInvalidModelProviderConfig,
		)
	}
	effective := revision
	if grant.ContextWindowTokens != nil {
		if *grant.ContextWindowTokens > revision.ContextWindowTokens {
			return ConfiguredModelRevisionRecord{}, fmt.Errorf(
				"project model grant context_window_tokens cannot exceed configured model context_window_tokens: %w",
				storeerr.ErrInvalidModelProviderConfig,
			)
		}
		effective.ContextWindowTokens = *grant.ContextWindowTokens
	}
	if grant.MaxOutputTokens != nil {
		if *grant.MaxOutputTokens > revision.MaxOutputTokens {
			return ConfiguredModelRevisionRecord{}, fmt.Errorf(
				"project model grant max_output_tokens cannot exceed configured model max_output_tokens: %w",
				storeerr.ErrInvalidModelProviderConfig,
			)
		}
		effective.MaxOutputTokens = *grant.MaxOutputTokens
	}
	if grant.DefaultMaxOutputTokens != nil {
		effective.DefaultMaxOutputTokens = cloneIntPtr(grant.DefaultMaxOutputTokens)
	}
	if grant.DefaultCacheRetention != "" {
		effective.DefaultCacheRetention = grant.DefaultCacheRetention
	}
	if grant.SupportsTools != nil {
		if *grant.SupportsTools && !revision.SupportsTools {
			return ConfiguredModelRevisionRecord{}, fmt.Errorf(
				"project model grant cannot enable tools for a model that does not support tools: %w",
				storeerr.ErrInvalidModelProviderConfig,
			)
		}
		effective.SupportsTools = *grant.SupportsTools
	}
	if grant.SupportsReasoning != nil {
		if *grant.SupportsReasoning && !revision.SupportsReasoning {
			return ConfiguredModelRevisionRecord{}, fmt.Errorf(
				"project model grant cannot enable reasoning for a model that does not support reasoning: %w",
				storeerr.ErrInvalidModelProviderConfig,
			)
		}
		effective.SupportsReasoning = *grant.SupportsReasoning
		if !effective.SupportsReasoning {
			effective.DefaultReasoningEffort = ""
			effective.SupportedReasoningEfforts = []string{}
		}
	}
	if len(grant.SupportedReasoningEfforts) > 0 {
		if !revision.SupportsReasoning {
			return ConfiguredModelRevisionRecord{}, fmt.Errorf(
				"project model grant cannot set reasoning efforts for a model that does not support reasoning: %w",
				storeerr.ErrInvalidModelProviderConfig,
			)
		}
		if len(revision.SupportedReasoningEfforts) > 0 {
			for _, effort := range grant.SupportedReasoningEfforts {
				if !slices.Contains(revision.SupportedReasoningEfforts, effort) {
					return ConfiguredModelRevisionRecord{}, fmt.Errorf(
						"project model grant supported_reasoning_efforts must be a subset of "+
							"configured model supported_reasoning_efforts: %w",
						storeerr.ErrInvalidModelProviderConfig,
					)
				}
			}
		}
		effective.SupportedReasoningEfforts = append([]string(nil), grant.SupportedReasoningEfforts...)
	}
	if grant.DefaultReasoningEffort != "" {
		effective.DefaultReasoningEffort = grant.DefaultReasoningEffort
	}
	if len(grant.InputModalities) > 0 {
		if len(revision.InputModalities) > 0 {
			for _, modality := range grant.InputModalities {
				if !slices.Contains(revision.InputModalities, modality) {
					return ConfiguredModelRevisionRecord{}, fmt.Errorf(
						"project model grant input_modalities must be a subset of configured model input_modalities: %w",
						storeerr.ErrInvalidModelProviderConfig,
					)
				}
			}
		}
		effective.InputModalities = append([]string(nil), grant.InputModalities...)
	}
	if len(grant.OutputModalities) > 0 {
		if len(revision.OutputModalities) > 0 {
			for _, modality := range grant.OutputModalities {
				if !slices.Contains(revision.OutputModalities, modality) {
					return ConfiguredModelRevisionRecord{}, fmt.Errorf(
						"project model grant output_modalities must be a subset of configured model output_modalities: %w",
						storeerr.ErrInvalidModelProviderConfig,
					)
				}
			}
		}
		effective.OutputModalities = append([]string(nil), grant.OutputModalities...)
	}
	if err := validateConfiguredModelOptions(apiFormat, configuredModelOptionsFromRevision(effective)); err != nil {
		return ConfiguredModelRevisionRecord{}, fmt.Errorf("project model grant effective options are invalid: %w", err)
	}
	return effective, nil
}

func EffectiveConfiguredModelForProjectGrant(
	apiFormat modelprotocol.APIFormat,
	configuredModel ConfiguredModelRecord,
	grant ProjectModelGrantRecord,
) (ConfiguredModelRevisionRecord, error) {
	return EffectiveConfiguredModelRevisionForProjectGrant(
		apiFormat,
		configuredModelRevisionFromConfiguredModel(configuredModel),
		grant,
	)
}

func EffectiveConfiguredModelRevisionForAgentOptions(
	apiFormat modelprotocol.APIFormat,
	revision ConfiguredModelRevisionRecord,
	options agentconfig.ModelOverrides,
) (ConfiguredModelRevisionRecord, error) {
	effective := revision
	if options.ContextWindowTokens != nil {
		if *options.ContextWindowTokens > revision.ContextWindowTokens {
			return ConfiguredModelRevisionRecord{}, fmt.Errorf(
				"agent model context_window_tokens cannot exceed project effective context_window_tokens: %w",
				storeerr.ErrInvalidModelProviderConfig,
			)
		}
		effective.ContextWindowTokens = *options.ContextWindowTokens
	}
	if options.DefaultMaxOutputTokens != nil {
		if *options.DefaultMaxOutputTokens > revision.MaxOutputTokens {
			return ConfiguredModelRevisionRecord{}, fmt.Errorf(
				"agent model default_max_output_tokens cannot exceed project effective max_output_tokens: %w",
				storeerr.ErrInvalidModelProviderConfig,
			)
		}
		effective.DefaultMaxOutputTokens = cloneIntPtr(options.DefaultMaxOutputTokens)
	}
	if options.CacheRetention != "" {
		effective.DefaultCacheRetention = options.CacheRetention
	}
	if options.ReasoningEffort != "" {
		if !revision.SupportsReasoning {
			return ConfiguredModelRevisionRecord{}, fmt.Errorf(
				"agent model reasoning.effort requires project effective supports_reasoning: %w",
				storeerr.ErrInvalidModelProviderConfig,
			)
		}
		effective.DefaultReasoningEffort = options.ReasoningEffort
	}
	if err := validateConfiguredModelOptions(apiFormat, configuredModelOptionsFromRevision(effective)); err != nil {
		return ConfiguredModelRevisionRecord{}, fmt.Errorf("agent model effective options are invalid: %w", err)
	}
	return effective, nil
}

func EffectiveConfiguredModelForAgentOptions(
	apiFormat modelprotocol.APIFormat,
	configuredModel ConfiguredModelRecord,
	grant ProjectModelGrantRecord,
	options agentconfig.ModelOverrides,
) (ConfiguredModelRevisionRecord, error) {
	effectiveProjectModel, err := EffectiveConfiguredModelForProjectGrant(apiFormat, configuredModel, grant)
	if err != nil {
		return ConfiguredModelRevisionRecord{}, err
	}
	return EffectiveConfiguredModelRevisionForAgentOptions(apiFormat, effectiveProjectModel, options)
}

func validateProjectModelGrantForConfiguredModel(
	apiFormat modelprotocol.APIFormat,
	configuredModel ConfiguredModelRecord,
	input CreateProjectModelGrantInput,
) error {
	grant := ProjectModelGrantRecord{
		OrgID:                     input.OrgID,
		ProjectID:                 input.ProjectID,
		ConfiguredModelID:         input.ConfiguredModelID,
		ContextWindowTokens:       cloneIntPtr(input.ContextWindowTokens),
		MaxOutputTokens:           cloneIntPtr(input.MaxOutputTokens),
		DefaultMaxOutputTokens:    cloneIntPtr(input.DefaultMaxOutputTokens),
		DefaultCacheRetention:     input.DefaultCacheRetention,
		SupportsTools:             cloneBoolPtr(input.SupportsTools),
		SupportsReasoning:         cloneBoolPtr(input.SupportsReasoning),
		DefaultReasoningEffort:    input.DefaultReasoningEffort,
		SupportedReasoningEfforts: append([]string(nil), input.SupportedReasoningEfforts...),
		InputModalities:           append([]string(nil), input.InputModalities...),
		OutputModalities:          append([]string(nil), input.OutputModalities...),
	}
	if _, err := EffectiveConfiguredModelForProjectGrant(apiFormat, configuredModel, grant); err != nil {
		return err
	}
	return nil
}

func validateModelTokenField(name string, value *int, minValue int) error {
	if value == nil {
		return nil
	}
	if *value < minValue {
		return fmt.Errorf("%s must be at least %d: %w", name, minValue, storeerr.ErrInvalidModelProviderConfig)
	}
	if *value > math.MaxInt32 {
		return fmt.Errorf("%s cannot exceed %d: %w", name, math.MaxInt32, storeerr.ErrInvalidModelProviderConfig)
	}
	return nil
}
