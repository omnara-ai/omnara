package modelstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/jsoncanonical"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/management"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"golang.org/x/net/http/httpguts"
)

var sigV4ScopeComponentPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
var bedrockMantleEndpointPattern = regexp.MustCompile(`^bedrock-mantle\.([^.]+)\.api\.aws\.?$`)

func sameModelProviderConfigIntent(record ModelProviderConfigRecord, input CreateModelProviderConfigInput) bool {
	return record.ManagementKind == input.managementKind &&
		record.Name == input.Name &&
		record.APIFormat == input.APIFormat &&
		record.APIVariant == input.APIVariant &&
		record.BaseURL == input.BaseURL &&
		record.EndpointPath == input.EndpointPath &&
		record.RequestTimeoutMS == input.RequestTimeoutMS &&
		record.IdleTimeoutMS == input.IdleTimeoutMS &&
		record.AuthKind == input.AuthKind &&
		jsoncanonical.Equal(storeutil.NormalizeJSON(record.AuthOptions), storeutil.NormalizeJSON(input.AuthOptions)) &&
		record.CredentialSecretID == input.CredentialSecretID
}

func sameConfiguredModelIntent(record ConfiguredModelRecord, input CreateConfiguredModelInput) bool {
	return record.ModelProviderConfigID == input.ModelProviderConfigID &&
		record.Name == input.Name &&
		record.ProviderModelSlug == input.ProviderModelSlug &&
		record.ContextWindowTokens == input.ContextWindowTokens &&
		storeutil.SameIntPtr(record.MaxOutputTokens, input.MaxOutputTokens) &&
		storeutil.SameIntPtr(record.DefaultMaxOutputTokens, input.DefaultMaxOutputTokens) &&
		record.DefaultCacheRetention == input.DefaultCacheRetention &&
		record.SupportsTools == boolPtrDefault(input.SupportsTools, true) &&
		record.SupportsReasoning == input.SupportsReasoning &&
		record.DefaultReasoningEffort == input.DefaultReasoningEffort &&
		slices.Equal(record.SupportedReasoningEfforts, input.SupportedReasoningEfforts) &&
		slices.Equal(record.InputModalities, input.InputModalities) &&
		slices.Equal(record.OutputModalities, input.OutputModalities) &&
		jsoncanonical.Equal(
			storeutil.NormalizeJSON(record.APIVariantOptions),
			storeutil.NormalizeJSON(input.APIVariantOptions),
		)
}

type configuredModelOptions struct {
	ContextWindowTokens       int
	MaxOutputTokens           *int
	DefaultMaxOutputTokens    *int
	DefaultCacheRetention     string
	SupportsReasoning         bool
	DefaultReasoningEffort    string
	SupportedReasoningEfforts []string
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

func nonNilStringSlice(value []string) []string {
	if value == nil {
		return []string{}
	}
	return value
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
	case ModelProviderAuthKindSigV4:
		_, _, err := ModelProviderSigV4ServiceRegion(authOptions)
		return err
	default:
		return fmt.Errorf(
			"unsupported model provider auth_kind %q: %w",
			authKind,
			storeerr.ErrInvalidModelProviderConfig,
		)
	}
}

func ModelProviderCredentialSecretKind(authKind string) (secrets.Kind, error) {
	switch authKind {
	case ModelProviderAuthKindBearerToken, ModelProviderAuthKindAPIKeyHeader:
		return secrets.KindGeneric, nil
	case ModelProviderAuthKindSigV4:
		return secrets.KindAWSCredentials, nil
	default:
		return "", fmt.Errorf("unsupported model provider auth_kind %q: %w", authKind, storeerr.ErrInvalidModelProviderConfig)
	}
}

func ModelProviderSigV4ServiceRegion(authOptions json.RawMessage) (string, string, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(authOptions, &fields); err != nil {
		return "", "", fmt.Errorf("auth_options must be a JSON object: %w", err)
	}
	if fields == nil {
		return "", "", fmt.Errorf("auth_options must be a JSON object: %w", storeerr.ErrInvalidModelProviderConfig)
	}
	for key := range fields {
		if key != "service" && key != "region" {
			return "", "", fmt.Errorf("auth_options.%s is not supported for sigv4: %w", key, storeerr.ErrInvalidModelProviderConfig)
		}
	}
	service, err := modelProviderAuthOptionString(fields, "service", ModelProviderAuthKindSigV4)
	if err != nil {
		return "", "", err
	}
	region, err := modelProviderAuthOptionString(fields, "region", ModelProviderAuthKindSigV4)
	if err != nil {
		return "", "", err
	}
	return service, region, nil
}

func validateModelProviderSigV4EndpointRegion(
	baseURL, authKind string,
	authOptions json.RawMessage,
) error {
	if authKind != ModelProviderAuthKindSigV4 {
		return nil
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return fmt.Errorf("base_url is invalid: %w", storeerr.ErrInvalidModelProviderConfig)
	}
	match := bedrockMantleEndpointPattern.FindStringSubmatch(strings.ToLower(parsed.Hostname()))
	if match == nil {
		return nil
	}
	_, signingRegion, err := ModelProviderSigV4ServiceRegion(authOptions)
	if err != nil {
		return err
	}
	if signingRegion != match[1] {
		return fmt.Errorf(
			"auth_options.region %q must match Bedrock endpoint region %q: %w",
			signingRegion,
			match[1],
			storeerr.ErrInvalidModelProviderConfig,
		)
	}
	return nil
}

func modelProviderAuthOptionString(fields map[string]json.RawMessage, name, authKind string) (string, error) {
	rawValue, ok := fields[name]
	if !ok {
		return "", fmt.Errorf("auth_options.%s is required for %s: %w", name, authKind, storeerr.ErrInvalidModelProviderConfig)
	}
	var value string
	if err := json.Unmarshal(rawValue, &value); err != nil {
		return "", fmt.Errorf("auth_options.%s must be a string: %w", name, err)
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("auth_options.%s is required for %s: %w", name, authKind, storeerr.ErrInvalidModelProviderConfig)
	}
	if len(value) > 64 {
		return "", fmt.Errorf("auth_options.%s is too long: %w", name, storeerr.ErrInvalidModelProviderConfig)
	}
	if !sigV4ScopeComponentPattern.MatchString(value) {
		return "", fmt.Errorf("auth_options.%s must be lowercase alphanumeric segments separated by hyphens: %w", name, storeerr.ErrInvalidModelProviderConfig)
	}
	return value, nil
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
			return "", fmt.Errorf(
				"auth_options.%s is not supported for api_key_header: %w",
				key,
				storeerr.ErrInvalidModelProviderConfig,
			)
		}
	}
	rawHeaderName, ok := fields["header_name"]
	if !ok {
		return "", fmt.Errorf(
			"auth_options.header_name is required for api_key_header: %w",
			storeerr.ErrInvalidModelProviderConfig,
		)
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
	if !httpguts.ValidHeaderFieldName(headerName) {
		return fmt.Errorf("auth_options.header_name is invalid: %w", storeerr.ErrInvalidModelProviderConfig)
	}
	if reservedModelProviderHeaderName(headerName) {
		return fmt.Errorf(
			"auth_options.header_name %q is reserved: %w",
			headerName,
			storeerr.ErrInvalidModelProviderConfig,
		)
	}
	return nil
}

var (
	reservedModelProviderHeaders = map[string]bool{
		"authorization":          true,
		"accept":                 true,
		"accept-encoding":        true,
		"host":                   true,
		"connection":             true,
		"keep-alive":             true,
		"transfer-encoding":      true,
		"te":                     true,
		"trailer":                true,
		"upgrade":                true,
		"expect":                 true,
		"user-agent":             true,
		"x-http-method-override": true,
		"x-http-method":          true,
		"x-method-override":      true,
		"idempotency-key":        true,
		"x-idempotency-key":      true,
	}
	reservedModelProviderHeaderPrefixes = []string{"content-", "proxy-"}
	modelProviderProtocolHeaders        = []string{
		"anthropic-version",
		"x-amz-date",
		"x-amz-security-token",
		"x-amz-content-sha256",
	}
)

func reservedModelProviderHeaderName(headerName string, extra ...string) bool {
	name := strings.ToLower(headerName)
	return reservedModelProviderHeaders[name] ||
		slices.ContainsFunc(reservedModelProviderHeaderPrefixes, func(prefix string) bool {
			return strings.HasPrefix(name, prefix)
		}) ||
		slices.ContainsFunc(extra, func(reserved string) bool { return strings.EqualFold(reserved, name) })
}

func modelProviderAuthHeaders(authKind string, authOptions json.RawMessage) []string {
	if authKind != ModelProviderAuthKindAPIKeyHeader {
		return nil
	}
	headerName, err := ModelProviderAPIKeyHeaderName(authOptions)
	if err != nil {
		return nil
	}
	return []string{headerName}
}

func ModelProviderHeadersFromColumns(
	headers, secretHeaders json.RawMessage,
	authHeaders ...string,
) (ModelProviderHeaders, error) {
	var parsed ModelProviderHeaders
	if err := json.Unmarshal(storeutil.NormalizeJSON(headers), &parsed.Headers); err != nil || parsed.Headers == nil {
		return ModelProviderHeaders{}, fmt.Errorf(
			"headers must be an object of strings: %w",
			storeerr.ErrInvalidModelProviderConfig,
		)
	}
	if err := json.Unmarshal(storeutil.NormalizeJSON(secretHeaders), &parsed.SecretHeaders); err != nil ||
		parsed.SecretHeaders == nil {
		return ModelProviderHeaders{}, fmt.Errorf(
			"secret_headers must be an object of secret ids: %w",
			storeerr.ErrInvalidModelProviderConfig,
		)
	}
	if len(parsed.Headers)+len(parsed.SecretHeaders) > maxModelProviderHeaders {
		return ModelProviderHeaders{}, fmt.Errorf(
			"headers and secret_headers may contain at most %d entries combined: %w",
			maxModelProviderHeaders,
			storeerr.ErrInvalidModelProviderConfig,
		)
	}
	reserved := slices.Concat(modelProviderProtocolHeaders, authHeaders)
	seen := make(map[string]bool, len(parsed.Headers)+len(parsed.SecretHeaders))
	validateName := func(field, name string) error {
		lower := strings.ToLower(name)
		switch {
		case !httpguts.ValidHeaderFieldName(name):
			return fmt.Errorf("%s has an invalid header name %q: %w", field, name, storeerr.ErrInvalidModelProviderConfig)
		case reservedModelProviderHeaderName(name, reserved...):
			return fmt.Errorf(
				"%s.%s is reserved: %w",
				field,
				name,
				storeerr.ErrInvalidModelProviderConfig,
			)
		case seen[lower]:
			return fmt.Errorf("header %s is set more than once: %w", name, storeerr.ErrInvalidModelProviderConfig)
		}
		seen[lower] = true
		return nil
	}
	for name, value := range parsed.Headers {
		if err := validateName("headers", name); err != nil {
			return ModelProviderHeaders{}, err
		}
		if err := ValidateModelProviderHeaderValue("headers."+name, value); err != nil {
			return ModelProviderHeaders{}, err
		}
	}
	for name := range parsed.SecretHeaders {
		if err := validateName("secret_headers", name); err != nil {
			return ModelProviderHeaders{}, err
		}
	}
	return parsed, nil
}

func ValidateModelProviderHeaderValue(field, value string) error {
	if !httpguts.ValidHeaderFieldValue(value) {
		return fmt.Errorf("%s has an invalid value: %w", field, storeerr.ErrInvalidModelProviderConfig)
	}
	if strings.Trim(value, " \t") != value {
		return fmt.Errorf("%s cannot start or end with whitespace: %w", field, storeerr.ErrInvalidModelProviderConfig)
	}
	return nil
}

// On a cluster-managed OpenRouter provider a tenant may only use what shapes or bills their own
// requests. Option keys are an allowlist so a new OpenRouter parameter is refused until it is
// classified (https://openrouter.ai/docs/api-reference/parameters); model ids are refused when
// they reach the shared free pool, the web plugin, or workspace presets
// (https://openrouter.ai/docs/guides/routing/model-variants,
// https://openrouter.ai/docs/guides/features/web-search,
// https://openrouter.ai/docs/guides/features/presets).
var sharedOpenRouterTenantOptionKeys = map[string]bool{
	"temperature": true, "top_p": true, "top_k": true, "min_p": true, "top_a": true,
	"frequency_penalty": true, "presence_penalty": true, "repetition_penalty": true,
	"seed": true, "stop": true, "logit_bias": true, "response_format": true,
	"parallel_tool_calls": true, "verbosity": true, "reasoning": true, "reasoning_effort": true,
	"provider": true, "models": true, "route": true, "transforms": true,
	"session_id": true, "prompt_cache_key": true, "usage": true,
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
	if reachesSharedOpenRouterPool(providerModelSlug) {
		return fmt.Errorf(
			"provider_model_slug %q is not allowed on a cluster-managed provider: %w",
			providerModelSlug, storeerr.ErrInvalidModelProviderConfig,
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
		if !sharedOpenRouterTenantOptionKeys[key] {
			return fmt.Errorf(
				"api_variant_options.%s is not allowed on a cluster-managed provider: %w",
				key, storeerr.ErrInvalidModelProviderConfig,
			)
		}
	}
	var fallbacks []string
	if raw := options["models"]; len(raw) != 0 {
		if err := json.Unmarshal(raw, &fallbacks); err != nil {
			return fmt.Errorf(
				"api_variant_options.models must be a list of model ids: %w",
				errors.Join(err, storeerr.ErrInvalidModelProviderConfig),
			)
		}
	}
	for _, fallback := range fallbacks {
		if reachesSharedOpenRouterPool(fallback) {
			return fmt.Errorf(
				"api_variant_options.models entry %q is not allowed on a cluster-managed provider: %w",
				fallback, storeerr.ErrInvalidModelProviderConfig,
			)
		}
	}
	return nil
}

func reachesSharedOpenRouterPool(modelID string) bool {
	modelID = strings.ToLower(strings.TrimSpace(modelID))
	if strings.Contains(modelID, "@preset/") {
		return true
	}
	base, variants, _ := strings.Cut(strings.TrimPrefix(modelID, "~"), ":")
	if base == "openrouter/free" {
		return true
	}
	for _, variant := range strings.Split(variants, ":") {
		if variant == "free" || variant == "online" {
			return true
		}
	}
	return false
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

func validateModelProviderAuthAPIVariant(authKind string, apiVariant modelprotocol.APIVariant) error {
	if authKind == ModelProviderAuthKindSigV4 && apiVariant != modelprotocol.APIVariantBedrock {
		return fmt.Errorf(
			"auth_kind %q requires api_variant %q: %w",
			authKind,
			modelprotocol.APIVariantBedrock,
			storeerr.ErrInvalidModelProviderConfig,
		)
	}
	return nil
}

func validateModelDefaultCacheRetention(value string) error {
	switch value {
	case "", ModelCacheRetentionNone, ModelCacheRetentionShort, ModelCacheRetentionLong:
		return nil
	default:
		return fmt.Errorf(
			"unsupported model default_cache_retention %q: %w",
			value,
			storeerr.ErrInvalidModelProviderConfig,
		)
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

func normalizeModelProviderTimeoutMS(value int, fallback int64) int {
	if value == 0 {
		return int(fallback)
	}
	return value
}

func validateModelProviderTimeoutMS(name string, value int) error {
	if value <= 0 {
		return fmt.Errorf("%s must be positive: %w", name, storeerr.ErrInvalidModelProviderConfig)
	}
	if value > math.MaxInt32 {
		return fmt.Errorf(
			"%s cannot exceed %d: %w",
			name, math.MaxInt32,
			storeerr.ErrInvalidModelProviderConfig,
		)
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
		return fmt.Errorf(
			"%s must be a JSON object: %w",
			name,
			errors.Join(
				err,
				storeerr.ErrInvalidModelProviderConfig,
			),
		)
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
	if err := validateEffectiveModelOptions(apiFormat, input); err != nil {
		return err
	}
	if input.MaxOutputTokens != nil && input.ContextWindowTokens <= *input.MaxOutputTokens {
		return fmt.Errorf(
			"context_window_tokens must exceed max_output_tokens: %w",
			storeerr.ErrInvalidModelProviderConfig,
		)
	}
	return nil
}

// Effective context windows may be smaller than an inherited output capacity.
// Request preparation fits the output allowance to the remaining shared window.
func validateEffectiveModelOptions(apiFormat modelprotocol.APIFormat, input configuredModelOptions) error {
	contextWindowTokens := input.ContextWindowTokens
	for _, field := range []struct {
		name  string
		value *int
		min   int
	}{
		{name: "context_window_tokens", value: &contextWindowTokens, min: 2},
		{name: "max_output_tokens", value: input.MaxOutputTokens, min: 1},
		{name: "default_max_output_tokens", value: input.DefaultMaxOutputTokens, min: 1},
	} {
		if err := validateModelTokenField(field.name, field.value, field.min); err != nil {
			return err
		}
	}
	if input.DefaultMaxOutputTokens != nil &&
		input.MaxOutputTokens != nil &&
		*input.DefaultMaxOutputTokens > *input.MaxOutputTokens {
		return fmt.Errorf(
			"default_max_output_tokens cannot exceed max_output_tokens: %w",
			storeerr.ErrInvalidModelProviderConfig,
		)
	}
	if input.DefaultMaxOutputTokens != nil && input.ContextWindowTokens <= *input.DefaultMaxOutputTokens {
		return fmt.Errorf(
			"context_window_tokens must exceed default_max_output_tokens: %w",
			storeerr.ErrInvalidModelProviderConfig,
		)
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
		MaxOutputTokens:           storeutil.ClonePtr(input.MaxOutputTokens),
		DefaultMaxOutputTokens:    storeutil.ClonePtr(input.DefaultMaxOutputTokens),
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
		if *grant.MaxOutputTokens >= effective.ContextWindowTokens {
			return ConfiguredModelRevisionRecord{}, fmt.Errorf(
				"project model grant max_output_tokens must be less than effective context_window_tokens: %w",
				storeerr.ErrInvalidModelProviderConfig,
			)
		}
		if revision.MaxOutputTokens != nil && *grant.MaxOutputTokens > *revision.MaxOutputTokens {
			return ConfiguredModelRevisionRecord{}, fmt.Errorf(
				"project model grant max_output_tokens cannot exceed configured model max_output_tokens: %w",
				storeerr.ErrInvalidModelProviderConfig,
			)
		}
		effective.MaxOutputTokens = storeutil.ClonePtr(grant.MaxOutputTokens)
	}
	if grant.DefaultMaxOutputTokens != nil {
		effective.DefaultMaxOutputTokens = storeutil.ClonePtr(grant.DefaultMaxOutputTokens)
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
	if err := validateEffectiveModelOptions(apiFormat, configuredModelOptionsFromRevision(effective)); err != nil {
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
		if revision.MaxOutputTokens != nil && *options.DefaultMaxOutputTokens > *revision.MaxOutputTokens {
			return ConfiguredModelRevisionRecord{}, fmt.Errorf(
				"agent model default_max_output_tokens cannot exceed project effective max_output_tokens: %w",
				storeerr.ErrInvalidModelProviderConfig,
			)
		}
		effective.DefaultMaxOutputTokens = storeutil.ClonePtr(options.DefaultMaxOutputTokens)
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
	if err := validateEffectiveModelOptions(apiFormat, configuredModelOptionsFromRevision(effective)); err != nil {
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
		ContextWindowTokens:       storeutil.ClonePtr(input.ContextWindowTokens),
		MaxOutputTokens:           storeutil.ClonePtr(input.MaxOutputTokens),
		DefaultMaxOutputTokens:    storeutil.ClonePtr(input.DefaultMaxOutputTokens),
		DefaultCacheRetention:     input.DefaultCacheRetention,
		SupportsTools:             storeutil.ClonePtr(input.SupportsTools),
		SupportsReasoning:         storeutil.ClonePtr(input.SupportsReasoning),
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
