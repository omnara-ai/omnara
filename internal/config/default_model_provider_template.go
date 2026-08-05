package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
	"gopkg.in/yaml.v3"
)

type defaultModelProviderTemplateFile struct {
	Provisioner          string                       `yaml:"provisioner"`
	Name                 string                       `yaml:"name"`
	CredentialSecretName string                       `yaml:"credential_secret_name"`
	APIFormat            string                       `yaml:"api_format"`
	APIVariant           string                       `yaml:"api_variant"`
	BaseURL              string                       `yaml:"base_url"`
	EndpointPath         string                       `yaml:"endpoint_path"`
	RequestTimeoutMS     int                          `yaml:"request_timeout_ms"`
	AuthKind             string                       `yaml:"auth_kind"`
	AuthOptions          map[string]any               `yaml:"auth_options"`
	Models               []defaultConfiguredModelFile `yaml:"models"`
}

type defaultConfiguredModelFile struct {
	Name                      string         `yaml:"name"`
	ProviderModelSlug         string         `yaml:"provider_model_slug"`
	ContextWindowTokens       int            `yaml:"context_window_tokens"`
	MaxOutputTokens           *int           `yaml:"max_output_tokens"`
	DefaultMaxOutputTokens    *int           `yaml:"default_max_output_tokens"`
	DefaultCacheRetention     string         `yaml:"default_cache_retention"`
	SupportsTools             *bool          `yaml:"supports_tools"`
	SupportsReasoning         bool           `yaml:"supports_reasoning"`
	DefaultReasoningEffort    string         `yaml:"default_reasoning_effort"`
	SupportedReasoningEfforts []string       `yaml:"supported_reasoning_efforts"`
	InputModalities           []string       `yaml:"input_modalities"`
	OutputModalities          []string       `yaml:"output_modalities"`
	APIVariantOptions         map[string]any `yaml:"api_variant_options"`
}

func loadDefaultModelProviderTemplate(path string) (modelstore.DefaultModelProviderTemplate, error) {
	file, err := os.Open(path)
	if err != nil {
		return modelstore.DefaultModelProviderTemplate{}, fmt.Errorf("open OMNARA_DEFAULT_MODEL_PROVIDER_TEMPLATE: %w", err)
	}
	defer func() { _ = file.Close() }()
	decoder := yaml.NewDecoder(file)
	decoder.KnownFields(true)
	var parsed defaultModelProviderTemplateFile
	if err := decoder.Decode(&parsed); err != nil {
		return modelstore.DefaultModelProviderTemplate{}, fmt.Errorf("parse OMNARA_DEFAULT_MODEL_PROVIDER_TEMPLATE: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return modelstore.DefaultModelProviderTemplate{}, errors.New(
				"parse OMNARA_DEFAULT_MODEL_PROVIDER_TEMPLATE: trailing YAML document",
			)
		}
		return modelstore.DefaultModelProviderTemplate{}, fmt.Errorf("parse OMNARA_DEFAULT_MODEL_PROVIDER_TEMPLATE: %w", err)
	}
	return defaultModelProviderTemplateFromFile(parsed, "provider")
}

func defaultModelProviderTemplateFromFile(
	parsed defaultModelProviderTemplateFile,
	label string,
) (modelstore.DefaultModelProviderTemplate, error) {
	models := make([]modelstore.DefaultConfiguredModelTemplate, 0, len(parsed.Models))
	for index, parsedModel := range parsed.Models {
		model, err := defaultConfiguredModelTemplateFromFile(parsedModel, fmt.Sprintf("%s.models[%d]", label, index))
		if err != nil {
			return modelstore.DefaultModelProviderTemplate{}, err
		}
		models = append(models, model)
	}
	authOptions, err := marshalDefaultModelProviderOptions("auth_options", parsed.AuthOptions, label)
	if err != nil {
		return modelstore.DefaultModelProviderTemplate{}, err
	}
	template := modelstore.DefaultModelProviderTemplate{
		Provisioner:          parsed.Provisioner,
		Name:                 parsed.Name,
		CredentialSecretName: parsed.CredentialSecretName,
		APIFormat:            modelprotocol.APIFormat(parsed.APIFormat),
		APIVariant:           modelprotocol.APIVariant(parsed.APIVariant),
		BaseURL:              parsed.BaseURL,
		EndpointPath:         parsed.EndpointPath,
		RequestTimeoutMS:     parsed.RequestTimeoutMS,
		AuthKind:             parsed.AuthKind,
		AuthOptions:          authOptions,
		Models:               models,
	}
	prepared, err := modelstore.PrepareDefaultModelProviderTemplate(template)
	if err != nil {
		return modelstore.DefaultModelProviderTemplate{}, fmt.Errorf(
			"OMNARA_DEFAULT_MODEL_PROVIDER_TEMPLATE %s: %w",
			label,
			err,
		)
	}
	return prepared, nil
}

func defaultConfiguredModelTemplateFromFile(
	parsed defaultConfiguredModelFile,
	label string,
) (modelstore.DefaultConfiguredModelTemplate, error) {
	apiVariantOptions, err := marshalDefaultModelProviderOptions(
		"api_variant_options",
		parsed.APIVariantOptions,
		label,
	)
	if err != nil {
		return modelstore.DefaultConfiguredModelTemplate{}, err
	}
	maxOutputTokens, defaultMaxOutputTokens, err := modelstore.ResolveConfiguredModelOutputLimits(
		parsed.ContextWindowTokens,
		parsed.MaxOutputTokens,
		parsed.DefaultMaxOutputTokens,
	)
	if err != nil {
		return modelstore.DefaultConfiguredModelTemplate{}, fmt.Errorf(
			"OMNARA_DEFAULT_MODEL_PROVIDER_TEMPLATE %s: %w",
			label,
			err,
		)
	}
	return modelstore.DefaultConfiguredModelTemplate{
		Name:                      parsed.Name,
		ProviderModelSlug:         parsed.ProviderModelSlug,
		ContextWindowTokens:       parsed.ContextWindowTokens,
		MaxOutputTokens:           maxOutputTokens,
		DefaultMaxOutputTokens:    defaultMaxOutputTokens,
		DefaultCacheRetention:     parsed.DefaultCacheRetention,
		SupportsTools:             parsed.SupportsTools,
		SupportsReasoning:         parsed.SupportsReasoning,
		DefaultReasoningEffort:    parsed.DefaultReasoningEffort,
		SupportedReasoningEfforts: parsed.SupportedReasoningEfforts,
		InputModalities:           parsed.InputModalities,
		OutputModalities:          parsed.OutputModalities,
		APIVariantOptions:         apiVariantOptions,
	}, nil
}

func marshalDefaultModelProviderOptions(field string, value map[string]any, label string) (json.RawMessage, error) {
	if value == nil {
		value = map[string]any{}
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal OMNARA_DEFAULT_MODEL_PROVIDER_TEMPLATE %s.%s: %w", label, field, err)
	}
	return json.RawMessage(raw), nil
}
